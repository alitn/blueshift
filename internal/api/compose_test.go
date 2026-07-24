package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/ids"
)

// --- fake composer ------------------------------------------------------------

// fakeComposer implements MomentComposer with canned behavior. Org scoping is
// emulated against the shared fakeRepo, so an episode invisible to the
// principal reports found=false exactly like the seam.
type fakeComposer struct {
	mu      sync.Mutex
	repo    *fakeRepo
	results []ComposedMoment // compose answer (nil => empty valid set)
	kept    EpisodeMoment    // keep answer
	failErr error            // forced error for both calls

	prompts []string              // prompts received, in order
	keeps   []ComposedMomentInput // keep inputs received
}

func (f *fakeComposer) visible(orgPublicID, episodePublicID string) bool {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	s, ok := f.repo.eps[episodePublicID]
	return ok && s.owner == orgPublicID && !s.deleted
}

func (f *fakeComposer) hasTranscript(episodePublicID string) bool {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	return len(f.repo.transcripts[episodePublicID]) > 0
}

func (f *fakeComposer) ComposeMoments(_ context.Context, orgPublicID, episodePublicID, prompt string) ([]ComposedMoment, bool, error) {
	if f.failErr != nil {
		return nil, true, f.failErr
	}
	if !f.visible(orgPublicID, episodePublicID) {
		return nil, false, nil
	}
	if !f.hasTranscript(episodePublicID) {
		return nil, true, ErrNotTranscribed
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	return append([]ComposedMoment(nil), f.results...), true, nil
}

func (f *fakeComposer) KeepComposedMoment(_ context.Context, orgPublicID, episodePublicID string, in ComposedMomentInput) (EpisodeMoment, bool, error) {
	if f.failErr != nil {
		return EpisodeMoment{}, true, f.failErr
	}
	if !f.visible(orgPublicID, episodePublicID) {
		return EpisodeMoment{}, false, nil
	}
	if !f.hasTranscript(episodePublicID) {
		return EpisodeMoment{}, true, ErrNotTranscribed
	}
	// Mirror the seam's re-validation: the quote must be verbatim in the
	// (fake) transcript, or the keep refuses.
	f.repo.mu.Lock()
	verbatim := false
	for _, seg := range f.repo.transcripts[episodePublicID] {
		if strings.Contains(seg.Text, in.QuoteFa) {
			verbatim = true
			break
		}
	}
	f.repo.mu.Unlock()
	if !verbatim {
		return EpisodeMoment{}, true, ErrInvalidComposedMoment
	}
	f.mu.Lock()
	f.keeps = append(f.keeps, in)
	f.mu.Unlock()
	return f.kept, true, nil
}

// --- harness --------------------------------------------------------------------

// composeRouter wires the episode router plus a fake composer. now is
// controllable so the per-org token bucket can be drained and refilled
// deterministically.
func composeRouter(t *testing.T, repo *fakeRepo, c MomentComposer, now func() time.Time) http.Handler {
	t.Helper()
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if now == nil {
		now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	}
	return NewRouter(Deps{
		Authenticator: stubAuth{},
		Directory:     stubDir{},
		Codec:         auth.NewCodec("test-secret"),
		Logger:        discard(),
		Now:           now,
		Episodes:      repo,
		Blob:          local,
		Composer:      c,
	})
}

// composedResult is the canned single compose result: the guest-reply span
// with its ZWNJ quote and word-accurate window.
func composedResult() ComposedMoment {
	return ComposedMoment{Rank: 1, StartIdx: 1, EndIdx: 1, StartMs: 2960, EndMs: 4600,
		RationaleEn: "The guest's joy answers the request.", QuoteFa: zwnjQuote}
}

// seedComposeEpisode creates an episode for org with a transcript whose
// segment 1 carries the ZWNJ quote, so composes run and keeps validate.
func seedComposeEpisode(t *testing.T, repo *fakeRepo, org string) string {
	t.Helper()
	row, err := repo.CreateEpisode(context.Background(), org, NewEpisode{
		Title: "Interview", SourceFilename: "m.mp4", Language: "fa", SizeBytes: 1,
	})
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	epID := ids.Encode(ids.Episode, row.PublicID)
	repo.setTranscript(epID, []TranscriptSegment{
		{Idx: 0, StartMs: 0, EndMs: 2200, Text: "سلام به برنامه خوش آمدید"},
		{Idx: 1, StartMs: 2600, EndMs: 4600, Text: zwnjQuote},
	})
	return epID
}

func postCompose(router http.Handler, org, epID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/episodes/"+epID+"/moments/compose", strings.NewReader(body))
	return doAs(router, org, req)
}

func postKeep(router http.Handler, org, epID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/episodes/"+epID+"/moments/keep", strings.NewReader(body))
	return doAs(router, org, req)
}

// --- POST /api/episodes/{id}/moments/compose --------------------------------------

// TestComposeRequiresAuth asserts unauthenticated compose and keep are 401
// before any seam work.
func TestComposeRequiresAuth(t *testing.T) {
	repo := newFakeRepo()
	router := composeRouter(t, repo, &fakeComposer{repo: repo}, nil)

	epPath := "/api/episodes/ep_" + strings.Repeat("0", 26)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, epPath+"/moments/compose", strings.NewReader(`{"prompt":"x"}`)),
		httptest.NewRequest(http.MethodPost, epPath+"/moments/keep", strings.NewReader(`{"start_idx":0,"end_idx":0,"rationale_en":"r","quote_fa":"q"}`)),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", req.URL.Path, rec.Code)
		}
	}
}

// TestComposeRoutesOffWithoutSeam asserts a deployment without a composer has
// no compose surface at all (404 from the mux), while the rest of the episode
// routes still serve.
func TestComposeRoutesOffWithoutSeam(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo) // no Composer in Deps
	epID := seedTranscriptEpisode(t, repo)

	if rec := postCompose(router, orgA, epID, `{"prompt":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("compose without seam status = %d, want 404", rec.Code)
	}
	if rec := getMoments(t, router, orgA, epID); rec.Code != http.StatusOK {
		t.Errorf("moments read status = %d, want 200 (episode surface unaffected)", rec.Code)
	}
}

// TestComposeUnknownAndCrossOrgIs404 asserts an unknown id and a foreign org's
// episode are indistinguishable 404s for both routes.
func TestComposeUnknownAndCrossOrgIs404(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo, results: []ComposedMoment{composedResult()}}
	router := composeRouter(t, repo, comp, nil)
	epID := seedTranscriptEpisode(t, repo)

	unknown := "ep_" + strings.Repeat("0", 26)
	for _, id := range []string{unknown, epID} {
		org := orgA
		if id == epID {
			org = orgB // real episode, foreign principal
		}
		if rec := postCompose(router, org, id, `{"prompt":"find the joy"}`); rec.Code != http.StatusNotFound {
			t.Errorf("compose %s as %s status = %d, want 404", id, org, rec.Code)
		}
		if rec := postKeep(router, org, id, `{"start_idx":1,"end_idx":1,"rationale_en":"r","quote_fa":"q"}`); rec.Code != http.StatusNotFound {
			t.Errorf("keep %s as %s status = %d, want 404", id, org, rec.Code)
		}
	}
	if len(comp.prompts) != 0 || len(comp.keeps) != 0 {
		t.Errorf("seam was reached for a 404 request: prompts=%v keeps=%v", comp.prompts, comp.keeps)
	}
}

// TestComposePromptValidation asserts the body gate: malformed JSON, blank
// prompt, and an over-cap prompt are 400s with neutral codes, before the seam.
func TestComposePromptValidation(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo}
	router := composeRouter(t, repo, comp, nil)
	epID := seedComposeEpisode(t, repo, orgA)

	if rec := postCompose(router, orgA, epID, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400", rec.Code)
	}
	if rec := postCompose(router, orgA, epID, `{"prompt":"   "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("blank prompt status = %d, want 400", rec.Code)
	}
	// 501 runes (multi-byte Persian: the cap is runes, not bytes — 501 of
	// these is ~1000 bytes and must still trip the cap, while 500 must pass).
	over := strings.Repeat("م", 501)
	rec := postCompose(router, orgA, epID, `{"prompt":"`+over+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "prompt_too_long") {
		t.Errorf("over-cap prompt = %d %s, want 400 prompt_too_long", rec.Code, rec.Body.String())
	}
	if len(comp.prompts) != 0 {
		t.Errorf("seam was reached for invalid bodies: %v", comp.prompts)
	}
	if rec := postCompose(router, orgA, epID, `{"prompt":"`+strings.Repeat("م", 500)+`"}`); rec.Code != http.StatusOK {
		t.Errorf("exactly-500-rune prompt status = %d, want 200", rec.Code)
	}
}

// TestComposeUntranscribedIs409 asserts an episode with no segments yet is a
// clean 409 with the neutral not_transcribed code.
func TestComposeUntranscribedIs409(t *testing.T) {
	repo := newFakeRepo()
	router := composeRouter(t, repo, &fakeComposer{repo: repo}, nil)

	// An episode without a seeded transcript.
	row, err := repo.CreateEpisode(context.Background(), orgA, NewEpisode{Title: "t", SourceFilename: "s.mp4", Language: "fa", SizeBytes: 1})
	if err != nil {
		t.Fatalf("CreateEpisode: %v", err)
	}
	epID := ids.Encode(ids.Episode, row.PublicID)

	rec := postCompose(router, orgA, epID, `{"prompt":"find the joy"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not_transcribed") {
		t.Errorf("untranscribed compose = %d %s, want 409 not_transcribed", rec.Code, rec.Body.String())
	}
}

// TestComposeShapeAndNeutrality drives the happy path: the neutral envelope
// (episode_id + moments array), the exact per-result key set (no status — the
// results are ephemeral and unreviewed), rank order, and the verbatim ZWNJ
// quote. Zero results is a 200 with an empty array, never null and never an
// error.
func TestComposeShapeAndNeutrality(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo, results: []ComposedMoment{composedResult()}}
	router := composeRouter(t, repo, comp, nil)
	epID := seedComposeEpisode(t, repo, orgA)

	rec := postCompose(router, orgA, epID, `{"prompt":"find the joy"}`)
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
	if body.EpisodeID != epID {
		t.Errorf("episode_id = %q, want %q", body.EpisodeID, epID)
	}
	if len(body.Moments) != 1 {
		t.Fatalf("moments len = %d, want 1", len(body.Moments))
	}
	assertKeys(t, "composed moment", body.Moments[0],
		"rank", "start_idx", "end_idx", "start_ms", "end_ms", "rationale_en", "quote_fa")
	var quote string
	if err := json.Unmarshal(body.Moments[0]["quote_fa"], &quote); err != nil {
		t.Fatalf("decode quote: %v", err)
	}
	if quote != zwnjQuote || !strings.ContainsRune(quote, '‌') {
		t.Errorf("quote_fa = %q, want verbatim with ZWNJ", quote)
	}
	// The prompt crossed the seam verbatim.
	if len(comp.prompts) != 1 || comp.prompts[0] != "find the joy" {
		t.Errorf("seam prompts = %v, want the verbatim prompt", comp.prompts)
	}

	// Zero results: valid, 200, empty array.
	comp.results = nil
	rec = postCompose(router, orgA, epID, `{"prompt":"nothing matches this"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"moments":[]`) {
		t.Errorf("zero-result compose = %d %s, want 200 with moments:[]", rec.Code, rec.Body.String())
	}
}

// TestComposeRateLimited asserts the per-org token bucket: the 7th call inside
// one minute is a 429 with the neutral code, a different org is unaffected,
// and the bucket refills with time.
func TestComposeRateLimited(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo, results: []ComposedMoment{composedResult()}}
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	router := composeRouter(t, repo, comp, clock)
	epID := seedComposeEpisode(t, repo, orgA)
	epB := seedComposeEpisode(t, repo, orgB)

	for i := 0; i < 6; i++ {
		if rec := postCompose(router, orgA, epID, `{"prompt":"p"}`); rec.Code != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200 (body %s)", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := postCompose(router, orgA, epID, `{"prompt":"p"}`)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("7th call = %d %s, want 429 rate_limited", rec.Code, rec.Body.String())
	}
	// Another org has its own bucket.
	if rec := postCompose(router, orgB, epB, `{"prompt":"p"}`); rec.Code != http.StatusOK {
		t.Errorf("org B compose status = %d, want 200 (per-org buckets)", rec.Code)
	}
	// The bucket refills: +20s buys two tokens at 6/min.
	mu.Lock()
	now = now.Add(20 * time.Second)
	mu.Unlock()
	if rec := postCompose(router, orgA, epID, `{"prompt":"p"}`); rec.Code != http.StatusOK {
		t.Errorf("post-refill compose status = %d, want 200", rec.Code)
	}
}

// TestComposeSeamErrorIs503Neutral asserts an engine failure surfaces as the
// neutral unavailable envelope — never the cause, never a provider hint.
func TestComposeSeamErrorIs503Neutral(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo, failErr: errors.New("llm backend 10.1.2.3 exploded")}
	router := composeRouter(t, repo, comp, nil)
	epID := seedTranscriptEpisode(t, repo)

	for _, rec := range []*httptest.ResponseRecorder{
		postCompose(router, orgA, epID, `{"prompt":"p"}`),
		postKeep(router, orgA, epID, `{"start_idx":1,"end_idx":1,"rationale_en":"r","quote_fa":"q"}`),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"error":"unavailable"`) || !strings.Contains(body, "error_id") {
			t.Errorf("body = %s, want neutral unavailable envelope", body)
		}
		if strings.Contains(body, "10.1.2.3") || strings.Contains(body, "llm") {
			t.Errorf("503 body leaks the internal cause: %s", body)
		}
	}
}

// --- POST /api/episodes/{id}/moments/keep -----------------------------------------

// TestKeepPersistsAndReturnsMoment drives the keep happy path: the seam
// receives the asserted span/texts (no client times), and the response is the
// persisted moment DTO — approved, at its next-free rank.
func TestKeepPersistsAndReturnsMoment(t *testing.T) {
	repo := newFakeRepo()
	kept := EpisodeMoment{Rank: 3, StartIdx: 1, EndIdx: 1, StartMs: 2960, EndMs: 4600,
		RationaleEn: "Keep the joy beat.", QuoteFa: zwnjQuote, Status: momentStatusApproved,
		StatusChangedAt: time.Unix(1_700_000_000, 0)}
	comp := &fakeComposer{repo: repo, kept: kept}
	router := composeRouter(t, repo, comp, nil)
	epID := seedComposeEpisode(t, repo, orgA)

	rec := postKeep(router, orgA, epID,
		`{"start_idx":1,"end_idx":1,"rationale_en":"Keep the joy beat.","quote_fa":"`+zwnjQuote+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertKeys(t, "kept moment", m,
		"rank", "start_idx", "end_idx", "start_ms", "end_ms", "rationale_en", "quote_fa", "status")
	if got := string(m["rank"]); got != "3" {
		t.Errorf("rank = %s, want 3 (next free)", got)
	}
	if got := string(m["status"]); got != `"approved"` {
		t.Errorf("status = %s, want approved", got)
	}
	if len(comp.keeps) != 1 || comp.keeps[0].QuoteFa != zwnjQuote || comp.keeps[0].StartIdx != 1 {
		t.Errorf("seam keep input = %+v, want the asserted span/quote", comp.keeps)
	}
}

// TestKeepInvalidBodyIs400 asserts the keep body gate: malformed JSON, missing
// span fields, an inverted span, and blank texts are 400s before the seam.
func TestKeepInvalidBodyIs400(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo}
	router := composeRouter(t, repo, comp, nil)
	epID := seedTranscriptEpisode(t, repo)

	for _, body := range []string{
		`not json`,
		`{"rationale_en":"r","quote_fa":"q"}`, // missing span
		`{"start_idx":2,"end_idx":1,"rationale_en":"r","quote_fa":"q"}`,  // inverted
		`{"start_idx":-1,"end_idx":1,"rationale_en":"r","quote_fa":"q"}`, // negative
		`{"start_idx":0,"end_idx":0,"rationale_en":" ","quote_fa":"q"}`,  // blank rationale
		`{"start_idx":0,"end_idx":0,"rationale_en":"r","quote_fa":"  "}`, // blank quote
	} {
		if rec := postKeep(router, orgA, epID, body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s status = %d, want 400", body, rec.Code)
		}
	}
	if len(comp.keeps) != 0 {
		t.Errorf("seam was reached for invalid bodies: %v", comp.keeps)
	}
}

// TestKeepStaleQuoteIs409 asserts a keep whose quote no longer matches the
// transcript is a clean 409 with the neutral invalid_moment code.
func TestKeepStaleQuoteIs409(t *testing.T) {
	repo := newFakeRepo()
	comp := &fakeComposer{repo: repo}
	router := composeRouter(t, repo, comp, nil)
	epID := seedComposeEpisode(t, repo, orgA)

	rec := postKeep(router, orgA, epID,
		`{"start_idx":1,"end_idx":1,"rationale_en":"r","quote_fa":"این جمله دیگر در متن نیست"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "invalid_moment") {
		t.Errorf("stale keep = %d %s, want 409 invalid_moment", rec.Code, rec.Body.String())
	}
	if len(comp.keeps) != 0 {
		t.Errorf("a refused keep landed: %v", comp.keeps)
	}
}
