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

	"apothecary/internal/actionspec"
	"apothecary/internal/channels"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/filestore"
	"apothecary/internal/kinds"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/signalspec"
	"apothecary/internal/tickets"
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
	// Auth marks the endpoints authenticated by a per-run bearer token
	// (the in-workspace shim's, not a user's).
	RunToken bool
}

// conf / kinds / cpui are the registration gates.
func haveConf(s *Server) bool  { return s.Conf != nil }
func haveKinds(s *Server) bool { return s.Kinds != nil }
func haveCPUI(s *Server) bool  { return s.CPUIDir != "" }

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
	tagChannels     = "channels"
	tagSystem       = "system"
)

var (
	auditQuery = []Param{
		{Name: "verdict", Doc: "allow|deny", Enum: []string{"allow", "deny"}},
		{Name: "host", Doc: "substring match on the host"},
		{Name: "limit", Doc: "max rows (default 200)", Type: "integer"},
	}
	tailQuery = []Param{{Name: "n", Doc: "bytes of tail to return (default 64 KiB)", Type: "integer"}}
)

// routes is the table. Handler() and Spec() are its only two readers.
func (s *Server) routes() []Route {
	return []Route{
		// --- runs -----------------------------------------------------------
		{Method: "GET", Path: "/api/runs", ID: "listRuns", Tag: tagRuns,
			Summary: "List runs, newest first",
			Query: []Param{
				{Name: "project", Doc: "only runs of this project"},
				{Name: "ticket", Doc: "only runs of this ticket"},
			},
			Handler: s.listRuns, Returns: []run.View{}},
		{Method: "GET", Path: "/api/runs/{id}", ID: "getRun", Tag: tagRuns,
			Summary: "One run with the tail of its event log",
			Handler: s.getRun, Returns: RunDetail{}},
		{Method: "GET", Path: "/api/runs/{id}/events", ID: "streamRunEvents", Tag: tagRuns,
			Summary: "Server-sent event stream of a run's events",
			Doc:     "Replays from after_seq, then streams live. The stream ends when the run reaches a terminal state.",
			Query:   []Param{{Name: "after_seq", Doc: "resume after this sequence number", Type: "integer"}},
			Handler: s.streamEvents, Produces: "text/event-stream"},
		{Method: "GET", Path: "/api/runs/{id}/logs/{stage}", ID: "getStageLog", Tag: tagRuns,
			Summary: "A stage's captured log (latest attempt)",
			Query:   tailQuery, Handler: s.getLog, Produces: "text/plain"},
		{Method: "GET", Path: "/api/runs/{id}/workspace-log", ID: "getWorkspaceLog", Tag: tagWorkspace,
			Summary: "Live tail of the workspace's exec log",
			Query:   tailQuery, Handler: s.getWorkspaceLog, Produces: "text/plain"},

		{Method: "POST", Path: "/api/runs/{id}/abort", ID: "abortRun", Tag: tagRuns,
			Summary: "Stop a run for good", Handler: s.postAbort, Status: http.StatusAccepted},
		{Method: "POST", Path: "/api/runs/{id}/pause", ID: "pauseRun", Tag: tagRuns,
			Summary: "Park a run; nothing moves until play",
			Doc:     "Works on a run that is parked waiting for an answer too: pausing one says it must not pick up when the answer lands.",
			Handler: s.postPause, Status: http.StatusAccepted},
		{Method: "POST", Path: "/api/runs/{id}/play", ID: "playRun", Tag: tagRuns,
			Summary: "Continue a paused run where it left off",
			Handler: s.postPlay, Status: http.StatusAccepted},
		{Method: "POST", Path: "/api/runs/{id}/archive", ID: "archiveRun", Tag: tagRuns,
			Summary: "Archive a finished run and destroy its workspace",
			Handler: s.postArchive, Status: http.StatusAccepted},
		{Method: "POST", Path: "/api/runs/{id}/resume", ID: "resumeRun", Tag: tagRuns,
			Summary: "Try again: re-enter the failed stage in the same workspace",
			Handler: s.postResume, Status: http.StatusAccepted},
		{Method: "POST", Path: "/api/runs/{id}/restart", ID: "restartRun", Tag: tagRuns,
			Summary: "Run the ticket again as a new run in a fresh workspace",
			Handler: s.postRestart, Body: RestartRunRequest{}, Returns: RunRef{}, Status: http.StatusCreated},
		{Method: "POST", Path: "/api/runs/{id}/rebuild", ID: "rebuildWorkspace", Tag: tagWorkspace,
			Summary: "Recreate the run's container in place, keeping the checkout",
			Doc:     "no_cache rebuilds the image from scratch — for the staleness a cache key cannot see, like a floating base tag that moved.",
			Handler: s.postRebuild, Body: RebuildRequest{}, Status: http.StatusAccepted},

		// --- egress ---------------------------------------------------------
		{Method: "GET", Path: "/api/egress-defaults", ID: "getEgressDefaults", Tag: tagEgress,
			Summary: "The built-in allowlist every project starts from",
			Handler: s.getEgressDefaults, Returns: EgressDefaults{}},
		{Method: "GET", Path: "/api/runs/{id}/egress-denials", ID: "listEgressDenials", Tag: tagEgress,
			Summary: "Hosts the gateway blocked for this run",
			Handler: s.getEgressDenials, Returns: []ports.EgressDenial{}},
		{Method: "POST", Path: "/api/runs/{id}/egress-allow", ID: "allowEgressHost", Tag: tagEgress,
			Summary: "Unblock a host for a live run and persist it to the project",
			Handler: s.postEgressAllow, Body: EgressHostRequest{}, Returns: EgressAllowResult{}},
		{Method: "POST", Path: "/api/runs/{id}/egress-rule", ID: "setEgressRule", Tag: tagEgress,
			Summary: "Allow or force-block a host for a run",
			Handler: s.postEgressRule, Body: EgressRuleRequest{}, Returns: EgressRuleResult{}},
		{Method: "GET", Path: "/api/runs/{id}/egress-audit", ID: "getRunEgressAudit", Tag: tagEgress,
			Summary: "Everything this run's workspace reached or was blocked from",
			Query:   auditQuery, Handler: s.getRunEgressAudit, Returns: []ports.EgressAuditRow{}},
		{Method: "GET", Path: "/api/runs/{id}/egress-policy", ID: "getRunEgressPolicy", Tag: tagEgress,
			Summary: "The firewall policy in force for a run right now",
			Handler: s.getRunEgressPolicy, Returns: EgressPolicy{}},

		// --- workspace ports / editor / resources ---------------------------
		{Method: "GET", Path: "/api/runs/{id}/ports", ID: "getRunPorts", Tag: tagWorkspace,
			Summary: "Exposed and newly observed workspace ports",
			Handler: s.getRunPorts, Returns: RunPorts{}},
		{Method: "POST", Path: "/api/runs/{id}/expose-port", ID: "exposeRunPort", Tag: tagWorkspace,
			Summary: "Publish a workspace port and get its browsable URL",
			Handler: s.postExposePort, Body: ExposePortRequest{}, Returns: ExposedPort{}},
		{Method: "POST", Path: "/api/runs/{id}/observed-ports", ID: "reportObservedPorts", Tag: tagWorkspace,
			Summary: "Report listening ports (in-workspace scanner)",
			Handler: s.postObservedPorts, Body: ObservedPortsRequest{}, Status: http.StatusNoContent,
			RunToken: true},
		{Method: "POST", Path: "/api/runs/{id}/open-editor", ID: "openEditor", Tag: tagWorkspace,
			Summary: "Start code-server in the workspace and get its URL",
			Handler: s.postOpenEditor, Returns: EditorURL{}},
		{Method: "GET", Path: "/api/runs/{id}/resources", ID: "getRunResources", Tag: tagWorkspace,
			Summary: "Rolling window of workspace cpu/memory/network samples",
			Handler: s.getRunResources, Returns: RunResources{}},

		// --- pings ----------------------------------------------------------
		{Method: "POST", Path: "/api/runs/{id}/pings", ID: "raisePing", Tag: tagPings,
			Summary: "Ask the human a question (ask_human)",
			Doc:     "Holds for hold_ms and answers in-band when it can; 204 means nobody answered in time and the caller should end its turn.",
			Handler: s.postPing, Body: RaisePingRequest{}, Returns: PingHold{},
			RunToken: true},
		{Method: "GET", Path: "/api/runs/{id}/pings/{pid}", ID: "repollPing", Tag: tagPings,
			Summary: "Re-poll a raised question for its answer",
			Handler: s.repollPing, Returns: PingHold{},
			RunToken: true},
		{Method: "GET", Path: "/api/pings", ID: "listPings", Tag: tagPings,
			Summary: "The inbox: questions waiting on a human",
			Query: []Param{
				{Name: "status", Doc: "pending|answered|all (default pending)", Enum: []string{"pending", "answered", "all"}},
				{Name: "limit", Doc: "max rows", Type: "integer"},
			},
			Handler: s.listPings, Returns: []run.PingView{}},
		{Method: "GET", Path: "/api/pings/{pid}", ID: "getPing", Tag: tagPings,
			Summary: "One question with its answer, if it has one",
			Handler: s.getStoredPing, Returns: run.PingView{}},
		{Method: "GET", Path: "/api/pings/{pid}/files", ID: "getPingFiles", Tag: tagPings,
			Summary: "The files a question attached, as captured when it was raised",
			Handler: s.getPingFiles, Returns: PingFiles{}},
		{Method: "POST", Path: "/api/pings/{pid}/answer", ID: "answerPing", Tag: tagPings,
			Summary: "Answer a question",
			Handler: s.answerPing, Body: AnswerPingRequest{}, Returns: AnswerAccepted{}},

		// --- files ----------------------------------------------------------
		{Method: "POST", Path: "/api/attachments", ID: "uploadAttachments", Tag: tagFiles,
			Summary: "Upload files to attach to a ticket, a goal or an answer",
			Handler: s.postAttachments, Returns: Uploaded{}, Status: http.StatusCreated,
			Accepts: "multipart/form-data"},
		{Method: "GET", Path: "/api/attachments/limits", ID: "getAttachmentLimits", Tag: tagFiles,
			Summary: "Upload policy: size cap and accepted types",
			Handler: s.getAttachmentLimits, Returns: filestore.UploadLimits{}},
		{Method: "GET", Path: "/api/attachments/{id}", ID: "downloadAttachment", Tag: tagFiles,
			Summary: "Download one uploaded file",
			Handler: s.getAttachment, Produces: "application/octet-stream"},
		{Method: "GET", Path: "/api/snapshots/{id}", ID: "downloadSnapshot", Tag: tagFiles,
			Summary: "Download one engine-captured record",
			Handler: s.getSnapshot, Produces: "application/octet-stream"},
		{Method: "GET", Path: "/api/runs/{id}/files", ID: "getRunFiles", Tag: tagFiles,
			Summary: "Everything filed under a run: human uploads and engine captures",
			Handler: s.getRunFiles, Returns: RunFiles{}},

		// --- channels -------------------------------------------------------
		{Method: "GET", Path: "/api/channels", ID: "listChannels", Tag: tagChannels,
			Summary: "Configured notification channels (secrets masked)",
			Handler: s.listChannels, Returns: []channels.Entry{}},
		{Method: "POST", Path: "/api/channels", ID: "addChannel", Tag: tagChannels,
			Summary: "Add a notification channel",
			Handler: s.addChannel, Body: ChannelRequest{}, Returns: channels.Entry{}, Status: http.StatusCreated},
		{Method: "DELETE", Path: "/api/channels/{id}", ID: "deleteChannel", Tag: tagChannels,
			Summary: "Remove a notification channel",
			Handler: s.deleteChannel, Status: http.StatusNoContent},

		// --- registries -----------------------------------------------------
		{Method: "GET", Path: "/api/kinds", ID: "listKinds", Tag: tagSystem,
			Summary: "Every port kind this build carries, with its config fields",
			Handler: s.listKinds, Returns: []kinds.Descriptor{}, Enabled: haveKinds},
		{Method: "GET", Path: "/api/actions", ID: "listActions", Tag: tagSystem,
			Summary: "Actions an `action` stage may name",
			Handler: s.listActions, Returns: []actionspec.Action{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/signals", ID: "listSignals", Tag: tagSystem,
			Summary: "Signals a `wait` stage or a gate's auto-answer may name",
			Handler: s.listSignals, Returns: []signalspec.Signal{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/settings", ID: "getSettings", Tag: tagSystem,
			Summary: "Engine-wide policy settings",
			Handler: s.getSettings, Returns: confstore.Settings{}, Enabled: haveConf},
		{Method: "PUT", Path: "/api/settings", ID: "updateSettings", Tag: tagSystem,
			Summary: "Replace the engine-wide policy settings",
			Handler: s.putSettings, Body: confstore.Settings{}, Returns: confstore.Settings{}, Enabled: haveConf},

		// --- integrations ---------------------------------------------------
		{Method: "GET", Path: "/api/integrations", ID: "listIntegrations", Tag: tagIntegrations,
			Summary: "Configured integrations (secrets masked)",
			Handler: s.listIntegrations, Returns: []integrationJSON{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/integrations", ID: "createIntegration", Tag: tagIntegrations,
			Summary: "Configure an integration",
			Handler: s.createIntegration, Body: IntegrationRequest{}, Returns: integrationJSON{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/integrations/{id}", ID: "getIntegration", Tag: tagIntegrations,
			Summary: "One integration",
			Handler: s.getIntegration, Returns: integrationJSON{}, Enabled: haveConf},
		{Method: "PUT", Path: "/api/integrations/{id}", ID: "updateIntegration", Tag: tagIntegrations,
			Summary: "Change an integration (secrets merge: present replaces, \"\" deletes, absent keeps)",
			Handler: s.updateIntegration, Body: IntegrationUpdate{}, Returns: integrationJSON{}, Enabled: haveConf},
		{Method: "DELETE", Path: "/api/integrations/{id}", ID: "deleteIntegration", Tag: tagIntegrations,
			Summary: "Delete an integration",
			Handler: s.deleteIntegration, Status: http.StatusNoContent, Enabled: haveConf},
		{Method: "POST", Path: "/api/integrations/{id}/test", ID: "testIntegration", Tag: tagIntegrations,
			Summary: "Check an integration's credentials against the real service",
			Handler: s.testIntegration, Returns: TestResult{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/integrations/{id}/repos", ID: "listIntegrationRepos", Tag: tagIntegrations,
			Summary: "Repositories a forge integration can see",
			Handler: s.listIntegrationRepos, Returns: []kinds.RepoInfo{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/integrations/{id}/models", ID: "listIntegrationModels", Tag: tagIntegrations,
			Summary: "Models a harness integration's credentials can reach",
			Handler: s.listIntegrationModels, Returns: []kinds.ModelInfo{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/integrations/{id}/tickets", ID: "searchSourceTickets", Tag: tagTickets,
			Summary: "Search a tracker for tickets to import (read-only)",
			Query: []Param{
				{Name: "q", Doc: "free-text query"},
				{Name: "limit", Doc: "max results (default 25)", Type: "integer"},
			},
			Handler: s.searchSourceTickets, Returns: []sourceTicketJSON{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/integrations/{id}/tickets/{key}", ID: "getSourceTicket", Tag: tagTickets,
			Summary: "One tracker ticket by key",
			Handler: s.getSourceTicket, Returns: sourceTicketJSON{}, Enabled: haveConf},

		// --- projects -------------------------------------------------------
		{Method: "GET", Path: "/api/projects", ID: "listProjects", Tag: tagProjects,
			Summary: "All projects", Handler: s.listProjects, Returns: []confstore.Project{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/projects", ID: "createProject", Tag: tagProjects,
			Summary: "Create a project",
			Handler: s.createProject, Body: projectBody{}, Returns: confstore.Project{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}", ID: "getProject", Tag: tagProjects,
			Summary: "One project", Handler: s.getProject, Returns: confstore.Project{}, Enabled: haveConf},
		{Method: "PUT", Path: "/api/projects/{id}", ID: "updateProject", Tag: tagProjects,
			Summary: "Replace a project",
			Handler: s.updateProject, Body: projectBody{}, Returns: confstore.Project{}, Enabled: haveConf},
		{Method: "DELETE", Path: "/api/projects/{id}", ID: "deleteProject", Tag: tagProjects,
			Summary: "Delete a project", Handler: s.deleteProject, Status: http.StatusNoContent, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}/branches", ID: "listProjectBranches", Tag: tagProjects,
			Summary: "Branches of the project's repository",
			Handler: s.listProjectBranches, Returns: []kinds.BranchInfo{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}/egress-audit", ID: "getProjectEgressAudit", Tag: tagEgress,
			Summary: "Everything this project's workspaces reached or were blocked from",
			Query:   auditQuery, Handler: s.getProjectEgressAudit, Returns: []ports.EgressAuditRow{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/projects/{id}/preflight", ID: "preflightProject", Tag: tagProjects,
			Summary: "Check a project is ready to run, step by step",
			Handler: s.preflightProject, Returns: PreflightResult{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/projects/{id}/envcheck", ID: "startEnvCheck", Tag: tagProjects,
			Summary: "Prove the project's workspace actually builds (throwaway provision)",
			Handler: s.startEnvCheck, Body: EnvCheckRequest{}, Returns: EnvCheckRef{},
			Status: http.StatusAccepted, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}/build-cache", ID: "getBuildCache", Tag: tagProjects,
			Summary: "What the project's devcontainer build cache holds",
			Doc:     "The cache is per project and outlives its runs — it is what stops every run rebuilding the image into its own throwaway daemon.",
			Handler: s.getBuildCache, Returns: BuildCacheInfo{}, Enabled: haveConf},
		{Method: "DELETE", Path: "/api/projects/{id}/build-cache", ID: "clearBuildCache", Tag: tagProjects,
			Summary: "Empty the project's build cache",
			Doc:     "Safe: the next build repopulates it, so the cost of clearing needlessly is one slow build.",
			Handler: s.deleteBuildCache, Status: http.StatusNoContent, Enabled: haveConf},
		{Method: "GET", Path: "/api/envchecks/{id}", ID: "getEnvCheck", Tag: tagProjects,
			Summary: "Poll a workspace check",
			Handler: s.getEnvCheck, Returns: engine.CheckRun{}, Enabled: haveConf},

		// --- tickets --------------------------------------------------------
		{Method: "GET", Path: "/api/tickets", ID: "listTickets", Tag: tagTickets,
			Summary: "All tickets",
			Query:   []Param{{Name: "project", Doc: "only tickets of this project"}},
			Handler: s.listTickets, Returns: []confstore.Ticket{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/tickets", ID: "createTicket", Tag: tagTickets,
			Summary: "Create a ticket",
			Handler: s.createTicket, Body: ticketBody{}, Returns: ticketResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/tickets/{id}", ID: "getTicket", Tag: tagTickets,
			Summary: "One ticket", Handler: s.getTicket, Returns: ticketResp{}, Enabled: haveConf},
		{Method: "PUT", Path: "/api/tickets/{id}", ID: "updateTicket", Tag: tagTickets,
			Summary: "Replace a ticket",
			Handler: s.updateTicket, Body: ticketBody{}, Returns: ticketResp{}, Enabled: haveConf},
		{Method: "DELETE", Path: "/api/tickets/{id}", ID: "deleteTicket", Tag: tagTickets,
			Summary: "Delete a ticket", Handler: s.deleteTicket, Status: http.StatusNoContent, Enabled: haveConf},
		{Method: "GET", Path: "/api/tickets/{id}/attachments", ID: "getTicketAttachments", Tag: tagTickets,
			Summary: "Files attached to a ticket",
			Handler: s.getTicketAttachments, Returns: AttachmentList{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/tickets/{id}/runs", ID: "startTicketRun", Tag: tagTickets,
			Summary: "Start one attempt on a ticket",
			Handler: s.startTicketRun, Body: StartRunRequest{}, Returns: RunRef{},
			Status: http.StatusCreated, Enabled: haveConf},

		// --- flows ----------------------------------------------------------
		// A flow belongs to a project: its stages name that project's commands
		// and its agents know that repository. The built-ins stay global
		// templates, and copying is how a good flow reaches another project.
		{Method: "GET", Path: "/api/flows", ID: "listAllFlows", Tag: tagFlows,
			Summary: "Every project's flows, for the cross-project overview",
			Handler: s.listFlows, Returns: []flowResp{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/flow-templates", ID: "listFlowTemplates", Tag: tagFlows,
			Summary: "The compiled-in flow templates a project can adopt or copy",
			Handler: s.listBuiltinFlows, Returns: []flowResp{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/flow-templates/{name}/copy", ID: "copyFlowTemplate", Tag: tagFlows,
			Summary: "Copy a compiled-in template into a project",
			Doc:     "A template belongs to no project, so it is copied from here rather than from under one. Copying it under its own name adopts it: the project gets its own editable document, which shadows the template from then on.",
			Handler: s.copyFlowTemplate, Body: CopyFlowRequest{}, Returns: flowResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "POST", Path: "/api/flows/validate", ID: "validateFlow", Tag: tagFlows,
			Summary: "Validate a flow document without saving it",
			Doc:     "Project-independent: the parser checks the document itself, and a flow carries its own agents.",
			Handler: s.validateFlow, Body: ValidateFlowRequest{}, Returns: FlowValidation{}, Enabled: haveConf},

		{Method: "GET", Path: "/api/projects/{id}/flows", ID: "listFlows", Tag: tagFlows,
			Summary: "A project's flows, with the templates it has not adopted",
			Handler: s.listFlows, Returns: []flowResp{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/projects/{id}/flows", ID: "createFlow", Tag: tagFlows,
			Summary: "Create a flow in a project",
			Handler: s.createFlow, Body: CreateFlowRequest{}, Returns: flowResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}/flows/{name}", ID: "getFlow", Tag: tagFlows,
			Summary: "One of a project's flows",
			Doc:     "Falls back to the built-in template of that name, marked builtin, when the project has not saved its own.",
			Handler: s.getFlow, Returns: flowResp{}, Enabled: haveConf},
		{Method: "PUT", Path: "/api/projects/{id}/flows/{name}", ID: "updateFlow", Tag: tagFlows,
			Summary: "Replace one of a project's flows",
			Doc:     "Saving a built-in template's name for the first time creates this project's own copy of it.",
			Handler: s.putFlow, Body: UpdateFlowRequest{}, Returns: flowResp{}, Enabled: haveConf},
		{Method: "DELETE", Path: "/api/projects/{id}/flows/{name}", ID: "deleteFlow", Tag: tagFlows,
			Summary: "Delete a project's flow (resets to the template, when there is one)",
			Handler: s.deleteFlow, Status: http.StatusNoContent, Enabled: haveConf},
		{Method: "POST", Path: "/api/projects/{id}/flows/{name}/copy", ID: "copyFlow", Tag: tagFlows,
			Summary: "Copy a flow into another project",
			Handler: s.copyFlow, Body: CopyFlowRequest{}, Returns: flowResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}/flows/{name}/versions", ID: "listFlowVersions", Tag: tagFlows,
			Summary: "A flow's version history within its project",
			Handler: s.listFlowVersions, Returns: []confstore.FlowVersion{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}/flow-versions/{hash}", ID: "getFlowVersion", Tag: tagFlows,
			Summary: "One flow version by content hash",
			Handler: s.getFlowVersion, Returns: confstore.FlowVersion{}, Enabled: haveConf},
	}
}

// Handler registers every enabled route, plus the CPUI bundle when one is
// configured. The bundle is not in the table: it is a static file server with
// an SPA fallback, not an API endpoint, and it would only muddy the spec.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		if rt.Enabled != nil && !rt.Enabled(s) {
			continue
		}
		mux.HandleFunc(rt.Method+" "+rt.Path, rt.Handler)
	}
	mux.HandleFunc("GET /api/openapi.json", s.getOpenAPIJSON)
	mux.HandleFunc("GET /api/openapi.yaml", s.getOpenAPIYAML)
	mux.HandleFunc("GET /docs", s.serveDocs)
	mux.HandleFunc("GET /docs/", s.serveDocs)
	if haveCPUI(s) {
		mux.HandleFunc("GET /", s.serveCPUI)
	}
	return mux
}

// Compile-time anchors for types the table names only through helpers.
var (
	_ = tickets.Sources(nil)
	_ = ports.ObservedPort{}
)
