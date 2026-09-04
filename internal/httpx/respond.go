package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// maxBodyBytes caps JSON request bodies. Images never pass through this API —
// they go straight to object storage — so a megabyte is generous.
const maxBodyBytes = 1 << 20

// errorEnvelope is the exact wire shape the contract specifies for failures.
type errorEnvelope struct {
	Error *APIError `json:"error"`
}

// WriteJSON serialises v as the response body. Success responses return the
// resource directly, with no wrapper.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("encoding response failed", "error", err)
		writeRaw(w, http.StatusInternalServerError, mustEnvelope(ErrInternal(err)))
		return
	}
	writeRaw(w, status, body)
}

// NoContent writes a bodiless 204.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// WriteError renders err in the contract's error envelope and logs anything
// that represents a server fault.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := AsAPIError(err)

	if apiErr.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path,
			"code", apiErr.Code, "error", apiErr.Error())
	} else {
		slog.DebugContext(r.Context(), "request rejected",
			"method", r.Method, "path", r.URL.Path,
			"status", apiErr.Status, "code", apiErr.Code)
	}

	writeRaw(w, apiErr.Status, mustEnvelope(apiErr))
}

func mustEnvelope(apiErr *APIError) []byte {
	body, err := json.Marshal(errorEnvelope{Error: apiErr})
	if err != nil {
		// Every field is a string or a map of strings, so this is unreachable;
		// fall back to a hand-built envelope rather than panicking mid-response.
		return []byte(`{"error":{"code":"INTERNAL_ERROR","message":"Something went wrong on our end. Please try again."}}`)
	}
	return body
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// DecodeJSON reads exactly one JSON object from the request into dst. It
// returns a VALIDATION_FAILED error for anything malformed, so a broken body
// never surfaces as a 500.
func DecodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mediaType := strings.TrimSpace(strings.Split(ct, ";")[0]); mediaType != "application/json" {
			return ErrUnsupportedMediaType("This endpoint accepts JSON only.")
		}
	}

	body := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(body)

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return ErrValidation("A request body is required.", nil)
		case errors.As(err, &maxErr):
			return ErrFileTooLarge("That request body is too large.")
		}

		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			return ErrValidation("That request body could not be read.", map[string]string{
				typeErr.Field: "expected " + typeErr.Type.String(),
			})
		}
		return ErrValidation("That request body could not be read.", nil).WithCause(err)
	}

	// A second value means the client sent concatenated JSON documents.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrValidation("A request body must contain a single JSON object.", nil)
	}
	return nil
}

// Handler is a http.Handler whose errors are returned rather than written, so
// handlers can `return httpx.ErrNotFound(...)` instead of writing and remembering
// to return.
type Handler func(w http.ResponseWriter, r *http.Request) error

// ServeHTTP implements http.Handler.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteError(w, r, err)
	}
}
