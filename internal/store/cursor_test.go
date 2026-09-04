package store_test

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"domieface/com/internal/store"
)

func TestCursorRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 11, 23, 0, 0, time.UTC)

	gotAt, gotID, err := store.DecodeCursor(store.EncodeCursor(createdAt, "post-1"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotAt.Equal(createdAt) {
		t.Errorf("createdAt: got %v, want %v", gotAt, createdAt)
	}
	if gotID != "post-1" {
		t.Errorf("id: got %q, want %q", gotID, "post-1")
	}
}

func TestDecodeCursorRejectsClientConstructed(t *testing.T) {
	// The contract tells clients to pass nextCursor back verbatim and never
	// construct one. Anything hand-rolled must be refused rather than silently
	// paginating from an arbitrary position.
	cases := map[string]string{
		"not base64":    "!!!not base64!!!",
		"not JSON":      base64.StdEncoding.EncodeToString([]byte("just text")),
		"missing id":    base64.StdEncoding.EncodeToString([]byte(`{"createdAt":"2026-09-04T00:00:00Z"}`)),
		"missing time":  base64.StdEncoding.EncodeToString([]byte(`{"id":"post-1"}`)),
		"empty payload": base64.StdEncoding.EncodeToString([]byte(`{}`)),
	}

	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.DecodeCursor(cursor); !errors.Is(err, store.ErrInvalidCursor) {
				t.Errorf("got %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestPageRequestNormalise(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to the default", 0, store.DefaultPageLimit},
		{"negative falls back to the default", -5, store.DefaultPageLimit},
		{"in range is kept", 25, 25},
		{"over the maximum is clamped", 500, store.MaxPageLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.PageRequest{Limit: tc.in}.Normalise().Limit
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNewIDIsOpaqueAndUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := store.NewID()
		if seen[id] {
			t.Fatalf("duplicate ID generated: %q", id)
		}
		seen[id] = true
	}
}
