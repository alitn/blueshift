package auth

import "context"

// Principal is the authenticated caller resolved from a session cookie by the
// server's authn middleware and read back by request handlers. It is the
// per-request identity used for org scoping and role checks.
type Principal struct {
	Email       string
	OrgPublicID string
	Role        string
}

type principalKey struct{}

// NewContext returns a copy of ctx carrying p.
func NewContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal set by the authn middleware, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
