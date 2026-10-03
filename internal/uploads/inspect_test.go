package uploads

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// minimalWebP is a 1x1 lossless WebP.
var minimalWebP = []byte{
	0x52, 0x49, 0x46, 0x46, 0x1a, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50,
	0x56, 0x50, 0x38, 0x4c, 0x0d, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00,
	0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
}

func TestDecodeHeaderIdentifiesRealFormats(t *testing.T) {
	cases := []struct {
		name     string
		data     []byte
		wantType string
		wantW    int
		wantH    int
	}{
		{"png", encodePNG(t, 640, 480), "image/png", 640, 480},
		{"jpeg", encodeJPEG(t, 300, 900), "image/jpeg", 300, 900},
		{"webp", minimalWebP, "image/webp", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := DecodeHeader(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("DecodeHeader: %v", err)
			}
			if info.ActualType != tc.wantType || info.Width != tc.wantW || info.Height != tc.wantH {
				t.Errorf("got %s %dx%d, want %s %dx%d",
					info.ActualType, info.Width, info.Height, tc.wantType, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestDecodeHeaderRejectsNonImages(t *testing.T) {
	for name, data := range map[string][]byte{
		"text":      []byte("definitely not a picture, just labelled image/png"),
		"empty":     nil,
		"truncated": encodePNG(t, 400, 400)[:10],
		"gif":       []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHeader(bytes.NewReader(data)); !errors.Is(err, ErrNotAnImage) {
				t.Errorf("got %v, want ErrNotAnImage", err)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name    string
		purpose Purpose
		info    ImageInfo
		want    error
	}{
		{"valid post", PurposePost, ImageInfo{"image/jpeg", "image/jpeg", 320, 320}, nil},
		{"valid avatar", PurposeAvatar, ImageInfo{"image/png", "image/png", 200, 200}, nil},
		{"declared type has params", PurposePost, ImageInfo{"IMAGE/JPEG; q=1", "image/jpeg", 500, 500}, nil},
		{"type mismatch", PurposePost, ImageInfo{"image/png", "image/jpeg", 500, 500}, ErrTypeMismatch},
		{"post too narrow", PurposePost, ImageInfo{"image/jpeg", "image/jpeg", 319, 500}, ErrTooSmall},
		{"avatar too short", PurposeAvatar, ImageInfo{"image/jpeg", "image/jpeg", 500, 199}, ErrTooSmall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.purpose, &tc.info)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}
