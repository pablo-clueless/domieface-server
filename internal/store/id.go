package store

import (
	"crypto/rand"
	"encoding/base32"
)

// idEncoding is lowercase base32 without padding: URL-safe, case-insensitive,
// and free of the characters that get mistaken for one another in logs.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID returns an opaque 128-bit identifier. The contract is explicit that
// clients must never parse, sort or do arithmetic on IDs, so these carry no
// embedded timestamp or sequence — pagination uses an explicit cursor instead.
func NewID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand never fails on any platform we target; if it somehow did,
		// continuing with a predictable ID would be far worse than crashing.
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return idEncoding.EncodeToString(buf)
}
