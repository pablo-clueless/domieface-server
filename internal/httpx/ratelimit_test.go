package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"domieface/com/internal/httpx"
)

func TestRateLimiterAllowsUpToBurstThenRefuses(t *testing.T) {
	limiter := httpx.NewRateLimiter(60, 3)

	for i := range 3 {
		if allowed, _ := limiter.Allow("client"); !allowed {
			t.Fatalf("request %d was refused inside the burst allowance", i+1)
		}
	}

	allowed, retryAfter := limiter.Allow("client")
	if allowed {
		t.Fatal("the request past the burst allowance should have been refused")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter: got %v, want a positive duration", retryAfter)
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	limiter := httpx.NewRateLimiter(60, 1)

	if allowed, _ := limiter.Allow("first"); !allowed {
		t.Fatal("first client was refused its only token")
	}
	if allowed, _ := limiter.Allow("second"); !allowed {
		t.Fatal("second client was refused because of the first client's usage")
	}
}

// The contract requires 429 responses to carry Retry-After so the client knows
// how long to back off.
func TestRateLimitMiddlewareSetsRetryAfterAndErrorCode(t *testing.T) {
	limiter := httpx.NewRateLimiter(60, 1)
	handler := limiter.Middleware(func(*http.Request) string { return "client" })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/posts", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/v1/posts", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", second.Code)
	}

	retryAfter := second.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After header was not set on the 429")
	}
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds < 1 {
		t.Errorf("Retry-After: got %q, want a positive whole number of seconds", retryAfter)
	}

	if got := second.Body.String(); !strings.Contains(got, httpx.CodeRateLimited) {
		t.Errorf("body %s did not carry the %s code", got, httpx.CodeRateLimited)
	}
}

func TestClientIPIgnoresForwardedHeaderWhenProxyIsNotTrusted(t *testing.T) {
	// Trusting X-Forwarded-For unconditionally would let any caller forge a
	// fresh identity per request and walk straight past the rate limiter.
	r := httptest.NewRequest(http.MethodGet, "/v1/posts", nil)
	r.RemoteAddr = "203.0.113.7:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")

	if got := httpx.ClientIP(r, false); got != "203.0.113.7" {
		t.Errorf("untrusted: got %q, want the socket address 203.0.113.7", got)
	}
	if got := httpx.ClientIP(r, true); got != "198.51.100.1" {
		t.Errorf("trusted: got %q, want the forwarded address 198.51.100.1", got)
	}
}
