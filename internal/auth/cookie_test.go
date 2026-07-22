package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCookieSetAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	CookieConfig{Secure: true}.Set(rec, "token-value")

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != CookieName || c.Value != "token-value" {
		t.Errorf("cookie = %s=%s", c.Name, c.Value)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie attrs: HttpOnly=%v Secure=%v SameSite=%v Path=%q", c.HttpOnly, c.Secure, c.SameSite, c.Path)
	}
	if c.MaxAge != int(SessionTTL.Seconds()) {
		t.Errorf("MaxAge = %d, want %d", c.MaxAge, int(SessionTTL.Seconds()))
	}
}

func TestCookieInsecureInDev(t *testing.T) {
	rec := httptest.NewRecorder()
	CookieConfig{Secure: false}.Set(rec, "x")
	if rec.Result().Cookies()[0].Secure {
		t.Error("dev cookie should not be Secure")
	}
}

func TestCookieClear(t *testing.T) {
	rec := httptest.NewRecorder()
	CookieConfig{Secure: true}.Clear(rec)
	c := rec.Result().Cookies()[0]
	if c.MaxAge != -1 || c.Value != "" {
		t.Errorf("clear cookie MaxAge=%d value=%q, want -1 and empty", c.MaxAge, c.Value)
	}
}
