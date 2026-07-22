package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Session is the decoded contents of a session cookie. It carries only what
// authz needs: the acting user (email), their org (public uuid), their role,
// and the expiry. No internal database ids ever enter the cookie — the payload
// is signed, not encrypted, so it is readable by anyone holding the cookie.
type Session struct {
	Email       string
	OrgPublicID string
	Role        string
	ExpiresAt   time.Time
}

// sessionPayload is the compact wire form: short JSON keys, expiry as unix
// seconds.
type sessionPayload struct {
	E string `json:"e"`
	O string `json:"o"`
	R string `json:"r"`
	X int64  `json:"x"`
}

// Codec mints and verifies session tokens with HMAC-SHA256 over a secret key.
// The token is "<base64url(payload)>.<base64url(sig)>"; the signature covers
// the encoded payload bytes exactly as transmitted, so verification is free of
// canonicalization ambiguity.
type Codec struct {
	key []byte
	now func() time.Time
}

// NewCodec returns a codec keyed by secret. An empty secret is accepted (config
// guarantees a non-empty value in every real deployment; dev uses a defaulted
// key with a startup WARN), but produces trivially forgeable tokens.
func NewCodec(secret string) *Codec {
	return &Codec{key: []byte(secret), now: time.Now}
}

// Mint encodes and signs s into a token string.
func (c *Codec) Mint(s Session) (string, error) {
	body, err := json.Marshal(sessionPayload{
		E: s.Email,
		O: s.OrgPublicID,
		R: s.Role,
		X: s.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	sig := c.sign([]byte(b64))
	return b64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks a token's signature and expiry and returns the decoded session.
// A structural or signature problem is ErrTampered; a valid-but-expired token
// is ErrExpired. Signature comparison is constant-time.
func (c *Codec) Verify(token string) (Session, error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return Session{}, ErrTampered
	}
	b64, sigPart := token[:dot], token[dot+1:]

	sig, err := base64.RawURLEncoding.DecodeString(sigPart)
	if err != nil {
		return Session{}, ErrTampered
	}
	if !hmac.Equal(sig, c.sign([]byte(b64))) {
		return Session{}, ErrTampered
	}

	body, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return Session{}, ErrTampered
	}
	var p sessionPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return Session{}, ErrTampered
	}

	exp := time.Unix(p.X, 0)
	if !c.now().Before(exp) {
		return Session{}, ErrExpired
	}
	return Session{Email: p.E, OrgPublicID: p.O, Role: p.R, ExpiresAt: exp}, nil
}

func (c *Codec) sign(b []byte) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write(b)
	return m.Sum(nil)
}
