package server

import (
	"log/slog"
	"net/http"

	"blueshift/internal/auth"
)

// AuthGate is the deny-by-default authn middleware for the /api subtree. It
// wraps the api handler: POST /api/auth/login passes through unauthenticated;
// every other request must carry a valid session cookie or gets a 401 JSON
// (never a redirect — the SPA decides where to send the user). On success the
// resolved Principal is placed in the request context for downstream handlers.
func AuthGate(codec *auth.Codec, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == auth.LoginPath {
			next.ServeHTTP(w, r)
			return
		}

		c, err := r.Cookie(auth.CookieName)
		if err != nil {
			unauthorized(w)
			return
		}
		sess, err := codec.Verify(c.Value)
		if err != nil {
			// Expired/tampered are the normal "please log in again" path; log
			// at debug only, never leak the reason to the client.
			logger.LogAttrs(r.Context(), slog.LevelDebug, "session rejected",
				slog.String("reason", err.Error()))
			unauthorized(w)
			return
		}

		p := auth.Principal{Email: sess.Email, OrgPublicID: sess.OrgPublicID, Role: sess.Role}
		next.ServeHTTP(w, r.WithContext(auth.NewContext(r.Context(), p)))
	})
}

// RequireRole gates a handler on the principal's role. It assumes AuthGate has
// already run (principal in context). Not wired to any route in M0 — the role
// split lands in M2 — but exercised by tests so it is ready to use.
func RequireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.FromContext(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		if p.Role != role {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}
