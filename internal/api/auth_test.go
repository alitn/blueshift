package api

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

	"blueshift/internal/auth"
)

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

var testCtx = auth.AuthContext{
	Email:       "dev-approver@blueshift.local",
	DisplayName: "Dev Approver",
	OrgPublicID: "0192f0aa-1111-7abc-8def-000000000001",
	OrgName:     "Blueshift Pilot",
	Role:        "approver",
}

// stubAuth returns a fixed context or error.
type stubAuth struct {
	ac  auth.AuthContext
	err error
}

func (s stubAuth) Authenticate(context.Context, string, string) (auth.AuthContext, error) {
	return s.ac, s.err
}

type stubDir struct {
	ac  auth.AuthContext
	err error
}

func (s stubDir) LookupByEmail(context.Context, string) (auth.AuthContext, error) {
	return s.ac, s.err
}

func newRouter(t *testing.T, d Deps) http.Handler {
	t.Helper()
	if d.Codec == nil {
		d.Codec = auth.NewCodec("test-secret")
	}
	if d.Logger == nil {
		d.Logger = discard()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	}
	return NewRouter(d)
}

func postLogin(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, auth.LoginPath, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginDevSuccessSetsCookie(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{ac: testCtx}, Directory: stubDir{ac: testCtx}})
	rec := postLogin(t, h, `{"email":"dev-approver@blueshift.local","password":"blueshift-dev"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.CookieName || cookies[0].Value == "" {
		t.Fatalf("expected session cookie set, got %v", cookies)
	}

	var got meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.User.Email != "dev-approver@blueshift.local" || got.User.Name != "Dev Approver" || got.Org.Name != "Blueshift Pilot" || got.Role != "approver" {
		t.Errorf("me shape = %+v", got)
	}
}

func TestLoginWrongPasswordNeutral401(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{err: auth.ErrAuthFailed}, Directory: stubDir{}})
	rec := postLogin(t, h, `{"email":"dev-approver@blueshift.local","password":"nope"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// Exact neutral body — proves nothing about the cause leaks.
	if body := rec.Body.String(); body != `{"error":"auth_failed"}`+"\n" {
		t.Errorf("body = %q, want neutral auth_failed", body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("no cookie should be set on failure")
	}
}

func TestLoginUnknownUserAlso401(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{err: auth.ErrUnknownUser}, Directory: stubDir{}})
	rec := postLogin(t, h, `{"email":"ghost@x","password":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no user enumeration)", rec.Code)
	}
	if body := rec.Body.String(); body != `{"error":"auth_failed"}`+"\n" {
		t.Errorf("body = %q, want auth_failed", body)
	}
}

func TestLoginBackendUnavailable503WithID(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{err: auth.ErrAuthUnavailable}, Directory: stubDir{}})
	rec := postLogin(t, h, `{"email":"dev-approver@blueshift.local","password":"x"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var got errIDBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error != "auth_unavailable" || got.ErrorID == "" {
		t.Errorf("body = %+v, want auth_unavailable + error_id", got)
	}
}

func TestLoginInvalidBody(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{ac: testCtx}, Directory: stubDir{}})
	for _, body := range []string{`not json`, `{"email":"","password":"x"}`, `{"email":"a@b","password":""}`} {
		rec := postLogin(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q -> status %d, want 400", body, rec.Code)
		}
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{err: auth.ErrAuthFailed}, Directory: stubDir{}, RatePerMin: 5})
	var last int
	for i := 0; i < 6; i++ {
		last = postLogin(t, h, `{"email":"a@b","password":"x"}`).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status = %d, want 429", last)
	}
}

func TestMeReturnsPrincipal(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{}, Directory: stubDir{ac: testCtx}})
	req := httptest.NewRequest(http.MethodGet, auth.MePath, nil)
	req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{
		Email: "dev-approver@blueshift.local", OrgPublicID: testCtx.OrgPublicID, Role: "approver",
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Assert the raw JSON exposes no internal id fields.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, bad := raw["id"]; bad {
		t.Error("me response exposes an id field")
	}
	user := raw["user"].(map[string]any)
	if _, bad := user["id"]; bad {
		t.Error("me user exposes an id field")
	}
	if user["email"] != "dev-approver@blueshift.local" || raw["role"] != "approver" {
		t.Errorf("me = %v", raw)
	}
}

func TestMeWithoutPrincipal401(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{}, Directory: stubDir{ac: testCtx}})
	req := httptest.NewRequest(http.MethodGet, auth.MePath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	h := newRouter(t, Deps{Authenticator: stubAuth{}, Directory: stubDir{}})
	req := httptest.NewRequest(http.MethodPost, auth.LogoutPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != auth.CookieName || c.MaxAge != -1 {
		t.Errorf("logout cookie = %+v, want cleared", c)
	}
}

// TestLoginIdentityModeNeutral drives the real identity authenticator against a
// fake local provider and asserts the client responses are byte-for-byte the
// neutral envelopes — so no provider detail can reach the browser. Provider-
// name absence is additionally asserted in the auth package's own tests.
func TestLoginIdentityModeNeutral(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
		wantBody string
	}{
		{"success", http.StatusOK, `{"localId":"abc","email":"dev-approver@blueshift.local"}`, http.StatusOK, ""},
		{"rejected", http.StatusBadRequest, `{"error":{"message":"INVALID_PASSWORD"}}`, http.StatusUnauthorized, `{"error":"auth_failed"}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer provider.Close()

			h := newRouter(t, Deps{
				Authenticator: auth.IdentityAuthenticator{
					APIKey:   "k",
					Endpoint: provider.URL,
					Client:   provider.Client(),
					Dir:      stubDir{ac: testCtx},
				},
				Directory: stubDir{ac: testCtx},
			})
			rec := postLogin(t, h, `{"email":"dev-approver@blueshift.local","password":"pw"}`)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}
