// Package keepalive periodically requests the server's own health endpoint.
//
// What this does and does not achieve is worth being precise about.
//
// It DOES keep the process and its connection pools warm: the request runs a
// database ping, so idle Postgres connections are exercised rather than being
// dropped by an idle timeout and reconnected on the first real user request.
//
// It does NOT stop a platform-as-a-service idling the instance. Those platforms
// decide based on traffic arriving at their edge router, and a request the
// container makes to its own loopback address never reaches it. To keep such a
// host awake, point SELF_PING_URL at the service's public health URL so the
// request goes out and back through the router.
package keepalive

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"domieface/com/internal/config"
)

// Pinger requests a URL on an interval until its context is cancelled.
type Pinger struct {
	url      string
	interval time.Duration
	client   *http.Client
}

// New builds a pinger from configuration.
func New(cfg config.SelfPingConfig) *Pinger {
	return &Pinger{
		url:      cfg.URL,
		interval: cfg.Interval,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// No redirect following: a health endpoint that redirects is a
			// misconfiguration, and quietly chasing it hides that.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Run pings on a ticker until ctx is cancelled.
//
// The first ping waits a full interval rather than firing immediately: at
// startup the listener may not be accepting connections yet, and a failure in
// the first second of every boot is noise, not signal.
func (p *Pinger) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	slog.InfoContext(ctx, "self-ping started", "url", p.url, "interval", p.interval)

	for {
		select {
		case <-ticker.C:
			if err := p.ping(ctx); err != nil {
				// A failed keep-alive is not fatal — it means one ping did not
				// land, and the next one is a tick away.
				slog.WarnContext(ctx, "self-ping failed", "url", p.url, "error", err)
			}
		case <-ctx.Done():
			slog.InfoContext(ctx, "self-ping stopped")
			return
		}
	}
}

func (p *Pinger) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	// Identifies these in the access log, so a spike in traffic is not mistaken
	// for real users.
	req.Header.Set("User-Agent", "domieface-server/keepalive")

	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Drain before closing so the connection returns to the pool for reuse
	// rather than being torn down every ten minutes.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %s", res.Status)
	}

	slog.DebugContext(ctx, "self-ping ok", "url", p.url, "status", res.StatusCode)
	return nil
}
