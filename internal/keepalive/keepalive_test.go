package keepalive_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"domieface/com/internal/config"
	"domieface/com/internal/keepalive"
)

// newCountingServer returns a server that records how many times it was hit and
// what status it replied with.
func newCountingServer(status int) (*httptest.Server, *atomic.Int64, *atomic.Value) {
	var hits atomic.Int64
	var userAgent atomic.Value
	userAgent.Store("")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		userAgent.Store(r.Header.Get("User-Agent"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	return server, &hits, &userAgent
}

func TestPingerCallsTheURLOnItsInterval(t *testing.T) {
	server, hits, userAgent := newCountingServer(http.StatusOK)
	defer server.Close()

	pinger := keepalive.New(config.SelfPingConfig{
		URL:      server.URL + "/healthz",
		Interval: 20 * time.Millisecond,
		Timeout:  time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pinger.Run(ctx) }()

	// Long enough for several intervals without being slow.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	if got := hits.Load(); got < 2 {
		t.Errorf("got %d pings in 150ms at a 20ms interval, want at least 2", got)
	}
	// Identifies keep-alive traffic in the access log so it is not mistaken for
	// real users.
	if got := userAgent.Load().(string); got != "domieface-server/keepalive" {
		t.Errorf("User-Agent: got %q", got)
	}
}

// The first ping waits a full interval, so a slow-starting listener does not
// produce a failure line on every boot.
func TestPingerDoesNotFireImmediately(t *testing.T) {
	server, hits, _ := newCountingServer(http.StatusOK)
	defer server.Close()

	pinger := keepalive.New(config.SelfPingConfig{
		URL:      server.URL + "/healthz",
		Interval: time.Hour,
		Timeout:  time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pinger.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := hits.Load(); got != 0 {
		t.Errorf("got %d pings before the first interval elapsed, want 0", got)
	}
}

// A keep-alive that cannot reach its target must not take the server down with
// it; it should log and carry on to the next tick.
func TestPingerSurvivesFailures(t *testing.T) {
	server, hits, _ := newCountingServer(http.StatusInternalServerError)
	defer server.Close()

	pinger := keepalive.New(config.SelfPingConfig{
		URL:      server.URL + "/healthz",
		Interval: 20 * time.Millisecond,
		Timeout:  time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pinger.Run(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run stopped after a failing ping instead of carrying on")
	}

	if got := hits.Load(); got < 2 {
		t.Errorf("got %d attempts, want the pinger to keep trying after a 500", got)
	}
}

func TestPingerStopsWhenContextIsCancelled(t *testing.T) {
	server, hits, _ := newCountingServer(http.StatusOK)
	defer server.Close()

	pinger := keepalive.New(config.SelfPingConfig{
		URL:      server.URL + "/healthz",
		Interval: 10 * time.Millisecond,
		Timeout:  time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pinger.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	settled := hits.Load()
	time.Sleep(50 * time.Millisecond)

	if got := hits.Load(); got != settled {
		t.Errorf("pings continued after shutdown: %d then %d", settled, got)
	}
}
