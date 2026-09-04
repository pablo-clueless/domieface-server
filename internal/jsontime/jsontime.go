// Package jsontime provides the single timestamp format the API contract
// allows: ISO 8601 UTC with exactly three fractional digits, e.g.
// "2026-09-04T11:23:00.000Z".
//
// time.Time's own JSON encoding uses RFC 3339 with a variable number of
// fractional digits and the machine's offset, so clients would see
// "2026-09-04T11:23:00Z" one moment and "...11:23:00.123456789Z" the next.
// Wrapping it removes that variability at the type level.
package jsontime

import (
	"fmt"
	"strings"
	"time"
)

// Layout is the wire format for every timestamp the API emits or accepts.
const Layout = "2006-01-02T15:04:05.000Z"

// Time is a time.Time that always marshals in Layout, in UTC.
type Time struct{ time.Time }

// New wraps t, truncating it to the millisecond precision the wire format
// carries so that a value survives a marshal/unmarshal round trip unchanged.
func New(t time.Time) Time {
	return Time{t.UTC().Truncate(time.Millisecond)}
}

// Now returns the current time in wire precision.
func Now() Time { return New(time.Now()) }

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(Layout)+2)
	b = append(b, '"')
	b = t.UTC().AppendFormat(b, Layout)
	return append(b, '"'), nil
}

// UnmarshalJSON implements json.Unmarshaler. It accepts any RFC 3339 timestamp,
// not just Layout, so a client sending "2026-09-04T11:23:00Z" is not rejected
// over a formatting detail.
func (t *Time) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("expected an ISO 8601 timestamp, got %q", s)
	}
	t.Time = parsed.UTC().Truncate(time.Millisecond)
	return nil
}
