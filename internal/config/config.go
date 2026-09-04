// Package config loads server configuration from the environment.
package config

import (
	"errors"
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

// Environment names. Anything that is not development is treated as
// production, so a typo fails safe rather than unlocking dev defaults.
const (
	EnvironmentDevelopment = "development"
	EnvironmentProduction  = "production"
)

// missingVar is one required setting that was not supplied.
type missingVar struct {
	key  string
	help string
}

// Load reads configuration from the environment.
//
// ENVIRONMENT decides how forgiving this is, and it defaults to production on
// purpose. Convenience defaults -- a localhost database, a known JWT signing
// key, MinIO's well-known credentials -- are only ever applied when
// ENVIRONMENT=development is set explicitly.
//
// Defaulting the other way round is how a deployment ends up signing real
// tokens with a secret that is published in this repository. A missing variable
// must stop the process, not quietly downgrade it.
func Load() (*Config, error) {
	environment := env("ENVIRONMENT", EnvironmentProduction)
	development := environment == EnvironmentDevelopment

	// missing accumulates everything unset so one failed boot reports every
	// problem, rather than making the operator redeploy once per variable.
	var missing []missingVar

	// required returns the environment value, falling back to devDefault only
	// in development and otherwise recording the omission.
	required := func(key, devDefault, help string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		if development {
			return devDefault
		}
		missing = append(missing, missingVar{key: key, help: help})
		return ""
	}

	cfg := &Config{
		Addr:        ":" + env("PORT", "8080"),
		Environment: environment,
		DatabaseURL: required("DATABASE_URL",
			"postgres://domieface:domieface@localhost:5432/domieface?sslmode=disable",
			"Postgres connection string, e.g. postgres://user:pass@host:5432/db?sslmode=require"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		UploadGCAfter:   24 * time.Hour,
		Storage: StorageConfig{
			// Endpoint is genuinely optional: empty means real AWS S3.
			Endpoint: env("S3_ENDPOINT", devOnly(development, "http://localhost:9000")),
			Region:   env("S3_REGION", "us-east-1"),
			Bucket: required("S3_BUCKET", "domieface",
				"object storage bucket for uploaded images"),
			AccessKey: required("S3_ACCESS_KEY_ID", "minioadmin",
				"object storage access key"),
			SecretKey: required("S3_SECRET_ACCESS_KEY", "minioadmin",
				"object storage secret key"),
			PublicBaseURL: required("S3_PUBLIC_BASE_URL", "http://localhost:9000/domieface",
				"public base URL images are read back from; must be reachable from the client device"),
			PresignTTL: 15 * time.Minute,
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

	cfg.Storage.UsePathStyle = envBool("S3_USE_PATH_STYLE", development)

	secret := os.Getenv("JWT_SECRET")
	switch {
	case secret == "" && development:
		// Deterministic, so restarting the server does not invalidate every
		// access token a client is holding.
		secret = "insecure-development-secret-do-not-use-in-production"
	case secret == "":
		missing = append(missing, missingVar{
			key:  "JWT_SECRET",
			help: "at least 32 characters; generate with: openssl rand -base64 48",
		})
	case len(secret) < 32 && !development:
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters, got %d", len(secret))
	}
	cfg.JWTSecret = []byte(secret)

	if len(missing) > 0 {
		return nil, missingError(environment, missing)
	}

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
		cfg.SelfPing.URL, err = defaultSelfPingURL(cfg.Addr)
		if err != nil {
			return nil, err
		}
	}

	cfg.Storage.PublicBaseURL = strings.TrimRight(cfg.Storage.PublicBaseURL, "/")

	return cfg, nil
}

// devOnly returns value in development and the empty string otherwise, for
// settings whose local default would be actively wrong in production.
func devOnly(development bool, value string) string {
	if development {
		return value
	}
	return ""
}

// missingError reports every unset variable at once, with enough detail to fix
// them without reading the source.
func missingError(environment string, missing []missingVar) error {
	var b strings.Builder
	fmt.Fprintf(&b, "missing required configuration (ENVIRONMENT=%s):\n", environment)
	for _, m := range missing {
		fmt.Fprintf(&b, "  %-22s %s\n", m.key, m.help)
	}
	b.WriteString("\nSet these in the service environment. " +
		"For a local machine, set ENVIRONMENT=development to use local defaults instead.")
	return errors.New(b.String())
}

// defaultSelfPingURL picks what the keep-alive should request when
// SELF_PING_URL is not set.
func defaultSelfPingURL(addr string) (string, error) {
	// Render publishes the service's own public URL. Using it means the request
	// leaves and re-enters through the platform router, which is what stops the
	// instance being idled; a loopback request never reaches the router.
	if external := os.Getenv("RENDER_EXTERNAL_URL"); external != "" {
		return strings.TrimRight(external, "/") + "/healthz", nil
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("PORT: %w", err)
	}
	if host == "" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
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
