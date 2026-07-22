package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"encoding/json"

	"blueshift/internal/auth"
	"blueshift/internal/blob"
)

// maxLoginBody bounds the login request body.
const maxLoginBody = 1 << 16

// Deps are the collaborators the auth handlers need.
type Deps struct {
	// Authenticator verifies credentials (dev or identity mode).
	Authenticator auth.Authenticator
	// Directory resolves the current principal's user + org for /me.
	Directory auth.Directory
	// Codec mints session cookies.
	Codec *auth.Codec
	// Cookie sets/clears the session cookie.
	Cookie auth.CookieConfig
	// Logger records raw causes of "unavailable" failures server-side.
	Logger *slog.Logger
	// Now supplies the clock (session expiry, rate limiter). Nil uses
	// time.Now.
	Now func() time.Time
	// RatePerMin caps login attempts per client IP per minute. <=0 uses 5.
	RatePerMin int
	// Episodes is the org-scoped episode persistence port. When it and Blob are
	// both set, the episode routes are registered.
	Episodes EpisodeRepo
	// Blob mints upload URLs and stats uploaded objects.
	Blob blob.Store
}

// handler holds resolved dependencies for the auth endpoints.
type handler struct {
	deps    Deps
	limiter *rateLimiter
}

// NewRouter builds the /api mux with the auth routes registered. The returned
// handler expects to be mounted behind the server's deny-by-default gate, which
// lets POST /api/auth/login through unauthenticated and injects the principal
// for the rest.
func NewRouter(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.RatePerMin <= 0 {
		d.RatePerMin = 5
	}
	h := &handler{
		deps:    d,
		limiter: newRateLimiter(float64(d.RatePerMin), time.Minute, d.Now),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+auth.LoginPath, h.login)
	mux.HandleFunc("POST "+auth.LogoutPath, h.logout)
	mux.HandleFunc("GET "+auth.MePath, h.me)
	if d.Episodes != nil && d.Blob != nil {
		mux.HandleFunc("POST /api/episodes", h.createEpisode)
		mux.HandleFunc("POST /api/episodes/{id}/upload-complete", h.uploadComplete)
	}
	return mux
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userDTO struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type orgDTO struct {
	Name string `json:"name"`
}

// meResponse is the /me and login-success shape: identity by email + names and
// role, no internal ids.
type meResponse struct {
	User userDTO `json:"user"`
	Org  orgDTO  `json:"org"`
	Role string  `json:"role"`
}

func meFrom(ac auth.AuthContext) meResponse {
	return meResponse{
		User: userDTO{Email: ac.Email, Name: ac.DisplayName},
		Org:  orgDTO{Name: ac.OrgName},
		Role: ac.Role,
	}
}

// login authenticates {email,password} and, on success, sets the session
// cookie and returns the identity. Public route (the gate lets it through).
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, errBody{Error: "rate_limited"})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLoginBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errBody{Error: "invalid_request"})
		return
	}

	ac, err := h.deps.Authenticator.Authenticate(r.Context(), email, req.Password)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}

	if err := h.setSession(w, ac); err != nil {
		id := errorID()
		h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, "session mint failed",
			slog.String("error_id", id), slog.String("error", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, errIDBody{Error: "auth_unavailable", ErrorID: id})
		return
	}
	writeJSON(w, http.StatusOK, meFrom(ac))
}

// logout clears the session cookie. Reached only with a valid session (the gate
// requires one for every /api route except login).
func (h *handler) logout(w http.ResponseWriter, _ *http.Request) {
	h.deps.Cookie.Clear(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// me returns the current principal's user, org, and role.
func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
		return
	}
	ac, err := h.deps.Directory.LookupByEmail(r.Context(), p.Email)
	if err != nil {
		if errors.Is(err, auth.ErrUnknownUser) {
			writeJSON(w, http.StatusUnauthorized, errBody{Error: "unauthorized"})
			return
		}
		id := errorID()
		h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, "me lookup failed",
			slog.String("error_id", id), slog.String("error", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, errIDBody{Error: "auth_unavailable", ErrorID: id})
		return
	}
	writeJSON(w, http.StatusOK, meFrom(ac))
}

func (h *handler) setSession(w http.ResponseWriter, ac auth.AuthContext) error {
	token, err := h.deps.Codec.Mint(auth.Session{
		Email:       ac.Email,
		OrgPublicID: ac.OrgPublicID,
		Role:        ac.Role,
		ExpiresAt:   h.deps.Now().Add(auth.SessionTTL),
	})
	if err != nil {
		return err
	}
	h.deps.Cookie.Set(w, token)
	return nil
}

// writeAuthError maps an auth sentinel to its neutral HTTP response. Credential
// problems are an opaque 401; backend problems are a 503 with a correlation id
// and the raw cause logged server-side only.
func (h *handler) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrAuthFailed), errors.Is(err, auth.ErrUnknownUser):
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "auth_failed"})
	default: // ErrAuthUnavailable and anything unexpected
		id := errorID()
		h.deps.Logger.LogAttrs(r.Context(), slog.LevelError, "auth backend unavailable",
			slog.String("error_id", id), slog.String("error", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, errIDBody{Error: "auth_unavailable", ErrorID: id})
	}
}
