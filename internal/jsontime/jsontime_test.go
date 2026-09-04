package jsontime_test

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"domieface/com/internal/jsontime"
)

// contractFormat is the shape the API contract shows: ISO 8601 UTC with
// exactly three fractional digits.
var contractFormat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

func TestMarshalAlwaysUsesContractFormat(t *testing.T) {
	cases := map[string]time.Time{
		"whole second":     time.Date(2026, 9, 4, 11, 23, 0, 0, time.UTC),
		"sub-millisecond":  time.Date(2026, 9, 4, 11, 23, 0, 123456789, time.UTC),
		"non-UTC location": time.Date(2026, 9, 4, 12, 23, 0, 0, time.FixedZone("WAT", 3600)),
	}

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(jsontime.New(input))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var text string
			if err := json.Unmarshal(encoded, &text); err != nil {
				t.Fatalf("result was not a JSON string: %v", err)
			}
			if !contractFormat.MatchString(text) {
				t.Errorf("got %q, which does not match the contract format", text)
			}
		})
	}
}

func TestNonUTCIsConvertedNotRelabelled(t *testing.T) {
	// 12:23 WAT is 11:23 UTC. Emitting "12:23...Z" would be an hour wrong.
	input := time.Date(2026, 9, 4, 12, 23, 0, 0, time.FixedZone("WAT", 3600))

	encoded, err := json.Marshal(jsontime.New(input))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `"2026-09-04T11:23:00.000Z"`; string(encoded) != want {
		t.Errorf("got %s, want %s", encoded, want)
	}
}

func TestRoundTrip(t *testing.T) {
	original := jsontime.New(time.Date(2026, 9, 4, 11, 23, 0, 456000000, time.UTC))

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded jsontime.Time
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !decoded.Equal(original.Time) {
		t.Errorf("got %v, want %v", decoded.Time, original.Time)
	}
}

func TestUnmarshalAcceptsPlainRFC3339(t *testing.T) {
	// A client sending a timestamp without fractional digits must not be
	// rejected over a formatting detail.
	var decoded jsontime.Time
	if err := json.Unmarshal([]byte(`"2026-09-04T11:23:00Z"`), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := time.Date(2026, 9, 4, 11, 23, 0, 0, time.UTC); !decoded.Equal(want) {
		t.Errorf("got %v, want %v", decoded.Time, want)
	}
}

func TestUnmarshalRejectsNonsense(t *testing.T) {
	var decoded jsontime.Time
	if err := json.Unmarshal([]byte(`"not a date"`), &decoded); err == nil {
		t.Fatal("expected an error for a malformed timestamp")
	}
}
