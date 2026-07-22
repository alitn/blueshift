package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultIdentityEndpoint is the identity provider's REST base. Provider
// hostnames may appear ONLY in this package (not scanned by the vendor gate);
// they never reach internal/api, web, or any client-visible response.
const defaultIdentityEndpoint = "https://identitytoolkit.googleapis.com/v1"

// maxProviderBody bounds how much of a provider response we read (for logs).
const maxProviderBody = 1 << 16

// IdentityAuthenticator verifies credentials against the identity provider's
// password sign-in REST endpoint, server-side, then attaches the local
// membership. The browser never talks to the provider: it only ever sees our
// neutral endpoints. Raw provider errors are wrapped into ErrAuthUnavailable
// for server-side logging and never surface to the client.
type IdentityAuthenticator struct {
	// APIKey is the provider web API key (IDP_API_KEY), injected from Secret
	// Manager via env at deploy.
	APIKey string
	// Endpoint overrides the provider base URL (tests point this at a fake
	// local server). Empty uses defaultIdentityEndpoint.
	Endpoint string
	// Client is the HTTP client used for the server-side call. Nil uses a
	// client with a short timeout.
	Client *http.Client
	// Dir resolves the local user + membership after a successful sign-in.
	Dir Directory
}

// signInResponse is the subset of the provider payload we care about. We do not
// mint or forward provider tokens in M0 — success of the call is the signal.
type signInResponse struct {
	LocalID string `json:"localId"`
	Email   string `json:"email"`
}

// Authenticate calls the provider, then resolves the local membership. A
// provider credential rejection is ErrAuthFailed; an unreachable or erroring
// provider is ErrAuthUnavailable (raw cause wrapped for logs only).
func (a IdentityAuthenticator) Authenticate(ctx context.Context, email, password string) (AuthContext, error) {
	if err := a.verify(ctx, email, password); err != nil {
		return AuthContext{}, err
	}
	// Credentials are good; the user must still be provisioned locally.
	return a.Dir.LookupByEmail(ctx, email)
}

func (a IdentityAuthenticator) verify(ctx context.Context, email, password string) error {
	base := a.Endpoint
	if base == "" {
		base = defaultIdentityEndpoint
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	body, err := json.Marshal(map[string]any{
		"email":             email,
		"password":          password,
		"returnSecureToken": true,
	})
	if err != nil {
		return fmt.Errorf("%w: marshal request: %v", ErrAuthUnavailable, err)
	}

	endpoint := base + "/accounts:signInWithPassword?key=" + url.QueryEscape(a.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// NewRequest errors can embed the endpoint URL (and thus the key).
		return fmt.Errorf("%w: build request: %s", ErrAuthUnavailable, a.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// A transport failure returns a *url.Error whose message embeds the
		// full request URL, including ?key=<IDP_API_KEY> (Go's stripPassword
		// only redacts userinfo, not query params). Keep only the underlying
		// cause and redact the key defensively so it never reaches the logs.
		return fmt.Errorf("%w: %s", ErrAuthUnavailable, a.redact(transportCause(err).Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))

	switch resp.StatusCode {
	case http.StatusOK:
		var parsed signInResponse
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed.LocalID == "" {
			// A 200 we can't parse is a contract break, not a user error.
			return fmt.Errorf("%w: unexpected provider response", ErrAuthUnavailable)
		}
		return nil
	case http.StatusBadRequest:
		// The provider rejects bad email/password with 400. Neutralize: the
		// raw message (which names the provider) stays out of the returned
		// error entirely.
		return ErrAuthFailed
	default:
		// 401/403/5xx: misconfig or outage. Wrap the raw body for server logs.
		return fmt.Errorf("%w: provider status %d: %s", ErrAuthUnavailable, resp.StatusCode, snippet(raw))
	}
}

// redact removes the API key from a string before it can reach the logs. It is
// a no-op when the key is empty (so it never replaces every empty substring).
func (a IdentityAuthenticator) redact(s string) string {
	if a.APIKey == "" {
		return s
	}
	out := strings.ReplaceAll(s, a.APIKey, "REDACTED")
	// Also cover the URL-escaped form in case the key contained escapables.
	if esc := url.QueryEscape(a.APIKey); esc != a.APIKey {
		out = strings.ReplaceAll(out, esc, "REDACTED")
	}
	return out
}

// transportCause unwraps a *url.Error to its underlying cause, dropping the
// request URL (which carries the key) from the message.
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// snippet collapses a response body to a short single-line form for logs.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
