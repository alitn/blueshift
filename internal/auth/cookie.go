package auth

import (
	"net/http"
	"time"
)

// CookieConfig captures the environment-dependent bits of the session cookie.
// Everything else (name, path, HttpOnly, SameSite, max-age) is fixed policy.
type CookieConfig struct {
	// Secure sets the cookie's Secure flag. False only in local dev (plain
	// HTTP); true in staging/prod.
	Secure bool
}

// Set writes the session cookie carrying token. HttpOnly (no JS access),
// SameSite=Lax, Path=/, seven-day max-age.
func (c CookieConfig) Set(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL / time.Second),
	})
}

// Clear expires the session cookie (logout).
func (c CookieConfig) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
