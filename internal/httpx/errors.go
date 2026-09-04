// Package httpx holds the transport-level plumbing shared by every handler:
// the error envelope, JSON encoding and decoding, and middleware.
package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// Error codes from the API contract. These are stable identifiers the client
// branches on; they are never rendered to a user.
const (
	CodeValidationFailed     = "VALIDATION_FAILED"
	CodeInvalidCredentials   = "INVALID_CREDENTIALS"
	CodeTokenExpired         = "TOKEN_EXPIRED"
	CodeTokenInvalid         = "TOKEN_INVALID"
	CodeForbidden            = "FORBIDDEN"
	CodeNotFound             = "NOT_FOUND"
	CodeConflict             = "CONFLICT"
	CodeFileTooLarge         = "FILE_TOO_LARGE"
	CodeUnsupportedMediaType = "UNSUPPORTED_MEDIA_TYPE"
	CodeRateLimited          = "RATE_LIMITED"
	CodeInternalError        = "INTERNAL_ERROR"
)

// APIError is an error that carries everything needed to render the contract's
// error envelope. Handlers return one of these; WriteError does the rest.
//
// Message is user-facing British English. Details maps a field name to a
// reason and drives inline form errors on the client.
type APIError struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`

	// cause is logged but never sent to the client.
	cause error
}

func (e *APIError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (e *APIError) Unwrap() error { return e.cause }

// WithCause attaches an internal error for logging. It is not serialised.
func (e *APIError) WithCause(err error) *APIError {
	clone := *e
	clone.cause = err
	return &clone
}

// WithDetails attaches per-field validation reasons.
func (e *APIError) WithDetails(details map[string]string) *APIError {
	clone := *e
	clone.Details = details
	return &clone
}

// Constructors, one per code in the contract.

// ErrValidation reports a body that failed validation. details maps field to reason.
func ErrValidation(message string, details map[string]string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: CodeValidationFailed, Message: message, Details: details}
}

// ErrInvalidCredentials reports a wrong email or password.
func ErrInvalidCredentials() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeInvalidCredentials, Message: "Incorrect email or password."}
}

// ErrTokenExpired tells the client to refresh and retry once.
func ErrTokenExpired() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeTokenExpired, Message: "Your session has expired. Please sign in again."}
}

// ErrTokenInvalid tells the client to log the user out.
func ErrTokenInvalid() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeTokenInvalid, Message: "Your session is no longer valid. Please sign in again."}
}

// ErrForbidden reports an authenticated caller acting outside their permissions.
func ErrForbidden(message string) *APIError {
	if message == "" {
		message = "You do not have permission to do that."
	}
	return &APIError{Status: http.StatusForbidden, Code: CodeForbidden, Message: message}
}

// ErrNotFound reports a missing resource.
func ErrNotFound(message string) *APIError {
	if message == "" {
		message = "We could not find what you were looking for."
	}
	return &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: message}
}

// ErrConflict reports an email or username already in use.
func ErrConflict(message string, details map[string]string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: message, Details: details}
}

// ErrFileTooLarge reports an upload over the size limit for its purpose.
func ErrFileTooLarge(message string) *APIError {
	return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeFileTooLarge, Message: message}
}

// ErrUnsupportedMediaType reports a MIME type outside the allow list.
func ErrUnsupportedMediaType(message string) *APIError {
	return &APIError{Status: http.StatusUnsupportedMediaType, Code: CodeUnsupportedMediaType, Message: message}
}

// ErrRateLimited reports that the caller should back off. The handler is
// responsible for setting Retry-After alongside it.
func ErrRateLimited() *APIError {
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: "Too many requests. Please try again shortly."}
}

// ErrInternal reports an unexpected failure. cause is logged, never sent.
func ErrInternal(cause error) *APIError {
	return &APIError{
		Status:  http.StatusInternalServerError,
		Code:    CodeInternalError,
		Message: "Something went wrong on our end. Please try again.",
		cause:   cause,
	}
}

// AsAPIError converts any error into an APIError, defaulting to a 500 so an
// unhandled failure can never leak an internal message to a client.
func AsAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return ErrInternal(err)
}
