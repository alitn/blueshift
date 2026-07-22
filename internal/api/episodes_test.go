package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"blueshift/internal/auth"
	"blueshift/internal/blob"
	"blueshift/internal/ids"
)

// --- fake org-scoped episode repo -------------------------------------------

type storedEpisode struct {
	row   EpisodeRow
	owner string // org public id that owns the row
}

// fakeRepo emulates the store's org scoping: a row is only visible to the org
// that created it. It is the seam where the cross-org isolation test lives — a
// principal of org B asking for org A's episode gets found=false, exactly as the
// SQL WHERE org_id = $2 would produce.
type fakeRepo struct {
	mu         sync.Mutex
	eps        map[string]storedEpisode // key: ep_ encoded public id
	counter    byte
	failCreate error
	failList   error
}

func newFakeRepo() *fakeRepo { return &fakeRepo{eps: map[string]storedEpisode{}} }

func orgBytes(orgPublicID string) [16]byte {
	h := sha256.Sum256([]byte(orgPublicID))
	var b [16]byte
	copy(b[:], h[:16])
	return b
}

func (f *fakeRepo) CreateEpisode(_ context.Context, orgPublicID string, in NewEpisode) (EpisodeRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate != nil {
		return EpisodeRow{}, f.failCreate
	}
	f.counter++
	var pid [16]byte
	pid[15] = f.counter
	row := EpisodeRow{
		OrgPublicID:    orgBytes(orgPublicID),
		PublicID:       pid,
		Title:          in.Title,
		SourceFilename: in.SourceFilename,
		Language:       in.Language,
		Status:         "uploaded",
		SizeBytes:      in.SizeBytes,
		CreatedAt:      time.Unix(1_700_000_000, 0),
	}
	f.eps[ids.Encode(ids.Episode, pid)] = storedEpisode{row: row, owner: orgPublicID}
	return row, nil
}

func (f *fakeRepo) GetEpisode(_ context.Context, orgPublicID, episodePublicID string) (EpisodeRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.eps[episodePublicID]
	if !ok || s.owner != orgPublicID {
		return EpisodeRow{}, false, nil
	}
	return s.row, true, nil
}

func (f *fakeRepo) SetEpisodeMasterKey(_ context.Context, orgPublicID, episodePublicID, key string) (EpisodeRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.eps[episodePublicID]
	if !ok || s.owner != orgPublicID {
		return EpisodeRow{}, false, nil
	}
	s.row.MasterKey = key
	f.eps[episodePublicID] = s
	return s.row, true, nil
}

// ListEpisodes returns only the rows owned by orgPublicID, newest-first by
// counter (the fake's insertion order). It mirrors the store's org scoping.
func (f *fakeRepo) ListEpisodes(_ context.Context, orgPublicID string) ([]EpisodeRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	var out []EpisodeRow
	for _, s := range f.eps {
		if s.owner == orgPublicID {
			out = append(out, s.row)
		}
	}
	// Deterministic newest-first: higher counter (last byte) first.
	sort.Slice(out, func(i, j int) bool { return out[i].PublicID[15] > out[j].PublicID[15] })
	return out, nil
}

// RetryEpisode compare-and-sets a failed row back to uploaded, org-scoped.
func (f *fakeRepo) RetryEpisode(_ context.Context, orgPublicID, episodePublicID string) (EpisodeRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.eps[episodePublicID]
	if !ok || s.owner != orgPublicID || s.row.Status != "failed" {
		return EpisodeRow{}, false, nil
	}
	s.row.Status = "uploaded"
	f.eps[episodePublicID] = s
	return s.row, true, nil
}

// setStatus is a test helper to move a stored row into a given status/state so
// the proxy and retry guards can be exercised without a worker.
func (f *fakeRepo) setStatus(epID, status, proxyKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.eps[epID]
	s.row.Status = status
	if proxyKey != "" {
		s.row.ProxyKey = proxyKey
	}
	f.eps[epID] = s
}

// errTrigger is the failure a fakeTrigger returns when told to fail.
var errTrigger = errors.New("trigger unavailable")

// fakeTrigger records the (episode, stage) pairs upload-complete launches, and
// can be told to fail so the best-effort behaviour is observable.
type fakeTrigger struct {
	mu    sync.Mutex
	calls [][2]string
	err   error
}

func (f *fakeTrigger) Trigger(_ context.Context, episodePublicID, stage string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{episodePublicID, stage})
	return f.err
}

func (f *fakeTrigger) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// --- harness ----------------------------------------------------------------

const (
	orgA = "0192f0aa-1111-7abc-8def-00000000000a"
	orgB = "0192f0aa-2222-7abc-8def-00000000000b"
)

func principalOf(org string) auth.Principal {
	return auth.Principal{Email: "e@x", OrgPublicID: org, Role: "editor"}
}

func newEpisodeRouter(t *testing.T, repo EpisodeRepo) (http.Handler, *blob.Local, *httptest.Server) {
	t.Helper()
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	blobSrv := httptest.NewServer(local.Handler())
	t.Cleanup(blobSrv.Close)

	router := NewRouter(Deps{
		Authenticator: stubAuth{},
		Directory:     stubDir{},
		Codec:         auth.NewCodec("test-secret"),
		Logger:        discard(),
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0) },
		Episodes:      repo,
		Blob:          local,
	})
	return router, local, blobSrv
}

// newEpisodeRouterWithTrigger is newEpisodeRouter plus a stage trigger wired
// into Deps, for exercising the upload-complete launch path.
func newEpisodeRouterWithTrigger(t *testing.T, repo EpisodeRepo, tr StageTrigger) (http.Handler, *httptest.Server) {
	t.Helper()
	local, err := blob.NewLocal(t.TempDir(), []byte("test-secret"), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	blobSrv := httptest.NewServer(local.Handler())
	t.Cleanup(blobSrv.Close)
	router := NewRouter(Deps{
		Authenticator: stubAuth{},
		Directory:     stubDir{},
		Codec:         auth.NewCodec("test-secret"),
		Logger:        discard(),
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0) },
		Episodes:      repo,
		Blob:          local,
		Trigger:       tr,
	})
	return router, blobSrv
}

// completeUpload runs the create -> PUT bytes -> upload-complete flow for orgA
// and returns the created episode id and the upload-complete recorder.
func completeUpload(t *testing.T, router http.Handler, blobSrv *httptest.Server) (string, *httptest.ResponseRecorder) {
	t.Helper()
	body := []byte("master-bytes")
	reqBody := `{"title":"Ep","source_filename":"a.mp4","size_bytes":` + itoa(len(body)) + `,"content_type":"video/mp4"}`
	rec := doAs(router, orgA, createReq(reqBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var created createEpisodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	putReq, _ := http.NewRequest(created.Upload.Method, blobSrv.URL+created.Upload.URL, strings.NewReader(string(body)))
	for k, v := range created.Upload.Headers {
		putReq.Header.Set(k, v)
	}
	putResp, err := blobSrv.Client().Do(putReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = putResp.Body.Close()
	crec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+created.Episode.ID+"/upload-complete", nil))
	return created.Episode.ID, crec
}

func TestUploadCompleteFiresTrigger(t *testing.T) {
	repo := newFakeRepo()
	tr := &fakeTrigger{}
	router, blobSrv := newEpisodeRouterWithTrigger(t, repo, tr)

	epID, crec := completeUpload(t, router, blobSrv)
	if crec.Code != http.StatusOK {
		t.Fatalf("upload-complete status = %d (body %s)", crec.Code, crec.Body.String())
	}
	if tr.count() != 1 {
		t.Fatalf("trigger fired %d times, want 1", tr.count())
	}
	if got := tr.calls[0]; got[0] != epID || got[1] != "ingest" {
		t.Errorf("trigger called with %v, want [%s ingest]", got, epID)
	}
}

func TestUploadCompleteSucceedsWhenTriggerFails(t *testing.T) {
	repo := newFakeRepo()
	// A trigger failure must not fail the upload: the master is recorded and the
	// worker can be re-driven.
	tr := &fakeTrigger{err: errTrigger}
	router, blobSrv := newEpisodeRouterWithTrigger(t, repo, tr)

	_, crec := completeUpload(t, router, blobSrv)
	if crec.Code != http.StatusOK {
		t.Fatalf("upload-complete status = %d, want 200 despite trigger failure", crec.Code)
	}
	if tr.count() != 1 {
		t.Errorf("trigger fired %d times, want 1", tr.count())
	}
}

// doAs issues req with the given principal in context (simulating AuthGate).
func doAs(h http.Handler, org string, req *http.Request) *httptest.ResponseRecorder {
	req = req.WithContext(auth.NewContext(req.Context(), principalOf(org)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createReq(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/episodes", strings.NewReader(body))
}

// --- tests ------------------------------------------------------------------

func TestCreateUploadCompleteRoundTrip(t *testing.T) {
	repo := newFakeRepo()
	router, _, blobSrv := newEpisodeRouter(t, repo)

	body := []byte("the-master-bytes-payload")
	reqBody := `{"title":"Ep One","source_filename":"Interview Final.mp4","size_bytes":` +
		itoa(len(body)) + `,"content_type":"video/mp4"}`

	rec := doAs(router, orgA, createReq(reqBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var created createEpisodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(created.Episode.ID, "ep_") {
		t.Errorf("episode id = %q, want ep_ prefix", created.Episode.ID)
	}
	if created.Episode.Status != "uploaded" {
		t.Errorf("status = %q, want uploaded", created.Episode.Status)
	}
	if created.Upload.Method != http.MethodPut || created.Upload.URL == "" {
		t.Errorf("upload = %+v, want PUT + url", created.Upload)
	}

	// PUT the bytes to the signed local URL.
	putReq, _ := http.NewRequest(created.Upload.Method, blobSrv.URL+created.Upload.URL, strings.NewReader(string(body)))
	for k, v := range created.Upload.Headers {
		putReq.Header.Set(k, v)
	}
	putResp, err := blobSrv.Client().Do(putReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", putResp.StatusCode)
	}

	// upload-complete.
	crec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+created.Episode.ID+"/upload-complete", nil))
	if crec.Code != http.StatusOK {
		t.Fatalf("upload-complete status = %d, want 200 (body %s)", crec.Code, crec.Body.String())
	}

	// The row now carries the master key with the org_/ep_/masters/ layout.
	stored := repo.eps[created.Episode.ID]
	wantKey := ids.Encode(ids.Org, orgBytes(orgA)) + "/" + created.Episode.ID + "/masters/Interview_Final.mp4"
	if stored.row.MasterKey != wantKey {
		t.Fatalf("master key = %q, want %q", stored.row.MasterKey, wantKey)
	}
	if !strings.HasPrefix(stored.row.MasterKey, "org_") {
		t.Errorf("master key not org-prefixed: %q", stored.row.MasterKey)
	}
}

func TestCreateUnauthenticated401(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	// No principal in context.
	req := createReq(`{"title":"x","source_filename":"a.mp4","size_bytes":1,"content_type":"video/mp4"}`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCrossOrgUploadCompleteIsolated(t *testing.T) {
	repo := newFakeRepo()
	router, _, blobSrv := newEpisodeRouter(t, repo)

	body := []byte("bytes")
	reqBody := `{"title":"A secret","source_filename":"a.mp4","size_bytes":` + itoa(len(body)) + `,"content_type":"video/mp4"}`
	rec := doAs(router, orgA, createReq(reqBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}
	var created createEpisodeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Even after org A uploads the bytes, org B cannot complete (or observe) it.
	putReq, _ := http.NewRequest(http.MethodPut, blobSrv.URL+created.Upload.URL, strings.NewReader(string(body)))
	putResp, _ := blobSrv.Client().Do(putReq)
	_ = putResp.Body.Close()

	brec := doAs(router, orgB, httptest.NewRequest(http.MethodPost, "/api/episodes/"+created.Episode.ID+"/upload-complete", nil))
	if brec.Code != http.StatusNotFound {
		t.Fatalf("cross-org upload-complete status = %d, want 404", brec.Code)
	}
	// The row must remain unmodified (no master key recorded by B).
	if repo.eps[created.Episode.ID].row.MasterKey != "" {
		t.Errorf("org B recorded a master key on org A's episode")
	}
}

func TestUploadCompleteMissingObject409(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	rec := doAs(router, orgA, createReq(`{"title":"x","source_filename":"a.mp4","size_bytes":10,"content_type":"video/mp4"}`))
	var created createEpisodeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// No bytes uploaded -> object missing -> 409.
	crec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+created.Episode.ID+"/upload-complete", nil))
	if crec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", crec.Code)
	}
}

func TestUploadCompleteShortObject409(t *testing.T) {
	repo := newFakeRepo()
	router, _, blobSrv := newEpisodeRouter(t, repo)
	// Declare 100 bytes but upload fewer.
	rec := doAs(router, orgA, createReq(`{"title":"x","source_filename":"a.mp4","size_bytes":100,"content_type":"video/mp4"}`))
	var created createEpisodeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	putReq, _ := http.NewRequest(http.MethodPut, blobSrv.URL+created.Upload.URL, strings.NewReader("short"))
	putResp, _ := blobSrv.Client().Do(putReq)
	_ = putResp.Body.Close()

	crec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+created.Episode.ID+"/upload-complete", nil))
	if crec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (short upload)", crec.Code)
	}
}

func TestUploadCompleteUnknownEpisode404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	for _, id := range []string{"ep_" + strings.Repeat("0", 26), "not-an-id"} {
		rec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+id+"/upload-complete", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("id %q status = %d, want 404", id, rec.Code)
		}
	}
}

func TestCreateValidation(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty title", `{"title":"  ","source_filename":"a.mp4","size_bytes":1,"content_type":"video/mp4"}`, http.StatusBadRequest},
		{"empty filename", `{"title":"x","source_filename":"","size_bytes":1,"content_type":"video/mp4"}`, http.StatusBadRequest},
		{"zero size", `{"title":"x","source_filename":"a.mp4","size_bytes":0,"content_type":"video/mp4"}`, http.StatusBadRequest},
		{"oversized", `{"title":"x","source_filename":"a.mp4","size_bytes":44000000000000,"content_type":"video/mp4"}`, http.StatusBadRequest},
		{"bad content type", `{"title":"x","source_filename":"a.mp4","size_bytes":1,"content_type":"application/zip"}`, http.StatusBadRequest},
		{"unusable filename", `{"title":"x","source_filename":"...","size_bytes":1,"content_type":"video/mp4"}`, http.StatusBadRequest},
		{"not json", `nope`, http.StatusBadRequest},
		{"mov ok", `{"title":"x","source_filename":"a.mov","size_bytes":1,"content_type":"video/quicktime"}`, http.StatusCreated},
		{"mxf ok", `{"title":"x","source_filename":"a.mxf","size_bytes":1,"content_type":"application/mxf"}`, http.StatusCreated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doAs(router, orgA, createReq(c.body))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestCreateDTONeutral asserts the create response exposes no internal id and no
// storage key — the master key stays server-side.
func TestCreateDTONeutral(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	rec := doAs(router, orgA, createReq(`{"title":"x","source_filename":"a.mp4","size_bytes":1,"content_type":"video/mp4"}`))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ep := raw["episode"].(map[string]any)
	for _, bad := range []string{"org_id", "show_id", "master_object_key", "master_key"} {
		if _, present := ep[bad]; present {
			t.Errorf("episode DTO exposes %q", bad)
		}
	}
	if _, present := raw["master_key"]; present {
		t.Error("response exposes master_key at top level")
	}
}

// --- list / proxy / retry ---------------------------------------------------

// seedEpisode creates an episode for org and returns its public id, without
// uploading bytes (status stays 'uploaded').
func seedEpisode(t *testing.T, router http.Handler, org, title, filename string) string {
	t.Helper()
	body := `{"title":"` + title + `","source_filename":"` + filename + `","size_bytes":10,"content_type":"video/mp4"}`
	rec := doAs(router, org, createReq(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed create status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var created createEpisodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.Episode.ID
}

func TestListEpisodesOrgScoped(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)

	aID := seedEpisode(t, router, orgA, "A one", "a.mp4")
	_ = seedEpisode(t, router, orgB, "B secret", "b.mp4")

	rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp listEpisodesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Episodes) != 1 {
		t.Fatalf("org A sees %d episodes, want 1 (leak?)", len(resp.Episodes))
	}
	if resp.Episodes[0].ID != aID {
		t.Errorf("listed id = %q, want %q", resp.Episodes[0].ID, aID)
	}
	if resp.Episodes[0].Status != "uploaded" {
		t.Errorf("status = %q, want uploaded", resp.Episodes[0].Status)
	}
}

// TestListEpisodesDTONeutral asserts the list projection exposes no internal id
// or storage key, and omits unknown duration (still 'uploaded').
func TestListEpisodesDTONeutral(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	_ = seedEpisode(t, router, orgA, "A", "a.mp4")

	rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes", nil))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list := raw["episodes"].([]any)
	ep := list[0].(map[string]any)
	for _, bad := range []string{"org_id", "show_id", "master_object_key", "master_key", "proxy_object_key", "proxy_key"} {
		if _, present := ep[bad]; present {
			t.Errorf("list DTO exposes %q", bad)
		}
	}
	if _, present := ep["duration_ms"]; present {
		t.Error("duration_ms present for a not-yet-measured episode; want omitted")
	}
	if _, present := ep["uploaded_at"]; !present {
		t.Error("uploaded_at missing from list DTO")
	}
}

func TestProxy404BeforeReady(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	id := seedEpisode(t, router, orgA, "A", "a.mp4")

	for _, st := range []string{"uploaded", "processing", "failed"} {
		repo.setStatus(id, st, "")
		rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes/"+id+"/proxy", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("proxy in state %q status = %d, want 404", st, rec.Code)
		}
	}
}

func TestProxyReadyReturnsSignedURL(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	id := seedEpisode(t, router, orgA, "A", "a.mp4")
	repo.setStatus(id, "ready", "org_x/ep_y/proxies/proxy.mp4")

	rec := doAs(router, orgA, httptest.NewRequest(http.MethodGet, "/api/episodes/"+id+"/proxy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp proxyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.URL == "" || resp.ExpiresAt == "" {
		t.Errorf("proxy response = %+v, want url + expires_at", resp)
	}
}

func TestProxyCrossOrgIsolated(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	id := seedEpisode(t, router, orgA, "A", "a.mp4")
	repo.setStatus(id, "ready", "org_x/ep_y/proxies/proxy.mp4")

	rec := doAs(router, orgB, httptest.NewRequest(http.MethodGet, "/api/episodes/"+id+"/proxy", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org proxy status = %d, want 404", rec.Code)
	}
}

func TestRetryOnlyFromFailed(t *testing.T) {
	repo := newFakeRepo()
	tr := &fakeTrigger{}
	router, _ := newEpisodeRouterWithTrigger(t, repo, tr)
	id := seedEpisode(t, router, orgA, "A", "a.mp4")

	// Not failed yet -> 409, no trigger.
	for _, st := range []string{"uploaded", "processing", "ready"} {
		repo.setStatus(id, st, "")
		rec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+id+"/retry", nil))
		if rec.Code != http.StatusConflict {
			t.Errorf("retry from %q status = %d, want 409", st, rec.Code)
		}
	}
	if tr.count() != 0 {
		t.Fatalf("trigger fired %d times before a valid retry, want 0", tr.count())
	}

	// Failed -> 200, resets to uploaded, fires ingest.
	repo.setStatus(id, "failed", "")
	rec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/"+id+"/retry", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry from failed status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var dto episodeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Status != "uploaded" {
		t.Errorf("post-retry status = %q, want uploaded", dto.Status)
	}
	if tr.count() != 1 {
		t.Fatalf("trigger fired %d times, want 1", tr.count())
	}
	if got := tr.calls[0]; got[0] != id || got[1] != "ingest" {
		t.Errorf("trigger called with %v, want [%s ingest]", got, id)
	}
}

func TestRetryUnknownEpisode404(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	rec := doAs(router, orgA, httptest.NewRequest(http.MethodPost, "/api/episodes/ep_"+strings.Repeat("0", 26)+"/retry", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("retry unknown status = %d, want 404", rec.Code)
	}
}

func TestRetryCrossOrgIsolated(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	id := seedEpisode(t, router, orgA, "A", "a.mp4")
	repo.setStatus(id, "failed", "")

	rec := doAs(router, orgB, httptest.NewRequest(http.MethodPost, "/api/episodes/"+id+"/retry", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org retry status = %d, want 404", rec.Code)
	}
	// Org A's episode stays failed.
	if repo.eps[id].row.Status != "failed" {
		t.Errorf("org B changed org A's episode status to %q", repo.eps[id].row.Status)
	}
}

func TestListUnauthenticated401(t *testing.T) {
	repo := newFakeRepo()
	router, _, _ := newEpisodeRouter(t, repo)
	req := httptest.NewRequest(http.MethodGet, "/api/episodes", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
