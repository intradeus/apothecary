package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Health is the probes' answer. Liveness says only that the process
// serves HTTP; readiness also says whether it should take traffic: not
// while it drains for shutdown, nor while its database is unreachable
// (every route but sign-in needs it).
type Health struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ObservabilityInfo is where the admin's platform dashboards live.
type ObservabilityInfo struct {
	GrafanaURL string `json:"grafana_url"`
}

// readyzDBTimeout bounds the readiness probe's database round trip, under
// the orchestrator's probe timeout so a hung pool reads as not ready
// rather than as a probe timeout.
const readyzDBTimeout = 2 * time.Second

// healthRoutes are the probes and the admin's pointer at the platform
// dashboards. The probes are public: an orchestrator has no account.
func (s *Server) healthRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/healthz", ID: "getHealthz", Tag: tagSystem,
			Summary: "Liveness: the process serves HTTP",
			Handler: s.getHealthz, Returns: Health{}, Auth: AccessPublic},
		{Method: "GET", Path: "/readyz", ID: "getReadyz", Tag: tagSystem,
			Summary: "Readiness: not draining, and the database answers",
			Doc:     "503 with status `draining` once shutdown begins, or `db` when the database does not answer within 2s. The error says only `database unreachable` or `database timeout` (the probe is public); the cause is in the server log.",
			Handler: s.getReadyz, Returns: Health{}, Auth: AccessPublic},
		{Method: "GET", Path: "/api/observability", ID: "getObservability", Tag: tagSystem,
			Summary: "Where the platform dashboards live (admin)",
			Doc:     "`grafana_url` is APOTHECARY_GRAFANA_URL; empty when not configured.",
			Handler: s.getObservability, Returns: ObservabilityInfo{}, Auth: AccessAdmin},
	}
}

func (s *Server) getHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: "ok"})
}

func (s *Server) getReadyz(w http.ResponseWriter, r *http.Request) {
	select {
	case <-s.draining():
		writeJSON(w, http.StatusServiceUnavailable, Health{Status: "draining"})
		return
	default:
	}
	if s.DB != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyzDBTimeout)
		defer cancel()
		if err := s.DB.Ping(ctx); err != nil {
			// The probe is public: the driver's error names the DSN's user,
			// host and port, so it stays in the log.
			msg := "database unreachable"
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "database timeout"
			}
			s.logger().WarnContext(r.Context(), "readiness: database ping failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, Health{Status: "db", Error: msg})
			return
		}
	}
	writeJSON(w, http.StatusOK, Health{Status: "ok"})
}

func (s *Server) getObservability(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, ObservabilityInfo{GrafanaURL: s.GrafanaURL})
}
