package uploads

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"

	// Decoders register themselves with image.DecodeConfig.
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Reasons an uploaded object can fail inspection. Callers match on these to
// pick a contract error code.
var (
	// ErrObjectMissing means the key was presigned but nothing was ever PUT.
	ErrObjectMissing = errors.New("uploads: object has not been uploaded")
	// ErrNotAnImage means the bytes are not a JPEG, PNG or WebP at all.
	ErrNotAnImage = errors.New("uploads: object is not a supported image")
	// ErrTypeMismatch means the bytes are a real image, but not the type the
	// upload was presigned (and stored) as.
	ErrTypeMismatch = errors.New("uploads: image type does not match its declared content type")
	// ErrTooSmall means the image is below the purpose's minimum dimensions.
	ErrTooSmall = errors.New("uploads: image is smaller than the minimum dimensions")
)

// contentTypeForFormat maps image.DecodeConfig's format names onto the MIME
// types in the allow list.
var contentTypeForFormat = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
}

// ImageInfo is what inspection learns about an uploaded object.
type ImageInfo struct {
	// DeclaredType is the Content-Type the object was stored with. It was part
	// of the presigned signature, so it is what the client asked for.
	DeclaredType string
	// ActualType is what the bytes decode as.
	ActualType string
	Width      int
	Height     int
}

// Inspect reads just enough of an uploaded object to identify its format and
// dimensions. Only the image header is decoded, never the pixels, so a large
// or hostile image costs a few kilobytes of transfer rather than a full decode.
func (p *Presigner) Inspect(ctx context.Context, key string) (*ImageInfo, error) {
	out, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrObjectMissing
		}
		return nil, fmt.Errorf("reading uploaded object: %w", err)
	}
	defer out.Body.Close()

	info, err := DecodeHeader(out.Body)
	if err != nil {
		return nil, err
	}
	info.DeclaredType = normaliseContentType(aws.ToString(out.ContentType))
	return info, nil
}

// DecodeHeader identifies the image format and dimensions from the start of r.
// DeclaredType is left empty for the caller to fill in.
func DecodeHeader(r io.Reader) (*ImageInfo, error) {
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		return nil, ErrNotAnImage
	}
	actual, ok := contentTypeForFormat[format]
	if !ok {
		return nil, ErrNotAnImage
	}
	return &ImageInfo{ActualType: actual, Width: cfg.Width, Height: cfg.Height}, nil
}

// Check holds an inspected image to the contract for its purpose.
func Check(purpose Purpose, info *ImageInfo) error {
	if info.ActualType != normaliseContentType(info.DeclaredType) {
		return ErrTypeMismatch
	}
	limits := ConstraintsFor(purpose)
	if info.Width < limits.MinWidth || info.Height < limits.MinHeight {
		return fmt.Errorf("%w: got %dx%d, need at least %dx%d",
			ErrTooSmall, info.Width, info.Height, limits.MinWidth, limits.MinHeight)
	}
	return nil
}

// isNotFound reports whether storage said the object does not exist. Real S3
// returns a typed NoSuchKey; some S3-compatible stores only send the code.
func isNotFound(err error) bool {
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}
