package api

import (
	"fmt"
	"net/http"
	"strings"

	"domieface/com/internal/httpx"
	"domieface/com/internal/jsontime"
	"domieface/com/internal/store"
	"domieface/com/internal/uploads"
	"domieface/com/internal/validate"
)

type presignRequest struct {
	ContentType   string `json:"contentType"`
	ContentLength int64  `json:"contentLength"`
	Purpose       string `json:"purpose"`
}

// handlePresign implements POST /v1/uploads/presign.
//
// The returned URL points at object storage, not at us. The client PUTs raw
// bytes to it with no Authorization header, then sends the key back on
// PATCH /users/me or POST /posts.
func (s *Server) handlePresign(w http.ResponseWriter, r *http.Request) error {
	me := currentUser(r.Context())

	var req presignRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	purpose, err := uploads.ParsePurpose(req.Purpose)
	if err != nil {
		return httpx.ErrValidation("That upload purpose is not valid.", validate.Errors{
			"purpose": err.Error(),
		})
	}

	// Media type and size get their own contract codes rather than folding into
	// VALIDATION_FAILED, so the client can tell "wrong format" from "too big"
	// and say something useful.
	if _, allowed := uploads.ExtensionFor(req.ContentType); !allowed {
		return httpx.ErrUnsupportedMediaType(fmt.Sprintf(
			"Images must be one of: %s.", strings.Join(uploads.AllowedContentTypes(), ", ")))
	}

	limits := uploads.ConstraintsFor(purpose)
	switch {
	case req.ContentLength <= 0:
		return httpx.ErrValidation("A content length is required.", validate.Errors{
			"contentLength": "must be greater than zero",
		})
	case req.ContentLength > limits.MaxBytes:
		return httpx.ErrFileTooLarge(fmt.Sprintf(
			"That image is too large. The limit for a %s is %d MB.", purpose, limits.MaxBytes>>20))
	}

	presigned, err := s.presigner.Presign(r.Context(), purpose, req.ContentType, req.ContentLength)
	if err != nil {
		return httpx.ErrInternal(err)
	}

	// Record the key so an upload that is never attached can be reclaimed, and
	// so attaching it later can be checked against the caller and purpose.
	err = s.store.Uploads().Reserve(r.Context(), store.Upload{
		Key:           presigned.Key,
		UserID:        me.ID,
		Purpose:       string(purpose),
		ContentType:   req.ContentType,
		ContentLength: req.ContentLength,
	})
	if err != nil {
		return httpx.ErrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusOK, presignResponse{
		UploadURL: presigned.UploadURL,
		Key:       presigned.Key,
		ExpiresAt: jsontime.New(presigned.ExpiresAt),
	})
	return nil
}
