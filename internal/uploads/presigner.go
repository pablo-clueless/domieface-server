package uploads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"domieface/com/internal/config"
)

// Presigner issues short-lived PUT URLs against an S3-compatible bucket and
// cleans up objects the janitor reclaims.
type Presigner struct {
	client        *s3.Client
	presign       *s3.PresignClient
	bucket        string
	publicBaseURL string
	ttl           time.Duration
}

// Presigned is the result handed back to the client.
type Presigned struct {
	UploadURL string
	Key       string
	ExpiresAt time.Time
}

// New builds a presigner. An empty Endpoint means real AWS S3; anything else
// (MinIO locally) is used verbatim with path-style addressing.
func New(ctx context.Context, cfg config.StorageConfig) (*Presigner, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})

	return &Presigner{
		client:        client,
		presign:       s3.NewPresignClient(client),
		bucket:        cfg.Bucket,
		publicBaseURL: cfg.PublicBaseURL,
		ttl:           cfg.PresignTTL,
	}, nil
}

// Presign returns a URL the client can PUT raw bytes to, with no Authorization
// header of ours attached.
//
// Content-Type and Content-Length are part of the signature, so the client
// cannot presign a 200 KB JPEG and then upload a 40 MB video: storage rejects
// any PUT whose headers do not match what was signed. That matters because this
// server never sees the bytes and so has no other chance to enforce the limits.
func (p *Presigner) Presign(ctx context.Context, purpose Purpose, contentType string, contentLength int64) (*Presigned, error) {
	ext, ok := ExtensionFor(contentType)
	if !ok {
		return nil, fmt.Errorf("uploads: content type %q is not allowed", contentType)
	}

	key := newKey(purpose, ext)
	expiresAt := time.Now().Add(p.ttl)

	req, err := p.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(p.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(normaliseContentType(contentType)),
		ContentLength: aws.Int64(contentLength),
	}, s3.WithPresignExpires(p.ttl))
	if err != nil {
		return nil, fmt.Errorf("presigning upload: %w", err)
	}

	return &Presigned{UploadURL: req.URL, Key: key, ExpiresAt: expiresAt}, nil
}

// PublicURL is where clients read an object back from.
func (p *Presigner) PublicURL(key string) string {
	if key == "" {
		return ""
	}
	return p.publicBaseURL + "/" + key
}

// Delete removes objects reclaimed by the janitor. It tolerates keys that are
// already gone, since an abandoned presign often means nothing was ever
// uploaded in the first place.
func (p *Presigner) Delete(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	// DeleteObjects takes at most 1,000 keys per call.
	const batchSize = 1000
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))

		objects := make([]types.ObjectIdentifier, 0, end-start)
		for _, key := range keys[start:end] {
			objects = append(objects, types.ObjectIdentifier{Key: aws.String(key)})
		}

		_, err := p.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(p.bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("deleting abandoned objects: %w", err)
		}
	}
	return nil
}

// newKey builds an opaque storage key. The random prefix keeps keys
// unguessable and spreads writes across the bucket's keyspace rather than
// clustering them under a shared date or user prefix.
func newKey(purpose Purpose, ext string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic("uploads: crypto/rand unavailable: " + err.Error())
	}
	return fmt.Sprintf("uploads/%s/%s.%s", hex.EncodeToString(buf), purpose, ext)
}
