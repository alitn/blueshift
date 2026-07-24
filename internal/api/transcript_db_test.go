package api_test

// DB-backed integration test for GET /api/episodes/{id}/transcript. It wires the
// real router (api.NewRouter) to a real *store.Store on a per-run scratch Postgres
// (internal/dbtest), seeds an episode's transcript through the production store
// path (ReplaceSegments + SetSegmentSpeakers), and asserts the endpoint's neutral
// DTO end-to-end: idx ordering (segments are seeded scrambled), verbatim text incl.
// U+200C round-tripped through Postgres jsonb, the positional words tuple, and
// speaker_key nullability from the real column.
//
// It lives in package api_test (external) so it may import internal/store, which
// itself imports internal/api — an in-package test could not, without an import
// cycle. This file is still under internal/api/, which the vendor-leak gate greps,
// so it names no provider: neutrality is proven positively (the response's field
// set is exactly the neutral contract) plus a negative check that no raw
// uuid/storage-key shape leaks.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"blueshift/internal/api"
	"blueshift/internal/asr"
	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/dbtest"
	"blueshift/internal/ids"
	"blueshift/internal/store"
)

// TestMain routes the whole api test binary (this package and package api) through
// a per-run scratch database when TEST_DATABASE_URL is set, and is a plain m.Run()
// otherwise — so the fake-backed handler tests stay green with no database while
// the DB-backed tests here provision, migrate, and drop their own scratch DB.
func TestMain(m *testing.M) { os.Exit(dbtest.RunMain(m)) }

// requireDB returns the per-run scratch DSN, or skips when no server is configured.
func requireDB(t *testing.T) string {
	t.Helper()
	dsn := dbtest.DSN()
	if dsn == "" {
		t.Skip("skip: TEST_DATABASE_URL not set (DB-backed api test needs a scratch Postgres)")
	}
	return dsn
}

// zwnjText carries a literal U+200C ZERO WIDTH NON-JOINER; the endpoint must
// surface it byte-for-byte (verbatim invariant, no normalization at rest).
const zwnjText = "خوش\u200cحالم"

func openStore(t *testing.T, ctx context.Context) *store.Store {
	t.Helper()
	st, err := store.Open(ctx, requireDB(t))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// pilotOrgUUID returns the canonical public id of the 'Blueshift Pilot' org
// seeded by migration 0002 — the form the session principal carries.
func pilotOrgUUID(t *testing.T, ctx context.Context, st *store.Store) string {
	t.Helper()
	var u string
	if err := st.Pool().QueryRow(ctx, `SELECT public_id::text FROM orgs WHERE name = 'Blueshift Pilot'`).Scan(&u); err != nil {
		t.Fatalf("find pilot org: %v", err)
	}
	return u
}

func newTranscriptRouter(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("blob.NewLocal: %v", err)
	}
	return api.NewRouter(api.Deps{
		Episodes: st,
		Blob:     local,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
}

// getTranscriptAs issues the transcript GET with the org principal in context,
// simulating what the server's auth gate injects.
func getTranscriptAs(router http.Handler, orgUUID, epID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/episodes/"+epID+"/transcript", nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Email: "e@x", OrgPublicID: orgUUID, Role: "editor"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// seedEpisode creates an episode under the pilot org and returns its encoded org
// id (org_…), encoded episode id (ep_…) and canonical episode uuid. It registers
// cleanup of the episode and its segments.
func seedEpisode(t *testing.T, ctx context.Context, st *store.Store, orgUUID string) (orgEnc, epEnc, epUUID string) {
	t.Helper()
	row, err := st.CreateEpisode(ctx, orgUUID, api.NewEpisode{
		Title: "Interview", SourceFilename: "m.mp4", Language: "fa", SizeBytes: 1,
	})
	if err != nil {
		t.Fatalf("CreateEpisode: %v", err)
	}
	orgEnc = ids.Encode(ids.Org, row.OrgPublicID)
	epEnc = ids.Encode(ids.Episode, row.PublicID)
	epUUID = canonicalUUID(row.PublicID)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM segments WHERE episode_id = (SELECT id FROM episodes WHERE public_id = $1::uuid)`, epUUID)
		_, _ = st.Pool().Exec(c, `DELETE FROM episodes WHERE public_id = $1::uuid`, epUUID)
	})
	return orgEnc, epEnc, epUUID
}

// TestTranscriptDBReadShapeOrderingVerbatim seeds a scrambled transcript through
// the store and asserts the endpoint returns the neutral DTO idx-ordered, with the
// verbatim ZWNJ text, positional word tuples, and per-segment speaker_key nulls.
func TestTranscriptDBReadShapeOrderingVerbatim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, epUUID := seedEpisode(t, ctx, st, orgUUID)

	// Seed segments out of idx order to prove the endpoint orders by idx.
	segs := []asr.Segment{
		{Idx: 2, StartMs: 2000, EndMs: 2600, Text: "پاسخ", Words: []asr.Word{{Text: "پاسخ", StartMs: 2000, EndMs: 2400, Conf: 0.90}}},
		{Idx: 0, StartMs: 0, EndMs: 900, Text: "سلام", Words: []asr.Word{{Text: "سلام", StartMs: 0, EndMs: 520, Conf: 0.98}}},
		{Idx: 1, StartMs: 1000, EndMs: 1600, Text: "خیلی " + zwnjText, Words: []asr.Word{
			{Text: "خیلی", StartMs: 1000, EndMs: 1200, Conf: 0.96},
			{Text: zwnjText, StartMs: 1240, EndMs: 1600, Conf: 0.95},
		}},
	}
	if err := st.ReplaceSegments(ctx, orgEnc, epEnc, segs); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}
	// Diarize only idx 0 and 2, leaving idx 1 un-attributed (speaker_key NULL).
	if err := st.SetSegmentSpeakers(ctx, orgEnc, epEnc, map[int]string{0: "S1", 2: "S2"}); err != nil {
		t.Fatalf("SetSegmentSpeakers: %v", err)
	}

	router := newTranscriptRouter(t, st)
	rec := getTranscriptAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// Top-level neutral key set — an unexpected field is a leak.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode top: %v", err)
	}
	assertExactKeys(t, "top", top, "episode_id", "language", "segments")

	var body struct {
		EpisodeID string                       `json:"episode_id"`
		Language  string                       `json:"language"`
		Segments  []map[string]json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.EpisodeID != epEnc || !strings.HasPrefix(body.EpisodeID, "ep_") {
		t.Errorf("episode_id = %q, want prefixed %q", body.EpisodeID, epEnc)
	}
	if body.Language != "fa" {
		t.Errorf("language = %q, want fa", body.Language)
	}
	if len(body.Segments) != 3 {
		t.Fatalf("segments len = %d, want 3", len(body.Segments))
	}

	// Ordering: idx must come back 0,1,2 despite the scrambled seed.
	wantSpeaker := []string{`"S1"`, "null", `"S2"`}
	for i, seg := range body.Segments {
		assertExactKeys(t, "segment", seg, "idx", "start_ms", "end_ms", "text", "speaker_key", "words")
		var idx int
		if err := json.Unmarshal(seg["idx"], &idx); err != nil {
			t.Fatalf("decode idx: %v", err)
		}
		if idx != i {
			t.Errorf("segment %d has idx %d, want %d (not ordered by idx)", i, idx, i)
		}
		if got := string(seg["speaker_key"]); got != wantSpeaker[i] {
			t.Errorf("segment idx %d speaker_key = %s, want %s", i, got, wantSpeaker[i])
		}
	}

	// Verbatim: idx 1 text carries U+200C byte-for-byte through Postgres jsonb.
	var seg1Text string
	if err := json.Unmarshal(body.Segments[1]["text"], &seg1Text); err != nil {
		t.Fatalf("decode seg1 text: %v", err)
	}
	if seg1Text != "خیلی "+zwnjText || !strings.ContainsRune(seg1Text, '\u200c') {
		t.Errorf("seg1 text = %q, want verbatim with U+200C", seg1Text)
	}

	// Words are positional [text, start_ms, end_ms, conf] arrays (not objects).
	var words1 [][]any
	if err := json.Unmarshal(body.Segments[1]["words"], &words1); err != nil {
		t.Fatalf("decode seg1 words: %v", err)
	}
	if len(words1) != 2 || len(words1[1]) != 4 {
		t.Fatalf("seg1 words shape = %v, want 2 tuples of 4", words1)
	}
	if words1[1][0] != zwnjText || words1[1][1] != float64(1240) || words1[1][2] != float64(1600) || words1[1][3] != 0.95 {
		t.Errorf("seg1 word[1] = %v, want [%q 1240 1600 0.95]", words1[1], zwnjText)
	}

	// Neutrality (negative): no storage-layout or raw-id shapes leak. The raw
	// episode uuid must never appear — only the prefixed ep_ id.
	lower := strings.ToLower(rec.Body.String())
	for _, bad := range []string{strings.ToLower(epUUID), "masters/", "proxies/", "clips/", "object_key", `"words":null`} {
		if bad != "" && strings.Contains(lower, bad) {
			t.Errorf("response leaks internal shape %q: %s", bad, rec.Body.String())
		}
	}
}

// TestTranscriptDBEmptyIsOK asserts an existing episode with no segments is a 200
// with an empty segments array (the awaiting-transcript state), not a 404/error.
func TestTranscriptDBEmptyIsOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	_, epEnc, _ := seedEpisode(t, ctx, st, orgUUID)

	router := newTranscriptRouter(t, st)
	rec := getTranscriptAs(router, orgUUID, epEnc)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"segments":[]`) {
		t.Errorf("body %s does not carry an empty segments array", rec.Body.String())
	}
}

// TestTranscriptDBCrossOrgIs404 seeds an episode under the pilot org and reads it
// as a second, real org: the store resolves the (existing) other org, finds no
// such episode for it, and the endpoint returns 404 — never another org's data.
func TestTranscriptDBCrossOrgIs404(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := openStore(t, ctx)
	orgUUID := pilotOrgUUID(t, ctx, st)
	orgEnc, epEnc, _ := seedEpisode(t, ctx, st, orgUUID)
	if err := st.ReplaceSegments(ctx, orgEnc, epEnc, []asr.Segment{
		{Idx: 0, StartMs: 0, EndMs: 500, Text: "سلام", Words: []asr.Word{{Text: "سلام", StartMs: 0, EndMs: 500, Conf: 0.9}}},
	}); err != nil {
		t.Fatalf("ReplaceSegments: %v", err)
	}

	// A second real tenant. It exists (so resolveOrg succeeds), but owns no episode.
	var otherUUID string
	if err := st.Pool().QueryRow(ctx, `INSERT INTO orgs (name) VALUES ('Blueshift Tenant Two') RETURNING public_id::text`).Scan(&otherUUID); err != nil {
		t.Fatalf("create second org: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = st.Pool().Exec(c, `DELETE FROM orgs WHERE public_id = $1::uuid`, otherUUID)
	})

	router := newTranscriptRouter(t, st)
	if rec := getTranscriptAs(router, otherUUID, epEnc); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	// The owner still reads it, proving the 404 was scoping, not a bad id.
	if rec := getTranscriptAs(router, orgUUID, epEnc); rec.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200", rec.Code)
	}
}

// canonicalUUID renders the 16-byte public id as the canonical 8-4-4-4-12
// hyphenated hex string — the form the DB stores and the session principal
// carries — so the neutrality check can assert the raw uuid never leaks and
// cleanup can target the exact row.
func canonicalUUID(b [16]byte) string {
	const hexd = "0123456789abcdef"
	var s [36]byte
	j := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			s[j] = '-'
			j++
		}
		s[j] = hexd[b[i]>>4]
		j++
		s[j] = hexd[b[i]&0x0f]
		j++
	}
	return string(s[:])
}

func assertExactKeys(t *testing.T, what string, m map[string]json.RawMessage, want ...string) {
	t.Helper()
	if len(m) != len(want) {
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		t.Errorf("%s has keys %v, want exactly %v", what, got, want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("%s missing key %q", what, k)
		}
	}
}
