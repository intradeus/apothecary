package httpapi

// The API surface, declared once.
//
// Every endpoint appears here with what serves it AND what it looks like from
// outside. Handler() builds the mux from this table and openapi.go reflects
// over the same table, so the specification cannot describe an endpoint the
// server does not have, or miss one it does. That is the whole reason the
// table exists: a hand-written spec beside a hand-written mux is two truths,
// and the second one to be edited is always the one that rots.
//
// ID is the operationId, which is also the name the generated frontend client
// gives the function — so read them as the client's API, not as internal
// labels.

import (
	"net/http"
	"slices"

	"apothecary/internal/ports/envport"
	"apothecary/internal/project/tickets"
)

// Param is one query parameter an endpoint reads.
type Param struct {
	Name string
	Doc  string
	// Type is the OpenAPI scalar: "string" (default) or "integer".
	Type string
	Enum []string
}

// Route is one endpoint.
type Route struct {
	Method  string // GET|POST|PUT|DELETE
	Path    string // net/http mux pattern, e.g. /api/runs/{id}
	ID      string // operationId, and the generated client's function name
	Tag     string // groups the endpoint in the docs and the client
	Summary string
	Doc     string // longer prose, when the summary cannot carry it

	Handler http.HandlerFunc
	// Enabled gates REGISTRATION, never the specification: a server wired
	// without the config plane serves fewer routes, but the API it documents
	// is still the API.
	Enabled func(*Server) bool

	Query []Param
	// Body / Returns are zero values of the request and response types;
	// openapi.go reflects their Go types into schemas.
	Body    any
	Returns any
	Status  int // success status; 0 means 200 (or 204 when Returns is nil)

	// Accepts / Produces override application/json. Set for the endpoints
	// that move bytes rather than documents.
	Accepts  string
	Produces string
	// Auth is how the endpoint is reached (auth.go): a signed-in user by
	// default, the instance admin, the in-workspace shim's run token, or
	// anyone. Scope names the resource the path's id must belong to the
	// caller through; a user route with an id and no scope is refused by
	// TestEveryRouteDeclaresItsAccess.
	Auth  Access
	Scope Scope
}

// conf / kinds / cpui are the registration gates.
func haveConf(s *Server) bool      { return s.Conf != nil }
func haveSkills(s *Server) bool    { return s.Skills != nil }
func haveAnalytics(s *Server) bool { return s.Analytics != nil }
func haveNotices(s *Server) bool   { return s.Notices != nil }
func haveKinds(s *Server) bool     { return s.Kinds != nil }
func haveCPUI(s *Server) bool      { return s.CPUIDir != "" }

const (
	tagRuns         = "runs"
	tagWorkspace    = "workspace"
	tagEgress       = "egress"
	tagPings        = "pings"
	tagFiles        = "files"
	tagTickets      = "tickets"
	tagProjects     = "projects"
	tagIntegrations = "integrations"
	tagFlows        = "flows"
	tagAutomations  = "automations"
	tagSkills       = "skills"
	tagChannels     = "channels"
	tagSystem       = "system"
	tagAuth         = "auth"
	tagUsers        = "users"
	tagAnalytics    = "analytics"
	tagNotices      = "notices"
)

var (
	auditQuery = []Param{
		{Name: "verdict", Doc: "allow|deny", Enum: []string{"allow", "deny"}},
		{Name: "host", Doc: "substring match on the host"},
		{Name: "limit", Doc: "max rows (default 200)", Type: "integer"},
	}
	tailQuery = []Param{{Name: "n", Doc: "bytes of tail to return (default 64 KiB)", Type: "integer"}}
)

// routes is the table, area by area in this order (the spec lists them so).
// Handler() and Spec() are its only two readers; each area's routes live in
// the file with its handlers.
func (s *Server) routes() []Route {
	return slices.Concat(
		s.authRoutes(),
		s.userRoutes(),
		s.runRoutes(),
		s.egressRoutes(),
		s.workspaceRoutes(),
		s.serviceRoutes(),
		s.authBrokerRoutes(),
		s.pingRoutes(),
		s.fileRoutes(),
		s.channelRoutes(),
		s.noticeRoutes(),
		s.registryRoutes(),
		s.integrationRoutes(),
		s.credentialRoutes(),
		s.skillRoutes(),
		s.projectRoutes(),
		s.ticketRoutes(),
		s.flowRoutes(),
		s.analyticsRoutes(),
		s.healthRoutes(),
	)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		if rt.Enabled != nil && !rt.Enabled(s) {
			continue
		}
		mux.Handle(rt.Method+" "+rt.Path, s.traced(rt, s.guard(rt)))
	}
	// A WebSocket, so outside the table (and the spec): see serveTerminal.
	// The run's owner only, from the UI's own origin.
	mux.HandleFunc("GET /api/runs/{id}/terminal", s.guard(Route{Scope: ScopeRun, Handler: func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOriginUpgrade(w, r) {
			return
		}
		s.serveTerminal(w, r)
	}}))
	// The workspace's browser MCP stream, upgraded likewise: see
	// serveBrowserMCP (it checks the run token itself, before the upgrade).
	mux.HandleFunc("GET /api/runs/{id}/browser-mcp", s.serveBrowserMCP)
	// The document and its viewer are public: an API an engine documents is
	// not a secret, and the page needs them before anyone signs in.
	mux.HandleFunc("GET /api/openapi.json", s.getOpenAPIJSON)
	mux.HandleFunc("GET /api/openapi.yaml", s.getOpenAPIYAML)
	mux.HandleFunc("GET /docs", s.serveDocs)
	mux.HandleFunc("GET /docs/", s.serveDocs)
	if haveCPUI(s) {
		mux.HandleFunc("GET /", s.serveCPUI)
	}
	if s.Apps != nil {
		// The sign-in hand-off to an app host (apps.go), on the main host.
		mux.HandleFunc("GET /api/apps/auth", s.Apps.authorize(s))
		return s.edge(s.Apps.wrap(s, mux))
	}
	return s.edge(mux)
}

// Compile-time anchors for types the table names only through helpers.
var (
	_ = tickets.Sources(nil)
	_ = envport.ObservedPort{}
)
