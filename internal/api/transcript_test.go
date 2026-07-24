package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blueshift/internal/ids"
)

// zwnjText carries a literal U+200C ZERO WIDTH NON-JOINER between its two
// morphemes. The transcript endpoint must surface it verbatim (no normalization):
// the byte sequence the client receives has to match the stored text exactly.
const zwnjText = "خوش\u200cحالم"

// seedTranscriptEpisode creates an org-A episode in the fake repo and returns its
// prefixed public id, ready for setTranscript.
func seedTranscriptEpisode(t *testing.T, repo *fakeRepo) string {
	t.Helper()
	row, err := repo.CreateEpisode(context.Background(), orgA, NewEpisode{
		Title: "Interview", SourceFilename: "m.mp4", Language: "fa", SizeBytes: 1,
	})
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	return ids.Encode(ids.Episode, row.PublicID)
}

func getTranscript(t *testing.T, router http.Handler, org, epID string) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(router, org, httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/transcript", nil))
}

// TestTranscriptRequiresAuth asserts an unauthenticated request is a 401 before
// any repo work.
func TestTranscriptRequiresAuth(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	// No principal in context (bypass doAs): the gate is simulated by the missing
	// auth context, exactly what an unauthenticated request presents to the mux.
	req := httptest.NewRequest(http.MethodGet, "/api/episodes/ep_"+strings.Repeat("0", 26)+"/transcript", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestTranscriptUnknownEpisodeIs404 asserts an id that names no row for the org
// is a 404 (never a 200 with an empty body that could imply existence).
func TestTranscriptUnknownEpisodeIs404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	rec := getTranscript(t, router, orgA, "ep_"+strings.Repeat("0", 26))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestTranscriptCrossOrgIs404 is the tenancy isolation test: org B may never read
// org A's transcript, and the denial is a 404 (indistinguishable from "no such
// episode"), so nothing about another org's data is observable.
func TestTranscriptCrossOrgIs404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	repo.setTranscript(epID, []TranscriptSegment{{Idx: 0, StartMs: 0, EndMs: 500, Text: "سلام"}})

	rec := getTranscript(t, router, orgB, epID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org status = %d, want 404", rec.Code)
	}
	// And org A still reads it — proving the 404 above was scoping, not a bad id.
	if ok := getTranscript(t, router, orgA, epID); ok.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200", ok.Code)
	}
}

// TestTranscriptEmptyIsOK asserts an existing episode with no segments yet is a
// 200 with segments: [] (the "awaiting transcript" state), not an error.
func TestTranscriptEmptyIsOK(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo) // no setTranscript: no segments

	rec := getTranscript(t, router, orgA, epID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		EpisodeID string            `json:"episode_id"`
		Language  string            `json:"language"`
		Segments  []json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.EpisodeID != epID {
		t.Errorf("episode_id = %q, want %q", body.EpisodeID, epID)
	}
	if body.Language != "fa" {
		t.Errorf("language = %q, want fa", body.Language)
	}
	if body.Segments == nil {
		t.Fatal("segments is null, want [] (empty array)")
	}
	if len(body.Segments) != 0 {
		t.Errorf("segments len = %d, want 0", len(body.Segments))
	}
	// The empty array must be literal "[]", never null.
	if !strings.Contains(rec.Body.String(), `"segments":[]`) {
		t.Errorf("body %s does not carry an empty segments array", rec.Body.String())
	}
}

// TestTranscriptShapeAndNeutrality drives the happy path and asserts the neutral
// DTO shape end-to-end: the exact top-level and per-segment key sets (an
// unexpected field is a leak), verbatim text incl. U+200C, the positional words
// tuple, and speaker_key nullability. Neutrality is proven positively (the field
// set is exactly the neutral contract) plus a negative check that no internal
// storage-key/id shape appears — this file lives under internal/api, which the
// vendor-leak gate greps, so provider names are never spelled here.
func TestTranscriptShapeAndNeutrality(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo)
	s1 := "S1"
	repo.setTranscript(epID, []TranscriptSegment{
		{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", SpeakerKey: s1, Words: []TranscriptWord{
			{Text: "سلام", StartMs: 0, EndMs: 520, Conf: 0.98},
		}},
		{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خیلی " + zwnjText, SpeakerKey: "", Words: []TranscriptWord{
			{Text: "خیلی", StartMs: 1000, EndMs: 1200, Conf: 0.96},
			{Text: zwnjText, StartMs: 1240, EndMs: 1600, Conf: 0.95},
		}},
	})

	rec := getTranscript(t, router, orgA, epID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// Top-level key set must be EXACTLY {episode_id, language, segments}.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode top: %v", err)
	}
	assertKeys(t, "top", top, "episode_id", "language", "segments")

	var body struct {
		EpisodeID string                       `json:"episode_id"`
		Language  string                       `json:"language"`
		Segments  []map[string]json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.HasPrefix(body.EpisodeID, "ep_") || body.EpisodeID != epID {
		t.Errorf("episode_id = %q, want prefixed %q", body.EpisodeID, epID)
	}
	if len(body.Segments) != 2 {
		t.Fatalf("segments len = %d, want 2", len(body.Segments))
	}
	for _, seg := range body.Segments {
		assertKeys(t, "segment", seg, "idx", "start_ms", "end_ms", "text", "speaker_key", "words")
	}

	// Segment 0: speaker_key present; single word tuple.
	if got := string(body.Segments[0]["speaker_key"]); got != `"S1"` {
		t.Errorf("seg0 speaker_key = %s, want \"S1\"", got)
	}
	// Segment 1: un-diarized -> speaker_key null (present, not omitted).
	if got := string(body.Segments[1]["speaker_key"]); got != "null" {
		t.Errorf("seg1 speaker_key = %s, want null", got)
	}

	// Verbatim: segment 1 text carries U+200C byte-for-byte.
	var seg1Text string
	if err := json.Unmarshal(body.Segments[1]["text"], &seg1Text); err != nil {
		t.Fatalf("decode seg1 text: %v", err)
	}
	if seg1Text != "خیلی "+zwnjText {
		t.Errorf("seg1 text = %q, want verbatim with ZWNJ", seg1Text)
	}
	if !strings.ContainsRune(seg1Text, '\u200c') {
		t.Error("seg1 text lost its U+200C ZWNJ")
	}

	// Words are positional [text, start_ms, end_ms, conf] tuples (arrays, not
	// objects). Assert the second word of segment 1 decodes positionally.
	var words1 [][]any
	if err := json.Unmarshal(body.Segments[1]["words"], &words1); err != nil {
		t.Fatalf("decode seg1 words: %v", err)
	}
	if len(words1) != 2 {
		t.Fatalf("seg1 words len = %d, want 2", len(words1))
	}
	w := words1[1]
	if len(w) != 4 {
		t.Fatalf("word tuple len = %d, want 4", len(w))
	}
	if w[0] != zwnjText {
		t.Errorf("word[0] = %v, want verbatim ZWNJ token", w[0])
	}
	if w[1] != float64(1240) || w[2] != float64(1600) {
		t.Errorf("word timings = %v/%v, want 1240/1600", w[1], w[2])
	}
	if w[3] != 0.95 {
		t.Errorf("word conf = %v, want 0.95", w[3])
	}

	// Negative neutrality: no storage-layout or internal-id shapes leak.
	lower := strings.ToLower(rec.Body.String())
	for _, bad := range []string{"masters/", "proxies/", "clips/", "object_key", "\"id\":", `"words":null`} {
		if strings.Contains(lower, bad) {
			t.Errorf("response leaks internal shape %q: %s", bad, rec.Body.String())
		}
	}
}

// TestTranscriptStoreErrorIs503 asserts a transcript-read failure surfaces as the
// neutral unavailable envelope (503 with a correlation id, no internal detail) —
// not a 500 that echoes the cause.
func TestTranscriptStoreErrorIs503(t *testing.T) {
	repo := newFakeRepo()
	repo.failTranscr = errors.New("segments read exploded: pg pool down at 10.0.0.5")
	router, _, _ := newEpisodeRouter(t, repo)

	epID := seedTranscriptEpisode(t, repo) // GetEpisode succeeds; EpisodeTranscript fails

	rec := getTranscript(t, router, orgA, epID)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":"unavailable"`) || !strings.Contains(body, `"error_id"`) {
		t.Errorf("body = %s, want neutral unavailable envelope with error_id", body)
	}
	// The raw cause (host, pool detail) must never reach the client.
	if strings.Contains(body, "pool") || strings.Contains(body, "10.0.0.5") || strings.Contains(body, "segments read") {
		t.Errorf("503 body leaks the internal cause: %s", body)
	}
}

// assertKeys fails unless m's key set is exactly want (order-independent). It is
// the leak guard: any field beyond the neutral contract fails the test.
func assertKeys(t *testing.T, what string, m map[string]json.RawMessage, want ...string) {
	t.Helper()
	if len(m) != len(want) {
		t.Errorf("%s has %d keys %v, want %d %v", what, len(m), keysOf(m), len(want), want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("%s missing key %q (have %v)", what, k, keysOf(m))
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
