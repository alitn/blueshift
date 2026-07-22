// Package auth owns authentication for the app: stateless signed-cookie
// sessions, the two login backends (offline dev password and the server-side
// identity provider), and the neutral error model that keeps provider details
// out of every client-visible surface.
//
// This package is the ONLY place provider hostnames/names may appear (see
// identity.go). The vendor-leak gate does not scan it; callers upstream
// (internal/api, web) never see provider strings because provider errors are
// classified into the sentinels below before they cross this boundary.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Route paths for the auth endpoints. Defined here so the server's deny-by-
// default gate (which must let the login route through unauthenticated) and the
// api router register exactly the same strings.
const (
	LoginPath  = "/api/auth/login"
	LogoutPath = "/api/auth/logout"
	MePath     = "/api/auth/me"
)

// CookieName is the session cookie name. Neutral, product-scoped.
const CookieName = "bs_session"

// SessionTTL is the session lifetime. No refresh or revocation in M0.
const SessionTTL = 7 * 24 * time.Hour

// Sentinel errors. Everything a login backend can go wrong with collapses into
// one of these before leaving the package, so upstream never branches on (or
// renders) a provider-specific error.
var (
	// ErrAuthFailed means the credentials were rejected: wrong password or a
	// provider credential error. Maps to a neutral 401 "auth_failed".
	ErrAuthFailed = errors.New("auth: credentials rejected")
	// ErrUnknownUser means the credentials may be valid but no seeded user +
	// membership exists. Treated as an auth failure at the edge (also 401
	// "auth_failed") so it never reveals whether an account exists.
	ErrUnknownUser = errors.New("auth: user not provisioned")
	// ErrAuthUnavailable means the identity backend could not answer (DB down,
	// provider unreachable/5xx, misconfig). Maps to a neutral 503
	// "auth_unavailable" plus an internal error id; the raw cause is logged
	// server-side only.
	ErrAuthUnavailable = errors.New("auth: identity backend unavailable")
	// ErrExpired means a session cookie's expiry has passed.
	ErrExpired = errors.New("auth: session expired")
	// ErrTampered means a session cookie failed signature or structural checks.
	ErrTampered = errors.New("auth: session invalid")
)

// AuthContext is a resolved identity: who the user is and the single org +
// role they act under. Contains no internal database ids — org is carried by
// its public uuid, the user by email + display name.
type AuthContext struct {
	Email       string
	DisplayName string
	OrgPublicID string
	OrgName     string
	Role        string
}

// AuthRow is the store's projection of a user's auth context. It mirrors
// AuthContext field-for-field so the store layer can hand back a value the
// directory converts without importing the generated db types here.
type AuthRow struct {
	Email       string
	DisplayName string
	OrgPublicID string
	OrgName     string
	Role        string
}

// Authenticator verifies a set of credentials and resolves the identity behind
// them. Implementations: DevAuthenticator (offline) and IdentityAuthenticator
// (server-side provider REST). Both return the sentinels above.
type Authenticator interface {
	Authenticate(ctx context.Context, email, password string) (AuthContext, error)
}

// Directory resolves a user's org + role by email, independent of how they
// authenticated. Used by /api/auth/me and by both authenticators to attach the
// local membership after a credential check.
type Directory interface {
	LookupByEmail(ctx context.Context, email string) (AuthContext, error)
}

// AuthQuerier is the minimal store dependency the directory needs. The store
// implements it; auth stays free of database types. The found bool separates
// "no such user" (found=false, err=nil) from an infrastructure failure (err).
type AuthQuerier interface {
	AuthContextByEmail(ctx context.Context, email string) (row AuthRow, found bool, err error)
}

// StoreDirectory is the production Directory backed by the store. Its querier
// may be nil (no database configured), in which case every lookup reports the
// backend unavailable rather than panicking — the app still boots without a DB.
type StoreDirectory struct {
	q AuthQuerier
}

// NewStoreDirectory builds a directory over q. Passing a nil q yields a
// directory whose lookups all return ErrAuthUnavailable.
func NewStoreDirectory(q AuthQuerier) *StoreDirectory {
	return &StoreDirectory{q: q}
}

// LookupByEmail resolves the auth context for email. It returns ErrUnknownUser
// when no user matches and ErrAuthUnavailable when the store is absent or
// errors (the raw cause is wrapped for server-side logging only).
func (d *StoreDirectory) LookupByEmail(ctx context.Context, email string) (AuthContext, error) {
	if d.q == nil {
		return AuthContext{}, ErrAuthUnavailable
	}
	row, found, err := d.q.AuthContextByEmail(ctx, email)
	if err != nil {
		return AuthContext{}, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	if !found {
		return AuthContext{}, ErrUnknownUser
	}
	return AuthContext(row), nil
}
