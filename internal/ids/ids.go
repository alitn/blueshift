// Package ids renders and parses the public, prefixed identifiers that appear
// in URLs and the API (e.g. ep_0h2x…). The 16 bytes of an entity's public_id
// UUID are encoded as lowercase Crockford base32 (no padding) behind a typed
// prefix. Internal incremental database ids never appear here — only the UUID
// bytes and their prefix — so nothing about row counts or insertion order ever
// leaks to clients.
package ids

import (
	"errors"
	"fmt"
	"strings"
)

// Prefix is a typed public-id namespace. Only the exported constants below are
// registered; anything else is rejected on the way in (Decode/Parse).
type Prefix string

// The registered prefixes, one per externally-exposed entity. Org is exposed
// only inside storage keys (never in an API/URL path today), but it is a real
// public identifier the same way the others are and so lives in the same
// registry rather than being encoded ad hoc.
const (
	Org     Prefix = "org"
	Episode Prefix = "ep"
	Show    Prefix = "sh"
	Moment  Prefix = "mo"
	Clip    Prefix = "clip"
	Speaker Prefix = "sp"
)

// registry is the closed set of valid prefixes.
var registry = map[Prefix]struct{}{
	Org:     {},
	Episode: {},
	Show:    {},
	Moment:  {},
	Clip:    {},
	Speaker: {},
}

// sep separates the prefix from the encoded body: ep_<body>.
const sep = "_"

// bodyLen is the number of base32 symbols for 16 bytes (128 bits): ceil(128/5).
const bodyLen = 26

// Sentinel errors. Callers can match with errors.Is.
var (
	// ErrUnknownPrefix means the supplied Prefix is not in the registry.
	ErrUnknownPrefix = errors.New("ids: unknown prefix")
	// ErrWrongPrefix means the string's prefix did not match the expected one.
	ErrWrongPrefix = errors.New("ids: wrong prefix")
	// ErrNoSeparator means the string has no prefix separator.
	ErrNoSeparator = errors.New("ids: missing prefix separator")
	// ErrBadLength means the encoded body is not exactly bodyLen symbols.
	ErrBadLength = errors.New("ids: bad length")
	// ErrBadChar means the body contains a character outside the alphabet.
	ErrBadChar = errors.New("ids: invalid character")
	// ErrNonCanonical means the trailing padding bits were not zero, so the
	// string is not the canonical encoding of any 16-byte value.
	ErrNonCanonical = errors.New("ids: non-canonical encoding")
)

// Valid reports whether p is a registered prefix.
func Valid(p Prefix) bool {
	_, ok := registry[p]
	return ok
}

// Encode renders the 16-byte value b under prefix p. p is expected to be one of
// the exported Prefix constants; Encode does not validate it (the type and the
// registry guard the untrusted direction, Decode/Parse).
func Encode(p Prefix, b [16]byte) string {
	return string(p) + sep + encode32(b)
}

// Decode parses s, requiring it to carry the given prefix p, and returns the
// 16-byte value. It rejects an unregistered p, a mismatched prefix, and any
// malformed body.
func Decode(p Prefix, s string) ([16]byte, error) {
	var zero [16]byte
	if !Valid(p) {
		return zero, fmt.Errorf("%w: %q", ErrUnknownPrefix, p)
	}
	want := string(p) + sep
	rest, ok := strings.CutPrefix(s, want)
	if !ok {
		return zero, fmt.Errorf("%w: want %q", ErrWrongPrefix, p)
	}
	return decode32(rest)
}

// Parse splits s on its separator, resolves the prefix against the registry,
// and decodes the body. It is the untrusted-input entry point when the caller
// does not know the expected prefix in advance.
func Parse(s string) (Prefix, [16]byte, error) {
	var zero [16]byte
	i := strings.IndexByte(s, sep[0])
	if i <= 0 {
		return "", zero, fmt.Errorf("%w: %q", ErrNoSeparator, s)
	}
	p := Prefix(s[:i])
	if !Valid(p) {
		return "", zero, fmt.Errorf("%w: %q", ErrUnknownPrefix, p)
	}
	b, err := decode32(s[i+1:])
	if err != nil {
		return "", zero, err
	}
	return p, b, nil
}

// crockAlphabet is Crockford base32, lowercase: digits then letters excluding
// i, l, o, u. Index in this string is the 5-bit symbol value.
const crockAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// encode32 encodes 16 bytes (128 bits) as bodyLen base32 symbols, big-endian.
func encode32(b [16]byte) string {
	out := make([]byte, 0, bodyLen)
	var acc uint32
	var bits uint
	for _, c := range b {
		acc = acc<<8 | uint32(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockAlphabet[(acc>>bits)&0x1f])
		}
	}
	if bits > 0 { // final 3 bits, left-padded to a 5-bit symbol
		out = append(out, crockAlphabet[(acc<<(5-bits))&0x1f])
	}
	return string(out)
}

// decode32 reverses encode32. It requires exactly bodyLen symbols, all in the
// alphabet, and canonical trailing padding (the 2 leftover bits must be zero).
func decode32(s string) ([16]byte, error) {
	var zero [16]byte
	if len(s) != bodyLen {
		return zero, fmt.Errorf("%w: got %d symbols, want %d", ErrBadLength, len(s), bodyLen)
	}
	var out [16]byte
	n := 0
	var acc uint32
	var bits uint
	for i := 0; i < len(s); i++ {
		v, ok := crockValue(s[i])
		if !ok {
			return zero, fmt.Errorf("%w: %q", ErrBadChar, s[i])
		}
		acc = acc<<5 | uint32(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out[n] = byte((acc >> bits) & 0xff)
			n++
		}
	}
	// 26*5 = 130 bits produced, 16 bytes (128 bits) consumed, 2 bits remain.
	// For a canonical encoding those padding bits must be zero.
	if acc&((1<<bits)-1) != 0 {
		return zero, ErrNonCanonical
	}
	return out, nil
}

// crockValue maps one Crockford base32 symbol to its 5-bit value,
// case-insensitively and honoring the standard ambiguity folds (o→0, i/l→1).
// The excluded letter u is rejected.
func crockValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c == 'o' || c == 'O':
		return 0, true
	case c == 'i' || c == 'I' || c == 'l' || c == 'L':
		return 1, true
	default:
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if idx := strings.IndexByte(crockAlphabet, c); idx >= 0 {
			return byte(idx), true
		}
		return 0, false
	}
}
