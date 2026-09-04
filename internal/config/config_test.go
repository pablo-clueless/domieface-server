package config_test

import (
	"strings"
	"testing"

	"domieface/com/internal/config"
)

// production sets the minimum a real deployment needs, so individual tests can
// remove one variable and assert on that.
func production(t *testing.T) {
	t.Helper()
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DATABASE_URL", "postgres://user:pass@db.example.com:5432/app?sslmode=require")
	t.Setenv("JWT_SECRET", strings.Repeat("s", 48))
	t.Setenv("S3_BUCKET", "images")
	t.Setenv("S3_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("S3_PUBLIC_BASE_URL", "https://cdn.example.com")
}

// This is the bug that broke the first Render deploy: with no DATABASE_URL the
// server used to fall back to localhost and fail with "connection refused",
// which points at the network rather than at the missing variable.
func TestProductionRefusesToFallBackToLocalhost(t *testing.T) {
	production(t)
	t.Setenv("DATABASE_URL", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load succeeded with no DATABASE_URL in production")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("the error should name the variable, got: %v", err)
	}
	if strings.Contains(err.Error(), "localhost") {
		t.Errorf("the error should not offer a localhost default in production: %v", err)
	}
}

// The severe one. ENVIRONMENT now defaults to production, so a deployment that
// forgets to set it cannot silently sign real tokens with the development key
// that is committed to this repository.
func TestUnsetEnvironmentDefaultsToProduction(t *testing.T) {
	production(t)
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("JWT_SECRET", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load succeeded with no JWT_SECRET and no ENVIRONMENT; dev defaults leaked into a deployment")
	}
	if !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Errorf("the error should name JWT_SECRET, got: %v", err)
	}
}

// A typo in ENVIRONMENT must fail safe rather than unlock the dev defaults.
func TestUnknownEnvironmentIsTreatedAsProduction(t *testing.T) {
	production(t)
	t.Setenv("ENVIRONMENT", "prod") // not the exact string "development"
	t.Setenv("JWT_SECRET", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("a misspelled ENVIRONMENT unlocked development defaults")
	}
}

// One failed boot should report every problem, not make the operator redeploy
// once per missing variable.
func TestEveryMissingVariableIsReportedAtOnce(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	for _, key := range []string{
		"DATABASE_URL", "JWT_SECRET", "S3_BUCKET",
		"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_PUBLIC_BASE_URL",
	} {
		t.Setenv(key, "")
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load succeeded with nothing configured")
	}

	for _, key := range []string{
		"DATABASE_URL", "JWT_SECRET", "S3_BUCKET",
		"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_PUBLIC_BASE_URL",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%s is missing but was not reported: %v", key, err)
		}
	}
}

func TestProductionRejectsAShortJWTSecret(t *testing.T) {
	production(t)
	t.Setenv("JWT_SECRET", "too-short")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load accepted a JWT secret under 32 characters in production")
	}
	if !strings.Contains(err.Error(), "32") {
		t.Errorf("the error should state the minimum length, got: %v", err)
	}
}

func TestProductionLoadsWithEverythingSet(t *testing.T) {
	production(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.IsProduction() != true {
		t.Error("IsProduction should be true")
	}
	if strings.Contains(cfg.DatabaseURL, "localhost") {
		t.Errorf("DatabaseURL: got %q, want the configured value", cfg.DatabaseURL)
	}
	// Real S3 unless an endpoint is given: the MinIO default must not leak.
	if cfg.Storage.Endpoint != "" {
		t.Errorf("S3 endpoint: got %q, want empty for real AWS S3", cfg.Storage.Endpoint)
	}
	if cfg.Storage.UsePathStyle {
		t.Error("path-style addressing is a MinIO workaround and should be off by default in production")
	}
}

// Local development must stay a single command with nothing configured.
func TestDevelopmentStillWorksWithNothingSet(t *testing.T) {
	t.Setenv("ENVIRONMENT", "development")
	for _, key := range []string{
		"DATABASE_URL", "JWT_SECRET", "S3_BUCKET", "S3_ENDPOINT",
		"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_PUBLIC_BASE_URL",
	} {
		t.Setenv(key, "")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("development Load should need no configuration, got: %v", err)
	}
	if !strings.Contains(cfg.DatabaseURL, "localhost") {
		t.Errorf("DatabaseURL: got %q, want the local default", cfg.DatabaseURL)
	}
	if cfg.Storage.Endpoint != "http://localhost:9000" {
		t.Errorf("S3 endpoint: got %q, want the local MinIO", cfg.Storage.Endpoint)
	}
	if !cfg.Storage.UsePathStyle {
		t.Error("MinIO needs path-style addressing in development")
	}
	if len(cfg.JWTSecret) == 0 {
		t.Error("development should supply a deterministic signing key")
	}
}

// On Render the keep-alive has to leave and re-enter through the platform
// router; a loopback request never reaches it, so it cannot prevent idling.
func TestSelfPingPrefersTheRenderExternalURL(t *testing.T) {
	production(t)
	t.Setenv("RENDER_EXTERNAL_URL", "https://domieface.onrender.com/")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "https://domieface.onrender.com/healthz"; cfg.SelfPing.URL != want {
		t.Errorf("self-ping URL: got %q, want %q", cfg.SelfPing.URL, want)
	}
}

func TestSelfPingURLIsOverridable(t *testing.T) {
	production(t)
	t.Setenv("RENDER_EXTERNAL_URL", "https://domieface.onrender.com")
	t.Setenv("SELF_PING_URL", "https://status.example.com/ping")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "https://status.example.com/ping"; cfg.SelfPing.URL != want {
		t.Errorf("self-ping URL: got %q, want the explicit override %q", cfg.SelfPing.URL, want)
	}
}

func TestSelfPingFallsBackToTheLocalListener(t *testing.T) {
	production(t)
	t.Setenv("PORT", "9999")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "http://localhost:9999/healthz"; cfg.SelfPing.URL != want {
		t.Errorf("self-ping URL: got %q, want %q", cfg.SelfPing.URL, want)
	}
}

// Render supplies PORT; binding anything else means the platform health check
// never reaches the service.
func TestPortIsTakenFromTheEnvironment(t *testing.T) {
	production(t)
	t.Setenv("PORT", "10000")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":10000" {
		t.Errorf("Addr: got %q, want :10000", cfg.Addr)
	}
}

func TestPublicBaseURLTrailingSlashIsTrimmed(t *testing.T) {
	production(t)
	t.Setenv("S3_PUBLIC_BASE_URL", "https://cdn.example.com/images/")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Otherwise every generated URL contains a double slash.
	if want := "https://cdn.example.com/images"; cfg.Storage.PublicBaseURL != want {
		t.Errorf("PublicBaseURL: got %q, want %q", cfg.Storage.PublicBaseURL, want)
	}
}

func TestInvalidDurationIsReported(t *testing.T) {
	production(t)
	t.Setenv("ACCESS_TOKEN_TTL", "fifteen minutes")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load accepted an unparseable duration")
	}
	if !strings.Contains(err.Error(), "ACCESS_TOKEN_TTL") {
		t.Errorf("the error should name the variable, got: %v", err)
	}
}
