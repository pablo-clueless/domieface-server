package api

import (
	"context"

	"domieface/com/internal/uploads"
)

// Presigner is the slice of object storage the API needs. It is an interface
// rather than the concrete *uploads.Presigner so handler tests can run without
// a bucket to talk to.
type Presigner interface {
	Presign(ctx context.Context, purpose uploads.Purpose, contentType string, contentLength int64) (*uploads.Presigned, error)
	PublicURL(key string) string
}
