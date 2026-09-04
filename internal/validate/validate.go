// Package validate implements the field rules from section 6 of the API
// contract. The client mirrors these for instant feedback; this is the copy
// that actually decides, and its details map is what the client renders.
package validate

import (
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits from the contract, exported so handlers and tests share one source.
const (
	EmailMaxLength       = 254
	UsernameMinLength    = 3
	UsernameMaxLength    = 30
	PasswordMinLength    = 8
	PasswordMaxLength    = 128
	DisplayNameMinLength = 1
	DisplayNameMaxLength = 50
	BioMaxLength         = 160
	CaptionMaxLength     = 2200
)

// usernamePattern is the contract's rule verbatim: lowercase letters, digits
// and underscores only.
var usernamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// Errors accumulates field name to reason, matching the `details` object in the
// error envelope. The zero value is ready to use.
type Errors map[string]string

// Add records a reason for a field, keeping the first reason recorded so the
// most specific check wins.
func (e *Errors) Add(field, reason string) {
	if *e == nil {
		*e = make(Errors)
	}
	if _, exists := (*e)[field]; !exists {
		(*e)[field] = reason
	}
}

// Any reports whether anything failed.
func (e Errors) Any() bool { return len(e) > 0 }

// Email normalises and checks an address. Stored lowercase, per the contract.
func Email(errs *Errors, field, value string) string {
	normalised := strings.ToLower(strings.TrimSpace(value))
	switch {
	case normalised == "":
		errs.Add(field, "is required")
	case len(normalised) > EmailMaxLength:
		errs.Add(field, "must be 254 characters or fewer")
	default:
		addr, err := mail.ParseAddress(normalised)
		if err != nil || addr.Address != normalised {
			errs.Add(field, "must be a valid email address")
		}
	}
	return normalised
}

// Username checks the immutable handle chosen at registration.
func Username(errs *Errors, field, value string) string {
	normalised := strings.TrimSpace(value)
	switch {
	case normalised == "":
		errs.Add(field, "is required")
	case len(normalised) < UsernameMinLength || len(normalised) > UsernameMaxLength:
		errs.Add(field, "must be between 3 and 30 characters")
	case !usernamePattern.MatchString(normalised):
		errs.Add(field, "may only contain lowercase letters, numbers and underscores")
	}
	return normalised
}

// Password checks length only. The contract is explicit that length is what
// matters and composition rules are not applied.
func Password(errs *Errors, field, value string) string {
	switch {
	case value == "":
		errs.Add(field, "is required")
	case len(value) < PasswordMinLength:
		errs.Add(field, "must be at least 8 characters")
	case len(value) > PasswordMaxLength:
		errs.Add(field, "must be 128 characters or fewer")
	}
	return value
}

// DisplayName checks the 1–50 character name shown to other users.
func DisplayName(errs *Errors, field, value string) string {
	normalised := strings.TrimSpace(value)
	switch {
	case utf8.RuneCountInString(normalised) < DisplayNameMinLength:
		errs.Add(field, "is required")
	case utf8.RuneCountInString(normalised) > DisplayNameMaxLength:
		errs.Add(field, "must be 50 characters or fewer")
	}
	return normalised
}

// Bio checks the optional 160 character profile line.
func Bio(errs *Errors, field, value string) string {
	normalised := strings.TrimSpace(value)
	if utf8.RuneCountInString(normalised) > BioMaxLength {
		errs.Add(field, "must be 160 characters or fewer")
	}
	return normalised
}

// Caption checks the optional 2,200 character post caption.
func Caption(errs *Errors, field, value string) string {
	normalised := strings.TrimSpace(value)
	if utf8.RuneCountInString(normalised) > CaptionMaxLength {
		errs.Add(field, "must be 2,200 characters or fewer")
	}
	return normalised
}
