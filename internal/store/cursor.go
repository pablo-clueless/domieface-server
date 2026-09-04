package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// ErrInvalidCursor means the client sent a cursor we did not issue. The
// contract tells clients to pass nextCursor back verbatim and never construct
// one, so this is a client bug rather than a server fault.
var ErrInvalidCursor = errors.New("store: invalid cursor")

// Pagination limits from the contract.
const (
	DefaultPageLimit = 20
	MaxPageLimit     = 50
)

// cursorPayload is the keyset position: posts are ordered by createdAt then id,
// both descending, and the ID tie-breaks posts created in the same millisecond.
// Without it, two posts sharing a timestamp could be skipped or repeated at a
// page boundary — the exact failure offset paging was rejected to avoid.
type cursorPayload struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

// EncodeCursor produces the opaque string handed back as nextCursor.
func EncodeCursor(createdAt time.Time, id string) string {
	raw, err := json.Marshal(cursorPayload{CreatedAt: createdAt.UTC(), ID: id})
	if err != nil {
		// Both fields are trivially serialisable; an error here is impossible.
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// DecodeCursor reverses EncodeCursor.
func DecodeCursor(cursor string) (createdAt time.Time, id string, err error) {
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	if payload.ID == "" || payload.CreatedAt.IsZero() {
		return time.Time{}, "", ErrInvalidCursor
	}
	return payload.CreatedAt.UTC(), payload.ID, nil
}

// Normalise clamps a page request to the contract's limits.
func (r PageRequest) Normalise() PageRequest {
	switch {
	case r.Limit <= 0:
		r.Limit = DefaultPageLimit
	case r.Limit > MaxPageLimit:
		r.Limit = MaxPageLimit
	}
	return r
}
