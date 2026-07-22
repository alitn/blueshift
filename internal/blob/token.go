package blob

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Token errors surfaced by the local upload/download handler.
var (
	// ErrTampered means the token's signature did not verify (wrong key,
	// truncated, or edited payload).
	ErrTampered = errors.New("blob: token signature invalid")
	// ErrExpired means the token verified but its expiry has passed.
	ErrExpired = errors.New("blob: token expired")
)

// op distinguishes what a local token authorizes, so a read token can never be
// replayed as a write token for the same key or vice versa.
type op string

const (
	opPut op = "put"
	opGet op = "get"
)

// tokenPayload is the signed contents of a local blob token: the object key, the
// operation it authorizes, and an absolute expiry (unix seconds). It reuses the
// same HMAC-SHA256-over-base64url construction as the session codec, keyed by
// the session secret, so local "signing" needs no new secret machinery.
type tokenPayload struct {
	K string `json:"k"`
	O op     `json:"o"`
	X int64  `json:"x"`
}

// signer mints and verifies local blob tokens with an HMAC key.
type signer struct {
	key []byte
	now func() time.Time
}

func newSigner(key []byte, now func() time.Time) *signer {
	if now == nil {
		now = time.Now
	}
	return &signer{key: key, now: now}
}

// mint returns a signed token authorizing o on key until now+ttl. The token is
// "<base64url(payload)>.<base64url(sig)>", the signature covering the encoded
// payload bytes exactly as transmitted.
func (s *signer) mint(key string, o op, ttl time.Duration) (string, error) {
	body, err := json.Marshal(tokenPayload{K: key, O: o, X: s.now().Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	sig := s.sign([]byte(b64))
	return b64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// verify checks a token's signature and expiry and returns the key it
// authorizes for operation o. A structural or signature problem is ErrTampered;
// a valid-but-expired token is ErrExpired; a valid token minted for a different
// operation is ErrTampered (a read grant is not a write grant).
func (s *signer) verify(token string, o op) (string, error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return "", ErrTampered
	}
	b64, sigPart := token[:dot], token[dot+1:]

	sig, err := base64.RawURLEncoding.DecodeString(sigPart)
	if err != nil {
		return "", ErrTampered
	}
	if !hmac.Equal(sig, s.sign([]byte(b64))) {
		return "", ErrTampered
	}
	body, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return "", ErrTampered
	}
	var p tokenPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", ErrTampered
	}
	if p.O != o {
		return "", ErrTampered
	}
	if !s.now().Before(time.Unix(p.X, 0)) {
		return "", ErrExpired
	}
	return p.K, nil
}

func (s *signer) sign(b []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(b)
	return m.Sum(nil)
}
