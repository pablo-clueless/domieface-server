// Package uploads presigns object-storage URLs. Image bytes never pass through
// this API: the client asks for a URL here, PUTs straight to storage, then
// sends the returned key back on the resource.
package uploads

import (
	"fmt"
	"strings"
)

// Purpose is what an upload will be attached to.
type Purpose string

// The two purposes the contract defines.
const (
	PurposeAvatar Purpose = "avatar"
	PurposePost   Purpose = "post"
)

// Constraints are the per-purpose limits from section 4 of the contract.
type Constraints struct {
	MaxBytes int64
	// MinWidth and MinHeight are documented for the client and are NOT
	// enforced here: the bytes go straight to object storage, so this server
	// never sees the image and cannot measure it. See README, "Known gaps".
	MinWidth  int
	MinHeight int
}

// allowedContentTypes is the shared MIME allow list, mapping each type to the
// extension used when naming the object.
var allowedContentTypes = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
}

var constraintsByPurpose = map[Purpose]Constraints{
	PurposeAvatar: {MaxBytes: 5 << 20, MinWidth: 200, MinHeight: 200},
	PurposePost:   {MaxBytes: 10 << 20, MinWidth: 320, MinHeight: 320},
}

// ParsePurpose validates the `purpose` field of a presign request.
func ParsePurpose(raw string) (Purpose, error) {
	p := Purpose(strings.TrimSpace(raw))
	if _, ok := constraintsByPurpose[p]; !ok {
		return "", fmt.Errorf("must be either %q or %q", PurposeAvatar, PurposePost)
	}
	return p, nil
}

// ConstraintsFor returns the limits for a validated purpose.
func ConstraintsFor(p Purpose) Constraints { return constraintsByPurpose[p] }

// ExtensionFor returns the file extension for an allowed MIME type, and whether
// the type is allowed at all.
func ExtensionFor(contentType string) (string, bool) {
	ext, ok := allowedContentTypes[normaliseContentType(contentType)]
	return ext, ok
}

// AllowedContentTypes lists the accepted MIME types, for error messages.
func AllowedContentTypes() []string {
	return []string{"image/jpeg", "image/png", "image/webp"}
}

// normaliseContentType strips any parameters and lowercases the media type, so
// "IMAGE/JPEG; charset=utf-8" is recognised.
func normaliseContentType(raw string) string {
	mediaType, _, _ := strings.Cut(raw, ";")
	return strings.ToLower(strings.TrimSpace(mediaType))
}
