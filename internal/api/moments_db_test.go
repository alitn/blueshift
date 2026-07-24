package api_test

// DB-backed integration tests for the moments endpoints. They wire the real
// router to a real *store.Store on the per-run scratch Postgres (internal/
// dbtest), seed an episode's segments + moment proposals through the
// production store path (ReplaceSegments + ReplaceMoments), and assert the
// neutral DTO and the status state machine end-to-end against the real
// UNIQUE(episode_id, rank) / CHECK-constrained table. Shares the harness
// (TestMain, openStore, pilotOrgUUID, seedEpisode) with transcript_db_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"blueshift/internal/asr"
	"blueshift/internal/auth"
	"blueshift/internal/pipeline"
	"blueshift/internal/store"
)

// momentQuoteZWNJ is rank 1's quote: a verbatim slice of the seeded segment 1
// text, U+200C included.
const momentQuoteZWNJ = "خیلی " + zwnjText

// seedMoments writes segments plus a two-moment proposal set through the
// production store path — deliberately rank-2-first, so reads must prove
// rank ordering.
func seedMoments(t *testing.T, ctx context.Context, st *store.Store, orgEnc, epEnc string) {
	t.Helper()
	segs := []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", Words: []asr.Word{{Text: "سلام", StartMs: 0, EndMs: 520, Conf: 0.98}}},
		{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خیلی " + zwnjText, Words: []asr.Word{
			{Text: "خیلی", StartMs: 1000, EndMs: 1200, Conf: 0.96},
			{Text: zwnjText, StartMs: 1240, EndMs: 1600, Conf: 0.95},
		}},
	}
	if err := st.ReplaceSegments(ctx, orgEnc, epEnc, segs); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	rows := []pipeline.MomentRow{
		{ProposedMoment: pipeline.ProposedMoment{Rank: 2, StartIdx: 0, EndIdx: 0,
			RationaleEn: "The greeting works as a cold open.", QuoteFa: "سلام"}, StartMs: 0, EndMs: 520},
		{ProposedMoment: pipeline.ProposedMoment{Rank: 1, StartIdx: 1, EndIdx: 1,
			RationaleEn: "The guest's reply is the quotable beat.", QuoteFa: momentQuoteZWNJ}, StartMs: 1000, EndMs: 1600},
	}
	if err := st.ReplaceMoments(ctx, orgEnc, epEnc, rows); err != nil {
		t.Fatalf("ReplaceMoments: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = st.ReplaceMoments(c, orgEnc, epEnc, nil) // wholesale delete before the episode rows go
	})
}

func getMomentsAs(router http.Handler, orgUUID, epID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/moments", nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Email: "e@x", OrgPublicID: orgUUID, Role: "editor"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func postStatusAs(router http.Handler, orgUUID, epID string, rank int, status string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost,
		"/api/episodes/"+epID+"/moments/"+strconv.Itoa(rank)+"/status",
		strings.NewReader(`{"status":"`+status+`"}`))
	req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Email: "e@x", OrgPublicID: orgUUID, Role: "editor"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestMomentsDBReadRankOrderedVerbatim seeds a scrambled proposal set through
// the store and asserts the endpoint returns the neutral DTO rank-ordered with
// the verbatim ZWNJ quote and the exact neutral key sets.
func TestMomentsDBReadRankOrderedVerbatim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, epUUID := seedEpisode(t, ctx, st, orgUUID)
	seedMoments(t, ctx, st, orgEnc, epEnc)

	router := newTranscriptRouter(t, st)
	rec := getMomentsAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode top: %v", err)
	}
	assertExactKeys(t, "top", top, "episode_id", "moments")

	var body struct {
		EpisodeID string `json:"episode_id"`
		Moments   []struct {
			Rank        int    `json:"rank"`
			StartIdx    int    `json:"start_idx"`
			EndIdx      int    `json:"end_idx"`
			StartMs     int    `json:"start_ms"`
			EndMs       int    `json:"end_ms"`
			RationaleEn string `json:"rationale_en"`
			QuoteFa     string `json:"quote_fa"`
			Status      string `json:"status"`
		} `json:"moments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.EpisodeID != epEnc {
		t.Errorf("episode_id = %q, want %q", body.EpisodeID, epEnc)
	}
	if len(body.Moments) != 2 {
		t.Fatalf("moments len = %d, want 2", len(body.Moments))
	}
	// Rank order despite the scrambled seed; every row starts proposed.
	for i, m := range body.Moments {
		if m.Rank != i+1 {
			t.Errorf("moment %d rank = %d, want %d", i, m.Rank, i+1)
		}
		if m.Status != "proposed" {
			t.Errorf("moment %d status = %q, want proposed", i, m.Status)
		}
	}
	// The rank-1 window is the quote-aligned ASR times, and the quote survived
	// Postgres byte-for-byte, ZWNJ included.
	m1 := body.Moments[0]
	if m1.StartIdx != 1 || m1.EndIdx != 1 || m1.StartMs != 1000 || m1.EndMs != 1600 {
		t.Errorf("rank 1 span/window = %+v, want idx 1..1, 1000..1600", m1)
	}
	if m1.QuoteFa != momentQuoteZWNJ || !strings.ContainsRune(m1.QuoteFa, '‌') {
		t.Errorf("rank 1 quote = %q, want verbatim with U+200C", m1.QuoteFa)
	}

	// Neutrality: no raw uuid, storage shapes, or review timestamps leak.
	lower := strings.ToLower(rec.Body.String())
	for _, bad := range []string{strings.ToLower(epUUID), "status_changed_at", "masters/", "proxies/", "clips/", `"moments":null`} {
		if bad != "" && strings.Contains(lower, bad) {
			t.Errorf("response leaks internal shape %q: %s", bad, rec.Body.String())
		}
	}
}

// TestMomentsDBEmptyIsOK asserts an episode whose moments stage has not run is
// a 200 with moments: [] — the awaiting state, not an error.
func TestMomentsDBEmptyIsOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	_, epEnc, _ := seedEpisode(t, ctx, st, orgUUID)

	router := newTranscriptRouter(t, st)
	rec := getMomentsAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"moments":[]`) {
		t.Errorf("body %s does not carry an empty moments array", rec.Body.String())
	}
}

// TestMomentsDBCrossOrgIs404 proves tenancy isolation on both routes against
// real org rows: a second org can neither read nor flip the pilot org's
// moments, and its requests 404 indistinguishably.
func TestMomentsDBCrossOrgIs404(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, _ := seedEpisode(t, ctx, st, orgUUID)
	seedMoments(t, ctx, st, orgEnc, epEnc)

	var otherUUID string
	if err := st.Pool().QueryRow(ctx, `INSERT INTO orgs (name) VALUES ('Blueshift Tenant Three') RETURNING public_id::text`).Scan(&otherUUID); err != nil {
		t.Fatalf("create second org: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM orgs WHERE public_id = $1::uuid`, otherUUID)
	})

	router := newTranscriptRouter(t, st)
	if rec := getMomentsAs(router, otherUUID, epEnc); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org GET status = %d, want 404", rec.Code)
	}
	if rec := postStatusAs(router, otherUUID, epEnc, 1, "approved"); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org POST status = %d, want 404", rec.Code)
	}
	// The owner's rank-1 moment is still proposed — the foreign flip never landed.
	rec := getMomentsAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"status":"approved"`) {
		t.Fatalf("owner read after cross-org flip = %d %s, want 200 all-proposed", rec.Code, rec.Body.String())
	}
}

// TestMomentsDBStatusTransitions walks the review state machine over the real
// CHECK-constrained rows: approve (200, stamped), approved->dismissed (409),
// undo to proposed (200), dismiss (200), unknown rank (404).
func TestMomentsDBStatusTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, _ := seedEpisode(t, ctx, st, orgUUID)
	seedMoments(t, ctx, st, orgEnc, epEnc)

	router := newTranscriptRouter(t, st)

	// proposed -> approved: 200 with the updated moment.
	rec := postStatusAs(router, orgUUID, epEnc, 1, "approved")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertExactKeys(t, "moment", m,
		"rank", "start_idx", "end_idx", "start_ms", "end_ms", "rationale_en", "quote_fa", "status")
	if got := string(m["status"]); got != `"approved"` {
		t.Errorf("status = %s, want approved", got)
	}
	// The flip stamped status_changed_at in the row (repo-side only, never serialized).
	ms, err := st.EpisodeMoments(ctx, orgUUID, epEnc)
	if err != nil || len(ms) != 2 {
		t.Fatalf("EpisodeMoments = (%d rows, %v)", len(ms), err)
	}
	if ms[0].StatusChangedAt.IsZero() {
		t.Error("status_changed_at not stamped by the approve")
	}

	// approved -> dismissed skips the undo: 409, row untouched.
	if rec := postStatusAs(router, orgUUID, epEnc, 1, "dismissed"); rec.Code != http.StatusConflict {
		t.Fatalf("approved->dismissed status = %d, want 409", rec.Code)
	}
	// Undo: approved -> proposed, then a clean dismiss.
	if rec := postStatusAs(router, orgUUID, epEnc, 1, "proposed"); rec.Code != http.StatusOK {
		t.Fatalf("undo status = %d, want 200", rec.Code)
	}
	if rec := postStatusAs(router, orgUUID, epEnc, 1, "dismissed"); rec.Code != http.StatusOK {
		t.Fatalf("dismiss status = %d, want 200", rec.Code)
	}
	// Unknown rank: 404, not 409.
	if rec := postStatusAs(router, orgUUID, epEnc, 99, "approved"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown rank status = %d, want 404", rec.Code)
	}
}
