package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- fakeRepo moment surface --------------------------------------------------
//
// The moment methods mirror the store's contract exactly: org-scoped reads that
// yield nil for a foreign/unknown episode, and a status flip guarded to the
// legal transitions (proposed -> approved/dismissed, approved/dismissed ->
// proposed) that reports a clean false — never an error — for an unknown rank,
// an illegal transition, or a same-status no-op.

func (f *fakeRepo) EpisodeMoments(_ context.Context, orgPublicID, episodePublicID string) ([]EpisodeMoment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMoments != nil {
		return nil, f.failMoments
	}
	s, ok := f.eps[episodePublicID]
	if !ok || s.owner != orgPublicID || s.deleted {
		return nil, nil
	}
	return append([]EpisodeMoment(nil), f.moments[episodePublicID]...), nil
}

func (f *fakeRepo) SetMomentStatus(_ context.Context, orgPublicID, episodePublicID string, rank int, status string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.eps[episodePublicID]
	if !ok || s.owner != orgPublicID || s.deleted {
		return false, nil
	}
	ms := f.moments[episodePublicID]
	for i := range ms {
		if ms[i].Rank != rank {
			continue
		}
		from := ms[i].Status
		legal := (from == momentStatusProposed && (status == momentStatusApproved || status == momentStatusDismissed)) ||
			((from == momentStatusApproved || from == momentStatusDismissed) && status == momentStatusProposed)
		if !legal {
			return false, nil
		}
		ms[i].Status = status
		ms[i].StatusChangedAt = time.Unix(1_700_000_000, 0)
		return true, nil
	}
	return false, nil
}

// setMoments seeds an episode's moments so the handlers can be exercised
// without a database.
func (f *fakeRepo) setMoments(epID string, moments []EpisodeMoment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moments[epID] = moments
}

// --- harness -------------------------------------------------------------------

// zwnjQuote carries a U+200C ZERO WIDTH NON-JOINER (escaped for lint
// visibility): the moments endpoint must surface the Persian quote
// byte-for-byte (verbatim invariant).
const zwnjQuote = "خیلی خوش\u200cحالم که اینجا هستم"

// sampleMoments is a two-moment ranked set, rank 1 carrying the ZWNJ quote.
func sampleMoments() []EpisodeMoment {
	return []EpisodeMoment{
		{Rank: 1, StartIdx: 1, EndIdx: 1, StartMs: 2600, EndMs: 4600,
			RationaleEn: "The guest's reply is the quotable beat.", QuoteFa: zwnjQuote, Status: momentStatusProposed},
		{Rank: 2, StartIdx: 0, EndIdx: 0, StartMs: 0, EndMs: 2200,
			RationaleEn: "The greeting works as a cold open.", QuoteFa: "سلام به برنامه خوش آمدید", Status: momentStatusProposed},
	}
}

func getMoments(t *testing.T, router http.Handler, org, epID string) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(router, org, httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/moments", nil))
}

func postStatus(t *testing.T, router http.Handler, org, epID string, rank int, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/episodes/"+epID+"/moments/"+strconv.Itoa(rank)+"/status", strings.NewReader(body))
	return doAs(router, org, req)
}

// --- GET /api/episodes/{id}/moments ---------------------------------------------

// TestMomentsRequireAuth asserts unauthenticated GET and POST are 401 before
// any repo work.
func TestMomentsRequireAuth(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epPath := "/api/episodes/ep_" + strings.Repeat("0", 26)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, epPath+"/moments", nil),
		httptest.NewRequest(http.MethodPost, epPath+"/moments/1/status", strings.NewReader(`{"status":"approved"}`)),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}

// TestMomentsUnknownEpisodeIs404 asserts an id naming no row for the org is a
// 404 for both routes.
func TestMomentsUnknownEpisodeIs404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	unknown := "ep_" + strings.Repeat("0", 26)
	if rec := getMoments(t, router, orgA, unknown); rec.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want 404", rec.Code)
	}
	if rec := postStatus(t, router, orgA, unknown, 1, `{"status":"approved"}`); rec.Code != http.StatusNotFound {
		t.Errorf("POST status = %d, want 404", rec.Code)
	}
}

// TestMomentsCrossOrgIs404 is the tenancy isolation test: org B may neither
// read nor flip org A's moments, and the denial is an indistinguishable 404.
func TestMomentsCrossOrgIs404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setMoments(epID, sampleMoments())

	if rec := getMoments(t, router, orgB, epID); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org GET status = %d, want 404", rec.Code)
	}
	if rec := postStatus(t, router, orgB, epID, 1, `{"status":"approved"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org POST status = %d, want 404", rec.Code)
	}
	// Org A still reads them untouched — the 404s were scoping, not bad ids,
	// and the foreign flip never landed.
	rec := getMoments(t, router, orgA, epID)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner GET status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"proposed"`) {
		t.Errorf("owner read shows a foreign flip landed: %s", rec.Body.String())
	}
}

// TestMomentsEmptyIsOK asserts an existing episode with no moments yet is a
// 200 with moments: [] (the "awaiting moments" state), never null.
func TestMomentsEmptyIsOK(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo) // no setMoments: no proposals yet

	rec := getMoments(t, router, orgA, epID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"moments":[]`) {
		t.Errorf("body %s does not carry an empty moments array", rec.Body.String())
	}
}

// TestMomentsShapeAndNeutrality drives the happy path and asserts the neutral
// DTO end-to-end: the exact top-level and per-moment key sets (an unexpected
// field — status_changed_at included — is a leak), rank order, and the verbatim
// ZWNJ quote. This file lives under internal/api, which the vendor-leak gate
// greps, so no provider is ever spelled here.
func TestMomentsShapeAndNeutrality(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setMoments(epID, sampleMoments())

	rec := getMoments(t, router, orgA, epID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode top: %v", err)
	}
	assertKeys(t, "top", top, "episode_id", "moments")

	var body struct {
		EpisodeID string                       `json:"episode_id"`
		Moments   []map[string]json.RawMessage `json:"moments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.EpisodeID != epID || !strings.HasPrefix(body.EpisodeID, "ep_") {
		t.Errorf("episode_id = %q, want prefixed %q", body.EpisodeID, epID)
	}
	if len(body.Moments) != 2 {
		t.Fatalf("moments len = %d, want 2", len(body.Moments))
	}
	for i, m := range body.Moments {
		assertKeys(t, "moment", m,
			"rank", "start_idx", "end_idx", "start_ms", "end_ms", "rationale_en", "quote_fa", "status")
		var rank int
		if err := json.Unmarshal(m["rank"], &rank); err != nil {
			t.Fatalf("decode rank: %v", err)
		}
		if rank != i+1 {
			t.Errorf("moment %d rank = %d, want %d (rank order)", i, rank, i+1)
		}
	}

	// Verbatim: the rank-1 quote carries U+200C byte-for-byte.
	var quote string
	if err := json.Unmarshal(body.Moments[0]["quote_fa"], &quote); err != nil {
		t.Fatalf("decode quote: %v", err)
	}
	if quote != zwnjQuote || !strings.ContainsRune(quote, '‌') {
		t.Errorf("quote_fa = %q, want verbatim with ZWNJ", quote)
	}

	// Negative neutrality: no internal shapes or review timestamps leak.
	lower := strings.ToLower(rec.Body.String())
	for _, bad := range []string{"status_changed_at", "masters/", "proxies/", "clips/", "\"id\":", `"moments":null`} {
		if strings.Contains(lower, bad) {
			t.Errorf("response leaks internal shape %q: %s", bad, rec.Body.String())
		}
	}
}

// TestMomentsStoreErrorIs503 asserts a moments-read failure surfaces as the
// neutral unavailable envelope, never echoing the cause.
func TestMomentsStoreErrorIs503(t *testing.T) {
	repo := newFakeRepo()
	repo.failMoments = errors.New("moments read exploded: pg pool down at 10.0.0.5")
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)

	rec := getMoments(t, router, orgA, epID)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":"unavailable"`) || !strings.Contains(body, `"error_id"`) {
		t.Errorf("body = %s, want neutral unavailable envelope with error_id", body)
	}
	if strings.Contains(body, "pool") || strings.Contains(body, "10.0.0.5") {
		t.Errorf("503 body leaks the internal cause: %s", body)
	}
}

// --- POST /api/episodes/{id}/moments/{rank}/status -------------------------------

// TestSetMomentStatusFlipsAndUndoes drives approve -> undo -> dismiss and
// asserts each 200 body is the updated neutral moment DTO.
func TestSetMomentStatusFlipsAndUndoes(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setMoments(epID, sampleMoments())

	steps := []struct{ status string }{
		{momentStatusApproved}, // proposed -> approved
		{momentStatusProposed}, // the undo
		{momentStatusDismissed},
	}
	for _, s := range steps {
		rec := postStatus(t, router, orgA, epID, 1, `{"status":"`+s.status+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s status = %d, want 200 (body %s)", s.status, rec.Code, rec.Body.String())
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		assertKeys(t, "moment", m,
			"rank", "start_idx", "end_idx", "start_ms", "end_ms", "rationale_en", "quote_fa", "status")
		if got := string(m["status"]); got != `"`+s.status+`"` {
			t.Errorf("status = %s, want %q", got, s.status)
		}
		if got := string(m["rank"]); got != "1" {
			t.Errorf("rank = %s, want 1", got)
		}
	}

	// Rank 2 was never touched.
	rec := getMoments(t, router, orgA, epID)
	var body struct {
		Moments []struct {
			Rank   int    `json:"rank"`
			Status string `json:"status"`
		} `json:"moments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if body.Moments[1].Rank != 2 || body.Moments[1].Status != momentStatusProposed {
		t.Errorf("rank 2 = %+v, want untouched proposed", body.Moments[1])
	}
}

// TestSetMomentStatusIllegalTransitionIs409 asserts approved -> dismissed and
// the same-status no-op are clean 409 refusals that leave the row untouched.
func TestSetMomentStatusIllegalTransitionIs409(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setMoments(epID, sampleMoments())

	if rec := postStatus(t, router, orgA, epID, 1, `{"status":"approved"}`); rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200", rec.Code)
	}
	// approved -> dismissed skips the undo: illegal.
	rec := postStatus(t, router, orgA, epID, 1, `{"status":"dismissed"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("approved->dismissed status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_transition") {
		t.Errorf("body = %s, want invalid_transition", rec.Body.String())
	}
	// Same-status no-op: also a refusal.
	if rec := postStatus(t, router, orgA, epID, 1, `{"status":"approved"}`); rec.Code != http.StatusConflict {
		t.Errorf("approved->approved status = %d, want 409", rec.Code)
	}
	// The row still reads approved.
	list := getMoments(t, router, orgA, epID)
	if !strings.Contains(list.Body.String(), `"status":"approved"`) {
		t.Errorf("row changed by refused transitions: %s", list.Body.String())
	}
}

// TestSetMomentStatusUnknownRankIs404 asserts a rank naming no moment — numeric
// or unparseable — is a 404, distinguished from the 409 transition refusal.
func TestSetMomentStatusUnknownRankIs404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setMoments(epID, sampleMoments())

	if rec := postStatus(t, router, orgA, epID, 99, `{"status":"approved"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown rank status = %d, want 404", rec.Code)
	}
	// Non-numeric and non-positive rank path segments name no moment either.
	for _, rank := range []string{"abc", "0", "-1"} {
		req := httptest.NewRequest(http.MethodPost,
			"/api/episodes/"+epID+"/moments/"+rank+"/status", strings.NewReader(`{"status":"approved"}`))
		if rec := doAs(router, orgA, req); rec.Code != http.StatusNotFound {
			t.Errorf("rank %q status = %d, want 404", rank, rec.Code)
		}
	}
}

// TestSetMomentStatusInvalidBodyIs400 asserts a malformed body or a status
// outside the closed set is a 400 before any store work.
func TestSetMomentStatusInvalidBodyIs400(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setMoments(epID, sampleMoments())

	if rec := postStatus(t, router, orgA, epID, 1, `{"status":"deleted"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bogus status = %d, want 400", rec.Code)
	}
	if rec := postStatus(t, router, orgA, epID, 1, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400", rec.Code)
	}
	// Neither request flipped anything.
	list := getMoments(t, router, orgA, epID)
	if strings.Contains(list.Body.String(), `"status":"deleted"`) || !strings.Contains(list.Body.String(), `"status":"proposed"`) {
		t.Errorf("invalid request mutated state: %s", list.Body.String())
	}
}
