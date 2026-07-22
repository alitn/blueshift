package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
