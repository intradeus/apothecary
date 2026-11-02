// Package httpapi is the engine's HTTP surface (DESIGN.md §8): stdlib
// net/http, hand-rolled SSE. Every route is reached as a signed-in user
// unless its table entry says otherwise (auth.go): the instance admin, the
// in-workspace shim's per-run bearer token, or anyone for sign-in itself.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/logins"
	"apothecary/internal/analytics"
	"apothecary/internal/engine"
	"apothecary/internal/engine/ask"
	"apothecary/internal/engine/authsink"
	"apothecary/internal/engine/browsermcp"
	"apothecary/internal/engine/egress"
	"apothecary/internal/engine/mrs"
	"apothecary/internal/notices"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/project/channels"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run/event"
	"apothecary/internal/skills/skillstore"
	"apothecary/internal/workspace/envcheck"
	"apothecary/internal/workspace/envstate"
	"apothecary/internal/workspace/resources"
	"apothecary/internal/workspace/runports"
	"apothecary/internal/workspace/services"
	"apothecary/internal/workspace/wsinfo"
)

type Server struct {
	// Runs is the engine: the run lifecycle, pings, holds and messages.
	Runs     *engine.Engine
	Store    event.Store
	Registry *channels.Registry
	RunsDir  string
	// Conf is the config-plane store (DESIGN2 §10); nil disables those
	// routes (bare test servers).
	Conf *confstore.Service
	// Kinds serves GET /api/kinds descriptors.
	Kinds *kinds.Registry
	// Ops runs integration tests / repo listing (the resolver); nil = 503.
	Ops IntegrationOps
	// Sources resolves ticketsource integrations for the ticket factory (the
	// resolver again); nil = importing is off and the routes say so.
	Sources tickets.Sources
	// Trackers resolves a ticket's tracker (the resolver: its remote issue's
	// writer, else the internal tracker) for the ticket page's status,
	// label and comment writes; nil = those routes answer 503.
	Trackers TicketTrackers
	// Syncer re-mirrors tickets from their remote trackers (the refresh
	// routes, and after a write to a remote tracker); nil = the refresh
	// routes answer 503 and a remote write waits for the poll.
	Syncer *tickets.Syncer
	// Pings serves the durable request/response projection (PLAN4 M15);
	// nil falls back to the engine's in-memory pending registry.
	Pings PingStore
	// Attachments/Snapshots are the file stores (DESIGN-FILES): human uploads
	// and the engine-captured record. nil disables the respective routes
	// (uploads → 503, lists → empty).
	Attachments *filestore.Attachments
	Snapshots   *filestore.Snapshots
	// Skills is the skill library (TODO 38); nil disables its routes.
	// SkillImports brings public folders in and refreshes them; nil = the
	// import and refresh routes answer 503.
	Skills       *skillstore.Store
	SkillImports *skillstore.Importer
	// CPUIDir, when set, is served at / with an SPA fallback to index.html
	// (the built SvelteKit bundle; the CPUI talks only to this API).
	CPUIDir string
	// Auth is who a request is (TODO 25): sessions, tokens, accounts. nil
	// answers every user route 503 — a server without it is misconfigured,
	// never open.
	Auth *auth.Service
	// SSH is the engine's SSH door (TODO 46), for the run page's connection
	// facts and for ending a disabled account's sessions; nil = no door.
	SSH SSHAccess
	// Limits are the instance's capacity caps: read by every user, set by
	// an admin. nil = the engine defaults, read-only (bare test servers).
	Limits LimitsSetting
	// HoldTimeout is the ping long-poll hold (§8: 55 s).
	HoldTimeout time.Duration
	Log         *slog.Logger
	// Apps serves host-routed exposed ports (apps.go); nil = exposures are
	// loopback listeners the engine binds itself.
	Apps *Apps
	// Keeper refreshes provider logins (a credential's Test refreshes a due
	// one); SignIns runs sign-in flows. nil: tests open credentials as
	// stored, and signing in answers 503.
	Keeper  *logins.Keeper
	SignIns *logins.SignIns
	// MRs reads merge requests from the projects' repository hosts.
	MRs *mrs.Service
	// Resources serves the run-page resource graphs.
	Resources *resources.Sampler
	// EnvState is the workspace badges: reconciled container status and the
	// engine's in-flight workspace ops (the same store the engine claims).
	EnvState *envstate.Store
	// Workspaces reaches run workspaces: checkouts and the sidecar, the live
	// log, SSH facts, the build cache.
	Workspaces *wsinfo.Service
	// EnvCheck runs workspace compliance checks for projects.
	EnvCheck *envcheck.Registry
	// Egress is the live egress controls and the audit trail.
	Egress *egress.Service
	// Ask runs the runs' Ask panes.
	Ask *ask.Service
	// Analytics answers the run-analytics queries and keeps the person's
	// dashboards and scorecards (TODO 23); nil disables its routes.
	Analytics *analytics.Service
	// Notices is the feed of setup problems a person must act on (TODO 51);
	// nil disables its routes.
	Notices *notices.Service
	// AuthSink serves the per-run auth brokers (the same sink the engine
	// registers live workspaces with).
	AuthSink *authsink.Sink
	// Ports exposes run ports and lists the discovered ones.
	Ports *runports.Service
	// Browser opens the agents' browser sessions and reports the project
	// browser's state.
	Browser *browsermcp.Service
	// Services is the run page's service start/stop/log.
	Services *services.Control
	// DB answers the readiness probe (GET /readyz); nil = ready whenever
	// the server is not draining (bare test servers have no database).
	DB interface{ Ping(context.Context) error }
	// GrafanaURL is where the admin's System hub links for the platform
	// dashboards (APOTHECARY_GRAFANA_URL); "" = not configured.
	GrafanaURL string

	logins loginLimiter

	// drain is closed by Drain: the long-lived responses (event streams,
	// ask_human holds) end on it.
	drainInit, drainOnce sync.Once
	drain                chan struct{}
}

// Drain ends the server's long-lived responses — run and Ask event streams
// end (a browser reconnects and resumes from its last event id), ask_human
// holds answer 204 (the shim re-polls) — so a graceful shutdown need not
// wait them out. Register it with http.Server.RegisterOnShutdown.
func (s *Server) Drain() {
	s.draining()
	s.drainOnce.Do(func() { close(s.drain) })
}

// draining is closed once the server starts shutting down.
func (s *Server) draining() <-chan struct{} {
	s.drainInit.Do(func() { s.drain = make(chan struct{}) })
	return s.drain
}

func New(e *engine.Engine, store event.Store, reg *channels.Registry, runsDir string) *Server {
	return &Server{
		Runs: e, Store: store, Registry: reg, RunsDir: runsDir,
		HoldTimeout: 55 * time.Second,
		Log:         slog.Default(),
	}
}

// serveCPUI serves the built CPUI bundle with an SPA fallback: files that
// exist are served as-is, everything else gets index.html so client-side
// routes deep-link correctly.
func (s *Server) serveCPUI(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(s.CPUIDir, filepath.Clean("/"+r.URL.Path))
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.CPUIDir, "index.html"))
}

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// runEvents loads a run's full event history, or 404s.
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request, id string) ([]event.Event, bool) {
	evs, err := s.Store.Events(r.Context(), id, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if len(evs) == 0 {
		writeErr(w, http.StatusNotFound, "run "+id+" not found")
		return nil, false
	}
	return evs, true
}

// SSHAccess is what the API needs from the SSH door (internal/workspace/sshdoor).
type SSHAccess interface {
	Address() (host string, port int)
	Fingerprint() string
	HostPublicKey() string
	DropUser(userID string)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func containsStr(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func removeStr(ss []string, v string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != v {
			out = append(out, s)
		}
	}
	return out
}
