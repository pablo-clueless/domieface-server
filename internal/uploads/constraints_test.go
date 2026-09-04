package uploads_test

import (
	"testing"

	"domieface/com/internal/uploads"
)

func TestParsePurpose(t *testing.T) {
	cases := []struct {
		in      string
		want    uploads.Purpose
		invalid bool
	}{
		{in: "avatar", want: uploads.PurposeAvatar},
		{in: "post", want: uploads.PurposePost},
		{in: " post ", want: uploads.PurposePost},
		{in: "banner", invalid: true},
		{in: "Avatar", invalid: true},
		{in: "", invalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := uploads.ParsePurpose(tc.in)
			if (err != nil) != tc.invalid {
				t.Fatalf("err = %v, wanted invalid = %v", err, tc.invalid)
			}
			if !tc.invalid && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtensionFor(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		allowed bool
	}{
		{in: "image/jpeg", want: "jpg", allowed: true},
		{in: "image/png", want: "png", allowed: true},
		{in: "image/webp", want: "webp", allowed: true},
		{in: "IMAGE/JPEG", want: "jpg", allowed: true},
		{in: "image/jpeg; charset=binary", want: "jpg", allowed: true},
		{in: "image/gif"},
		{in: "image/heic"},
		{in: "application/pdf"},
		{in: "video/mp4"},
		{in: ""},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, allowed := uploads.ExtensionFor(tc.in)
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", allowed, tc.allowed)
			}
			if allowed && got != tc.want {
				t.Errorf("extension: got %q, want %q", got, tc.want)
			}
		})
	}
}

// The limits differ per purpose, and mixing them up is exactly what binding a
// key to its purpose at presign time is there to prevent.
func TestConstraintsMatchTheContract(t *testing.T) {
	avatar := uploads.ConstraintsFor(uploads.PurposeAvatar)
	if avatar.MaxBytes != 5<<20 {
		t.Errorf("avatar max: got %d, want 5 MB", avatar.MaxBytes)
	}
	if avatar.MinWidth != 200 || avatar.MinHeight != 200 {
		t.Errorf("avatar min dimensions: got %dx%d, want 200x200", avatar.MinWidth, avatar.MinHeight)
	}

	post := uploads.ConstraintsFor(uploads.PurposePost)
	if post.MaxBytes != 10<<20 {
		t.Errorf("post max: got %d, want 10 MB", post.MaxBytes)
	}
	if post.MinWidth != 320 || post.MinHeight != 320 {
		t.Errorf("post min dimensions: got %dx%d, want 320x320", post.MinWidth, post.MinHeight)
	}
}
