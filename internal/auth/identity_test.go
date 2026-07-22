package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// providerNames are strings that must never appear in an error our api layer
// could render to a client. This test file lives in the auth package, which the
// vendor-leak gate does not scan, so we can name them here to assert absence.
var providerNames = []string{"google", "identitytoolkit", "gemini", "vertex", "firebase"}

// fakeIdentityServer stands in for the provider's password sign-in endpoint.
func fakeIdentityServer(t *testing.T, status int, body string, gotReq *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "signInWithPassword") {
			t.Errorf("unexpected provider path %q", r.URL.Path)
		}
		if r.URL.Query().Get("key") == "" {
			t.Errorf("provider call missing api key")
		}
		if gotReq != nil {
			b, _ := io.ReadAll(r.Body)
			*gotReq = string(b)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func assertNeutral(t *testing.T, err error) {
	t.Helper()
	msg := strings.ToLower(err.Error())
	for _, name := range providerNames {
		if strings.Contains(msg, name) {
			t.Errorf("error message %q leaks provider name %q", msg, name)
		}
	}
}

func TestIdentityAuthenticatorSuccess(t *testing.T) {
	var reqBody string
	srv := fakeIdentityServer(t, http.StatusOK,
		`{"localId":"abc","email":"dev-approver@blueshift.local","idToken":"tok"}`, &reqBody)
	defer srv.Close()

	a := IdentityAuthenticator{
		APIKey:   "test-key",
		Endpoint: srv.URL,
		Client:   srv.Client(),
		Dir:      fakeDir{ac: AuthContext(sampleRow)},
	}
	ac, err := a.Authenticate(context.Background(), "dev-approver@blueshift.local", "pw")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if ac.Role != "approver" {
		t.Errorf("role = %q, want approver", ac.Role)
	}
	// The password is sent to the provider server-side only.
	var sent map[string]any
	if err := json.Unmarshal([]byte(reqBody), &sent); err != nil {
		t.Fatalf("provider request body not JSON: %v", err)
	}
	if sent["email"] != "dev-approver@blueshift.local" || sent["password"] != "pw" {
		t.Errorf("provider request = %v, want email+password", sent)
	}
}

func TestIdentityAuthenticatorBadCredentials(t *testing.T) {
	// A realistic provider 400 that names the provider — must be neutralized.
	srv := fakeIdentityServer(t, http.StatusBadRequest,
		`{"error":{"code":400,"message":"INVALID_PASSWORD","errors":[{"domain":"global"}]},"provider":"identitytoolkit.googleapis.com"}`, nil)
	defer srv.Close()

	a := IdentityAuthenticator{
		APIKey:   "test-key",
		Endpoint: srv.URL,
		Client:   srv.Client(),
		Dir:      fakeDir{ac: AuthContext(sampleRow)},
	}
	_, err := a.Authenticate(context.Background(), "dev-approver@blueshift.local", "wrong")
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	assertNeutral(t, err)
}

func TestIdentityAuthenticatorUnavailable(t *testing.T) {
	srv := fakeIdentityServer(t, http.StatusInternalServerError,
		`{"error":"backend down"}`, nil)
	defer srv.Close()

	a := IdentityAuthenticator{
		APIKey:   "test-key",
		Endpoint: srv.URL,
		Client:   srv.Client(),
		Dir:      fakeDir{ac: AuthContext(sampleRow)},
	}
	if _, err := a.Authenticate(context.Background(), "dev-approver@blueshift.local", "pw"); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("err = %v, want ErrAuthUnavailable", err)
	}
}

func TestIdentityAuthenticatorNetworkError(t *testing.T) {
	srv := fakeIdentityServer(t, http.StatusOK, `{"localId":"x"}`, nil)
	url := srv.URL
	client := srv.Client()
	srv.Close() // now unreachable

	a := IdentityAuthenticator{APIKey: "k", Endpoint: url, Client: client, Dir: fakeDir{ac: AuthContext(sampleRow)}}
	if _, err := a.Authenticate(context.Background(), "dev-approver@blueshift.local", "pw"); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("err = %v, want ErrAuthUnavailable", err)
	}
}

func TestIdentityAuthenticatorNetworkErrorRedactsKey(t *testing.T) {
	// The provider is unreachable, so client.Do returns a *url.Error whose
	// message would embed the request URL (?key=<IDP_API_KEY>). The wrapped
	// ErrAuthUnavailable that reaches the logs must not contain the key.
	const secretKey = "SUPER-SECRET-IDP-KEY-abc123"

	srv := fakeIdentityServer(t, http.StatusOK, `{"localId":"x"}`, nil)
	url := srv.URL
	client := srv.Client()
	srv.Close() // now unreachable → transport error

	a := IdentityAuthenticator{APIKey: secretKey, Endpoint: url, Client: client, Dir: fakeDir{ac: AuthContext(sampleRow)}}
	_, err := a.Authenticate(context.Background(), "dev-approver@blueshift.local", "pw")
	if !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("err = %v, want ErrAuthUnavailable", err)
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("wrapped error leaks the API key: %q", err.Error())
	}
}

func TestIdentityAuthenticatorProvisioning(t *testing.T) {
	// Provider accepts the credentials but the user is not provisioned locally.
	srv := fakeIdentityServer(t, http.StatusOK, `{"localId":"abc","email":"ghost@x"}`, nil)
	defer srv.Close()

	a := IdentityAuthenticator{
		APIKey:   "k",
		Endpoint: srv.URL,
		Client:   srv.Client(),
		Dir:      fakeDir{err: ErrUnknownUser},
	}
	if _, err := a.Authenticate(context.Background(), "ghost@x", "pw"); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("err = %v, want ErrUnknownUser", err)
	}
}
