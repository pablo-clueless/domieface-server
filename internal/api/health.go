package api

import (
	"context"
	"net/http"
	"time"

	"domieface/com/internal/buildinfo"
	"domieface/com/internal/httpx"
	"domieface/com/internal/jsontime"
)

// Health status values. Only these two are ever returned, so a probe can match
// on them exactly.
const (
	// StatusOK means every dependency answered.
	StatusOK = "ok"
	// StatusDegraded means at least one dependency did not.
	StatusDegraded = "degraded"
)

// checkTimeout bounds each dependency check. A readiness probe that hangs is
// worse than one that fails: the orchestrator waits on it, and an instance that
// cannot answer promptly cannot serve traffic promptly either.
const checkTimeout = 2 * time.Second

// healthResponse is the body of both health endpoints.
//
// Deliberately more than a status word. During an incident these are the fields
// you want without shelling into the box: which build is running, how long it
// has been up (a number that keeps resetting means it is crash-looping), and
// which dependency is the one that is broken.
type healthResponse struct {
	Status        string        `json:"status"`
	Service       string        `json:"service"`
	Version       string        `json:"version"`
	Revision      string        `json:"revision,omitempty"`
	GoVersion     string        `json:"goVersion"`
	Environment   string        `json:"environment"`
	Time          jsontime.Time `json:"time"`
	UptimeSeconds int64         `json:"uptimeSeconds"`

	// Checks is present on readiness only. Liveness deliberately checks
	// nothing, so it has nothing to report.
	Checks []healthCheck `json:"checks,omitempty"`
}

// healthCheck is one dependency's verdict.
type healthCheck struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
	// Error is the failure detail, omitted when the check passed. Dependency
	// errors are operational detail rather than anything user-facing, and these
	// endpoints are not authenticated — so it says what failed, not where.
	Error string `json:"error,omitempty"`
}

// serviceName identifies this service in health responses, for a load balancer
// or dashboard aggregating several.
const serviceName = "domieface-server"

// newHealthResponse fills in the fields both endpoints share.
func (s *Server) newHealthResponse() healthResponse {
	return healthResponse{
		Status:        StatusOK,
		Service:       serviceName,
		Version:       buildinfo.Version(),
		Revision:      buildinfo.Revision(),
		GoVersion:     buildinfo.GoVersion(),
		Environment:   s.cfg.Environment,
		Time:          jsontime.Now(),
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
	}
}

// handleHealth implements GET /healthz — liveness.
//
// It touches no dependency on purpose. Liveness answers "is this process
// wedged, should you restart it". Failing it because Postgres is briefly
// unreachable would have the orchestrator kill healthy instances during a
// database blip, turning a recoverable outage into a restart storm. That
// question belongs to readiness, below.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, s.newHealthResponse())
	return nil
}

// handleReady implements GET /readyz — readiness.
//
// Reports whether this instance can serve traffic, so a rolling deploy does not
// route to one that cannot. Returns 503 when any check fails, because that
// status is what actually takes the instance out of rotation; the body explains
// which dependency was at fault.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) error {
	response := s.newHealthResponse()
	response.Checks = []healthCheck{s.checkDatabase(r.Context())}

	status := http.StatusOK
	for _, check := range response.Checks {
		if check.Status != StatusOK {
			response.Status = StatusDegraded
			status = http.StatusServiceUnavailable
			break
		}
	}

	httpx.WriteJSON(w, status, response)
	return nil
}

// checkDatabase pings Postgres and times it. The latency is worth reporting on
// its own: a check that passes in 1900ms is a database about to start failing.
//
// Object storage is deliberately not checked here. Uploads need it, but the
// feed and every read path do not, so a storage blip should not pull the whole
// instance out of rotation. Add it here if you would rather it did.
func (s *Server) checkDatabase(ctx context.Context) healthCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	start := time.Now()
	err := s.store.Ping(ctx)
	check := healthCheck{
		Name:      "database",
		Status:    StatusOK,
		LatencyMs: time.Since(start).Milliseconds(),
	}

	if err != nil {
		check.Status = StatusDegraded
		check.Error = "unreachable"
	}
	return check
}
