package validate_test

import (
	"strings"
	"testing"

	"domieface/com/internal/validate"
)

func TestEmail(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		invalid bool
	}{
		{name: "valid", in: "ada@example.com", want: "ada@example.com"},
		{name: "stored lowercase", in: "Ada@Example.COM", want: "ada@example.com"},
		{name: "surrounding space is trimmed", in: "  ada@example.com  ", want: "ada@example.com"},
		{name: "empty", in: "", invalid: true},
		{name: "no domain", in: "ada@", invalid: true},
		{name: "no at sign", in: "ada.example.com", invalid: true},
		{name: "display name form is rejected", in: "Ada <ada@example.com>", invalid: true},
		{name: "over 254 characters", in: strings.Repeat("a", 250) + "@example.com", invalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errs validate.Errors
			got := validate.Email(&errs, "email", tc.in)

			if errs.Any() != tc.invalid {
				t.Fatalf("errors = %v, wanted invalid = %v", errs, tc.invalid)
			}
			if !tc.invalid && got != tc.want {
				t.Errorf("normalised to %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUsername(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		invalid bool
	}{
		{name: "valid", in: "ada"},
		{name: "digits and underscores", in: "ada_lovelace_1815"},
		{name: "at the minimum length", in: "ada"},
		{name: "at the maximum length", in: strings.Repeat("a", 30)},
		{name: "too short", in: "ad", invalid: true},
		{name: "too long", in: strings.Repeat("a", 31), invalid: true},
		{name: "uppercase is rejected", in: "Ada", invalid: true},
		{name: "hyphen is rejected", in: "ada-lovelace", invalid: true},
		{name: "full stop is rejected", in: "ada.lovelace", invalid: true},
		{name: "empty", in: "", invalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errs validate.Errors
			validate.Username(&errs, "username", tc.in)

			if errs.Any() != tc.invalid {
				t.Errorf("errors = %v, wanted invalid = %v", errs, tc.invalid)
			}
		})
	}
}

func TestPasswordChecksLengthOnly(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		invalid bool
	}{
		{name: "at the minimum", in: "12345678"},
		{name: "a long passphrase with no symbols", in: "correct-horse-battery"},
		{name: "at the maximum", in: strings.Repeat("a", 128)},
		{name: "too short", in: "1234567", invalid: true},
		{name: "too long", in: strings.Repeat("a", 129), invalid: true},
		{name: "empty", in: "", invalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errs validate.Errors
			validate.Password(&errs, "password", tc.in)

			if errs.Any() != tc.invalid {
				t.Errorf("errors = %v, wanted invalid = %v", errs, tc.invalid)
			}
		})
	}
}

// Lengths are counted in runes, not bytes, or a name in a non-Latin script
// would be rejected well short of the documented limit.
func TestLengthsAreCountedInRunes(t *testing.T) {
	var errs validate.Errors
	validate.DisplayName(&errs, "displayName", strings.Repeat("é", 50))
	if errs.Any() {
		t.Errorf("50 accented characters should fit a 50 character limit, got %v", errs)
	}

	errs = nil
	validate.Bio(&errs, "bio", strings.Repeat("ñ", 160))
	if errs.Any() {
		t.Errorf("160 accented characters should fit a 160 character limit, got %v", errs)
	}

	errs = nil
	validate.Caption(&errs, "caption", strings.Repeat("ø", 2201))
	if !errs.Any() {
		t.Error("2,201 characters should exceed the 2,200 character caption limit")
	}
}

func TestOptionalFieldsAcceptEmpty(t *testing.T) {
	var errs validate.Errors
	validate.Bio(&errs, "bio", "")
	validate.Caption(&errs, "caption", "")

	if errs.Any() {
		t.Errorf("bio and caption are optional, got %v", errs)
	}
}

func TestErrorsKeepsTheFirstReasonPerField(t *testing.T) {
	var errs validate.Errors
	errs.Add("email", "is required")
	errs.Add("email", "must be a valid email address")

	if got := errs["email"]; got != "is required" {
		t.Errorf("got %q, want the first reason recorded", got)
	}
}
