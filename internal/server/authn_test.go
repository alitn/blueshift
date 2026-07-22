package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blueshift/internal/auth"
)

// echoPrincipal writes the resolved principal's role, or "none".
func echoPrincipal() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.FromContext(r.Context())
		if !ok {
			_, _ = w.Write([]byte("none"))
			return
		}
		_, _ = w.Write([]byte(p.Role))
	})
}

func validCookie(t *testing.T, codec *auth.Codec) *http.Cookie {
	t.Helper()
	tok, err := codec.Mint(auth.Session{
		Email: "dev-approver@blueshift.local", OrgPublicID: "org-uuid", Role: "approver",
		ExpiresAt: time.Now().Add(auth.SessionTTL),
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return &http.Cookie{Name: auth.CookieName, Value: tok}
}

func TestAuthGateDenyByDefault(t *testing.T) {
	codec := auth.NewCodec("k")
	gate := AuthGate(codec, discardLogger(), echoPrincipal())

	// No cookie on a protected /api path -> 401.
	req := httptest.NewRequest(http.MethodGet, auth.MePath, nil)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie -> %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); body != `{"error":"unauthorized"}`+"\n" {
		t.Errorf("body = %q, want neutral unauthorized", body)
	}
}

func TestAuthGateLoginIsPublic(t *testing.T) {
	codec := auth.NewCodec("k")
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	gate := AuthGate(codec, discardLogger(), next)

	req := httptest.NewRequest(http.MethodPost, auth.LoginPath, nil)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if !reached {
		t.Fatal("POST login did not pass through the gate unauthenticated")
	}
}

func TestAuthGateInjectsPrincipal(t *testing.T) {
	codec := auth.NewCodec("k")
	gate := AuthGate(codec, discardLogger(), echoPrincipal())

	req := httptest.NewRequest(http.MethodGet, auth.MePath, nil)
	req.AddCookie(validCookie(t, codec))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid cookie -> %d, want 200", rec.Code)
	}
	if rec.Body.String() != "approver" {
		t.Errorf("principal role = %q, want approver", rec.Body.String())
	}
}

func TestAuthGateRejectsTamperedCookie(t *testing.T) {
	codec := auth.NewCodec("k")
	gate := AuthGate(codec, discardLogger(), echoPrincipal())

	req := httptest.NewRequest(http.MethodGet, auth.MePath, nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "garbage.token"})
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered cookie -> %d, want 401", rec.Code)
	}
}

func TestRequireRole(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	t.Run("matching role passes", func(t *testing.T) {
		h := RequireRole("approver", ok)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Role: "approver"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("wrong role forbidden", func(t *testing.T) {
		h := RequireRole("approver", ok)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(auth.NewContext(req.Context(), auth.Principal{Role: "editor"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("no principal unauthorized", func(t *testing.T) {
		h := RequireRole("approver", ok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}
