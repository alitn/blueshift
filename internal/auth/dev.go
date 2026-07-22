package auth

import (
	"context"
	"crypto/subtle"
)

// DevAuthenticator is the offline login backend used by `make demo` and local
// dev. It performs no network calls: the password must equal a single shared
// dev password, and the email must resolve to a seeded user + membership.
type DevAuthenticator struct {
	// Password is the shared dev password (DEV_PASSWORD).
	Password string
	// Dir resolves the seeded user's org + role.
	Dir Directory
}

// Authenticate resolves the user, then constant-time compares the password.
// Unknown users and wrong passwords both surface as auth failures upstream, so
// this never reveals whether an account exists.
func (a DevAuthenticator) Authenticate(ctx context.Context, email, password string) (AuthContext, error) {
	ac, err := a.Dir.LookupByEmail(ctx, email)
	if err != nil {
		return AuthContext{}, err
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(a.Password)) != 1 {
		return AuthContext{}, ErrAuthFailed
	}
	return ac, nil
}
