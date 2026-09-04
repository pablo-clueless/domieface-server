// Package config loads server configuration from the environment.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved configuration for one server process.
type Config struct {
	Addr        string
	DatabaseURL string
	Environment string

	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	Storage StorageConfig

	// UploadGCAfter is how long an unattached upload key survives before the
	// janitor reclaims it. The contract promises 24 hours.
	UploadGCAfter time.Duration

	// DocsEnabled serves the OpenAPI spec and Swagger UI. On by default; turn
	// it off for a deployment that would rather not advertise its surface.
	DocsEnabled bool

	// SelfPing keeps the process warm by requesting its own health endpoint on
	// an interval.
	SelfPing SelfPingConfig

	// RateLimit is the per-client request budget applied across the API.
	RateLimitBurst  int
	RateLimitPerMin int
	// AuthRateLimit is the much tighter budget on the credential endpoints,
	// which are the ones worth brute-forcing.
	AuthRateLimitBurst  int
	AuthRateLimitPerMin int
	TrustProxyHeader    bool
}

// StorageConfig points at an S3-compatible object store (MinIO locally, S3 in
// production). Uploads never pass through this API; we only sign URLs for it.
type StorageConfig struct {
	Endpoint      string // empty means real AWS S3
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	UsePathStyle  bool   // MinIO needs this
	PublicBaseURL string // where clients read objects back from
	PresignTTL    time.Duration
}

// SelfPingConfig controls the keep-alive ping.
//
// Pinging the loopback address keeps this process and its database pool warm,
// but it will NOT stop a platform-as-a-service idling the instance: those
// decide based on inbound traffic reaching their router, which a request from
// the container to itself never does. To keep such a host awake, set
// SELF_PING_URL to the service's public health URL.
type SelfPingConfig struct {
	Enabled  bool
	URL      string
	Interval time.Duration
	Timeout  time.Duration
}

// Load reads configuration from the environment, applying defaults that are
// safe for local development and failing fast on anything that must be set
// explicitly in production.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:            ":" + env("PORT", "8080"),
		DatabaseURL:     env("DATABASE_URL", "postgres://domieface:domieface@localhost:5432/domieface?sslmode=disable"),
		Environment:     env("ENVIRONMENT", "development"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		UploadGCAfter:   24 * time.Hour,
		Storage: StorageConfig{
			Endpoint:      env("S3_ENDPOINT", "http://localhost:9000"),
			Region:        env("S3_REGION", "us-east-1"),
			Bucket:        env("S3_BUCKET", "domieface"),
			AccessKey:     env("S3_ACCESS_KEY_ID", "minioadmin"),
			SecretKey:     env("S3_SECRET_ACCESS_KEY", "minioadmin"),
			PublicBaseURL: env("S3_PUBLIC_BASE_URL", "http://localhost:9000/domieface"),
			PresignTTL:    15 * time.Minute,
		},
		DocsEnabled: envBool("DOCS_ENABLED", true),
		SelfPing: SelfPingConfig{
			Enabled:  envBool("SELF_PING_ENABLED", true),
			URL:      os.Getenv("SELF_PING_URL"),
			Interval: 10 * time.Minute,
			Timeout:  10 * time.Second,
		},
		RateLimitPerMin:     120,
		RateLimitBurst:      40,
		AuthRateLimitPerMin: 20,
		AuthRateLimitBurst:  10,
		TrustProxyHeader:    envBool("TRUST_PROXY_HEADER", false),
	}

	cfg.Storage.UsePathStyle = envBool("S3_USE_PATH_STYLE", true)

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		if cfg.IsProduction() {
			return nil, fmt.Errorf("JWT_SECRET must be set when ENVIRONMENT=production")
		}
		// Deterministic dev secret so restarting the server does not invalidate
		// every access token a client is holding.
		secret = "insecure-development-secret-do-not-use-in-production"
	}
	if len(secret) < 32 && cfg.IsProduction() {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	cfg.JWTSecret = []byte(secret)

	var err error
	if cfg.AccessTokenTTL, err = envDuration("ACCESS_TOKEN_TTL", cfg.AccessTokenTTL); err != nil {
		return nil, err
	}
	if cfg.RefreshTokenTTL, err = envDuration("REFRESH_TOKEN_TTL", cfg.RefreshTokenTTL); err != nil {
		return nil, err
	}
	if cfg.Storage.PresignTTL, err = envDuration("PRESIGN_TTL", cfg.Storage.PresignTTL); err != nil {
		return nil, err
	}
	if cfg.UploadGCAfter, err = envDuration("UPLOAD_GC_AFTER", cfg.UploadGCAfter); err != nil {
		return nil, err
	}
	if cfg.RateLimitPerMin, err = envInt("RATE_LIMIT_PER_MIN", cfg.RateLimitPerMin); err != nil {
		return nil, err
	}
	if cfg.RateLimitBurst, err = envInt("RATE_LIMIT_BURST", cfg.RateLimitBurst); err != nil {
		return nil, err
	}
	if cfg.AuthRateLimitPerMin, err = envInt("AUTH_RATE_LIMIT_PER_MIN", cfg.AuthRateLimitPerMin); err != nil {
		return nil, err
	}
	if cfg.AuthRateLimitBurst, err = envInt("AUTH_RATE_LIMIT_BURST", cfg.AuthRateLimitBurst); err != nil {
		return nil, err
	}

	if cfg.SelfPing.Interval, err = envDuration("SELF_PING_INTERVAL", cfg.SelfPing.Interval); err != nil {
		return nil, err
	}
	if cfg.SelfPing.Timeout, err = envDuration("SELF_PING_TIMEOUT", cfg.SelfPing.Timeout); err != nil {
		return nil, err
	}
	if cfg.SelfPing.URL == "" {
		// Default to our own listener. Note the caveat on SelfPingConfig: this
		// keeps the process warm but does not stop a PaaS idling it.
		host, port, splitErr := net.SplitHostPort(cfg.Addr)
		if splitErr != nil {
			return nil, fmt.Errorf("PORT: %w", splitErr)
		}
		if host == "" {
			host = "localhost"
		}
		cfg.SelfPing.URL = "http://" + net.JoinHostPort(host, port) + "/healthz"
	}

	if cfg.Storage.Bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET must be set")
	}
	cfg.Storage.PublicBaseURL = strings.TrimRight(cfg.Storage.PublicBaseURL, "/")

	return cfg, nil
}

// IsProduction reports whether the server is running with production guardrails.
func (c *Config) IsProduction() bool { return c.Environment == "production" }

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
