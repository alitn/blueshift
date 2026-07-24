package api_test

// DB-backed integration tests for the compose surface: the REAL router → the
// REAL moments.Composer seam → a fake-backed llm.Client (offline recording,
// real validate/retry/audit loop) → the REAL store on the per-run scratch
// Postgres. They prove, end to end over HTTP: the ephemeral compose response
// with WORD-ACCURATE times derived from the stored ASR word data, the
// llm_calls audit row tagged with the compose prompt_version, approve-to-keep
// persisting at the next free rank with source='prompt', and the kept row
// surviving a stage reprocess. Shares the harness with transcript_db_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blueshift/internal/api"
	"blueshift/internal/asr"
	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/llm"
	"blueshift/internal/moments"
	"blueshift/internal/pipeline"
	"blueshift/internal/store"

	_ "blueshift/internal/lang/fa"
)

// composeRecording is the fake model output for the seeded transcript of
// seedMoments: one result quoting segment 1's second word (the ZWNJ word), so
// the derived window must be that word's ASR times — 1240..1600 — not the
// segment's 1000..1600.
const composeRecording = `{"moments":[{"rank":1,"start_idx":1,"end_idx":1,` +
	`"rationale_en":"The guest's reply matches the request.","quote_fa":"` + zwnjText + `"}]}`

// newComposeDBRouter wires the real router over the real store with a real
// Composer seam backed by an offline recording — the exact production shape
// of cmd/app's fake mode.
func newComposeDBRouter(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	client, err := llm.NewFakeClient(llm.NewDBAuditor(st), llm.NewFakeEngine("bs-lm-1", "bs-lm-fake", []byte(composeRecording)))
	if err != nil {
		t.Fatalf("NewFakeClient: %v", err)
	}
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("blob.NewLocal: %v", err)
	}
	return api.NewRouter(api.Deps{
		Episodes: st,
		Blob:     local,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Composer: moments.Composer{
			Engine: moments.Engine{Gen: client, Labels: moments.LangLabelResolver{Label: "bs-lm-1"}},
			Store:  st,
		},
	})
}

func postJSONAs(router http.Handler, orgUUID, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Email: "e@x", OrgPublicID: orgUUID, Role: "editor"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// cleanupMoments removes ALL of an episode's moment rows (composed included —
// ReplaceMoments deliberately spares source='prompt', so the FK cleanup needs
// its own delete).
func cleanupMoments(t *testing.T, st *store.Store, epUUID string) {
	t.Helper()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM moments WHERE episode_id = (SELECT id FROM episodes WHERE public_id = $1::uuid)`, epUUID)
		_, _ = st.Pool().Exec(c, `DELETE FROM llm_calls WHERE episode_id = (SELECT id FROM episodes WHERE public_id = $1::uuid)`, epUUID)
	})
}

// TestComposeDBEndToEnd drives compose → keep → reprocess over the real stack:
// word-accurate ephemeral results, the compose-tagged audit row, next-free-rank
// persistence with source='prompt', and reprocess survival.
func TestComposeDBEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, epUUID := seedEpisode(t, ctx, st, orgUUID)
	cleanupMoments(t, st, epUUID)
	seedMoments(t, ctx, st, orgEnc, epEnc) // segments + a 2-moment auto set

	router := newComposeDBRouter(t, st)

	// --- compose: ephemeral, word-accurate, audited -------------------------
	rec := postJSONAs(router, orgUUID, "/api/episodes/"+epEnc+"/moments/compose",
		`{"prompt":"find the moment the guest is happy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("compose status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var composed struct {
		EpisodeID string `json:"episode_id"`
		Moments   []struct {
			Rank        int    `json:"rank"`
			StartIdx    int    `json:"start_idx"`
			EndIdx      int    `json:"end_idx"`
			StartMs     int    `json:"start_ms"`
			EndMs       int    `json:"end_ms"`
			RationaleEn string `json:"rationale_en"`
			QuoteFa     string `json:"quote_fa"`
		} `json:"moments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &composed); err != nil {
		t.Fatalf("decode compose response: %v", err)
	}
	if len(composed.Moments) != 1 {
		t.Fatalf("composed results = %d, want 1", len(composed.Moments))
	}
	cm := composed.Moments[0]
	// Word-accurate: the quote is segment 1's SECOND word, so the window is
	// that word's ASR times — never the segment bounds.
	if cm.StartMs != 1240 || cm.EndMs != 1600 {
		t.Errorf("composed window = %d..%d, want the quote word's ASR times 1240..1600", cm.StartMs, cm.EndMs)
	}
	if cm.QuoteFa != zwnjText || !strings.ContainsRune(cm.QuoteFa, '‌') {
		t.Errorf("composed quote = %q, want verbatim with ZWNJ", cm.QuoteFa)
	}
	// Ephemeral: the persisted moment set is untouched (still the 2 autos).
	if ms, _ := st.EpisodeMoments(ctx, orgUUID, epEnc); len(ms) != 2 {
		t.Fatalf("moments after compose = %d, want 2 (compose persists NOTHING)", len(ms))
	}
	// Audited: one ok llm_calls row tagged with the compose prompt_version.
	var auditCount int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM llm_calls WHERE prompt_version = 'compose-v1' AND status = 'ok'
		   AND episode_id = (SELECT id FROM episodes WHERE public_id = $1::uuid)`, epUUID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("compose-tagged llm_calls rows = %d, want 1", auditCount)
	}

	// --- keep: persists at next free rank, source='prompt', approved --------
	keepBody, _ := json.Marshal(map[string]any{
		"start_idx": cm.StartIdx, "end_idx": cm.EndIdx,
		"rationale_en": cm.RationaleEn, "quote_fa": cm.QuoteFa,
	})
	rec = postJSONAs(router, orgUUID, "/api/episodes/"+epEnc+"/moments/keep", string(keepBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("keep status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var kept struct {
		Rank    int    `json:"rank"`
		StartMs int    `json:"start_ms"`
		EndMs   int    `json:"end_ms"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &kept); err != nil {
		t.Fatalf("decode keep response: %v", err)
	}
	if kept.Rank != 3 || kept.Status != "approved" {
		t.Errorf("kept = rank %d status %q, want rank 3 approved", kept.Rank, kept.Status)
	}
	if kept.StartMs != 1240 || kept.EndMs != 1600 {
		t.Errorf("kept window = %d..%d, want the server-derived 1240..1600", kept.StartMs, kept.EndMs)
	}
	var src string
	if err := st.Pool().QueryRow(ctx,
		`SELECT source FROM moments WHERE rank = 3 AND episode_id = (SELECT id FROM episodes WHERE public_id = $1::uuid)`,
		epUUID).Scan(&src); err != nil {
		t.Fatalf("read kept source: %v", err)
	}
	if src != "prompt" {
		t.Errorf("kept source = %q, want prompt", src)
	}
	// The kept row now behaves like any moment: the review undo transition works.
	if rec := postStatusAs(router, orgUUID, epEnc, 3, "proposed"); rec.Code != http.StatusOK {
		t.Errorf("undo on kept moment status = %d, want 200 (kept rows join the review state machine)", rec.Code)
	}
	if rec := postStatusAs(router, orgUUID, epEnc, 3, "approved"); rec.Code != http.StatusOK {
		t.Errorf("re-approve on kept moment status = %d, want 200", rec.Code)
	}

	// --- reprocess: the stage replace spares the kept row -------------------
	if err := st.ReplaceMoments(ctx, orgEnc, epEnc, []pipeline.MomentRow{
		{ProposedMoment: pipeline.ProposedMoment{Rank: 1, StartIdx: 0, EndIdx: 0,
			RationaleEn: "Fresh proposal.", QuoteFa: "سلام"}, StartMs: 0, EndMs: 520},
	}); err != nil {
		t.Fatalf("ReplaceMoments (reprocess): %v", err)
	}
	ms, err := st.EpisodeMoments(ctx, orgUUID, epEnc)
	if err != nil {
		t.Fatalf("EpisodeMoments: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("moments after reprocess = %d, want 2 (1 fresh auto + the kept row)", len(ms))
	}
	if ms[1].Rank != 2 || ms[1].Status != "approved" || ms[1].QuoteFa != zwnjText {
		t.Errorf("kept row after reprocess = %+v, want rank 2, approved, verbatim quote", ms[1])
	}
}

// TestComposeDBStaleKeepIs409 proves the keep re-validates against the CURRENT
// transcript: after a re-transcribe changes the text, keeping the stale
// composed result refuses with the neutral invalid_moment code and persists
// nothing.
func TestComposeDBStaleKeepIs409(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, epUUID := seedEpisode(t, ctx, st, orgUUID)
	cleanupMoments(t, st, epUUID)
	seedMoments(t, ctx, st, orgEnc, epEnc)

	router := newComposeDBRouter(t, st)

	// The quote was valid against the seeded transcript… until a re-transcribe
	// rewrites segment 1 with different words.
	if err := st.ReplaceSegments(ctx, orgEnc, epEnc, []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", Words: []asr.Word{{Text: "سلام", StartMs: 0, EndMs: 520, Conf: 0.98}}},
		{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "متن تازه", Words: []asr.Word{
			{Text: "متن", StartMs: 1000, EndMs: 1200, Conf: 0.96},
			{Text: "تازه", StartMs: 1240, EndMs: 1600, Conf: 0.95},
		}},
	}); err != nil {
		t.Fatalf("re-transcribe: %v", err)
	}
	rec := postJSONAs(router, orgUUID, "/api/episodes/"+epEnc+"/moments/keep",
		`{"start_idx":1,"end_idx":1,"rationale_en":"stale","quote_fa":"`+zwnjText+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale keep status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_moment") {
		t.Errorf("stale keep body = %s, want neutral invalid_moment", rec.Body.String())
	}
	if ms, _ := st.EpisodeMoments(ctx, orgUUID, epEnc); len(ms) != 2 {
		t.Errorf("moments after refused keep = %d, want the 2 autos only", len(ms))
	}
}
