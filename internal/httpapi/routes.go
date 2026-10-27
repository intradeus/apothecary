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
	"apothecary/internal/auth"
	"apothecary/internal/channels"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/kinds"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/sidecar"
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
	// Auth is how the endpoint is reached (auth.go): a signed-in user by
	// default, the instance admin, the in-workspace shim's run token, or
	// anyone. Scope names the resource the path's id must belong to the
	// caller through; a user route with an id and no scope is refused by
	// TestEveryRouteDeclaresItsAccess.
	Auth  Access
	Scope Scope
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
	tagAutomations  = "automations"
	tagChannels     = "channels"
	tagSystem       = "system"
	tagAuth         = "auth"
	tagUsers        = "users"
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
		// --- auth -----------------------------------------------------------
		// Accounts (TODO 25): the one public surface is finding out whether the
		// instance is set up, setting it up, and signing in.
		{Method: "GET", Path: "/api/auth/status", ID: "getAuthStatus", Tag: tagAuth,
			Summary: "Whether the instance needs its first account, and how one may sign in",
			Handler: s.getAuthStatus, Returns: AuthStatus{}, Auth: AccessPublic},
		{Method: "POST", Path: "/api/auth/setup", ID: "setupInstance", Tag: tagAuth,
			Summary: "Claim a fresh instance: create its first account, an admin, and sign in",
			Doc:     "Only while no account exists (409 after).",
			Handler: s.postSetup, Body: SetupRequest{}, Returns: Me{}, Status: http.StatusCreated, Auth: AccessPublic},
		{Method: "POST", Path: "/api/auth/login", ID: "login", Tag: tagAuth,
			Summary: "Sign in and start a browser session",
			Doc:     "method is password unless the instance offers another; login is a handle or an email; proof the password. Repeated failures for one account from one address are throttled (429).",
			Handler: s.postLogin, Body: LoginRequest{}, Returns: Me{}, Auth: AccessPublic},
		{Method: "POST", Path: "/api/auth/logout", ID: "logout", Tag: tagAuth,
			Summary: "End the browser session",
			Handler: s.postLogout, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/me", ID: "getMe", Tag: tagAuth,
			Summary: "The signed-in user",
			Handler: s.getMe, Returns: Me{}},
		{Method: "PUT", Path: "/api/me", ID: "updateMe", Tag: tagAuth,
			Summary: "Change one's own handle, email or name",
			Handler: s.putMe, Body: ProfileRequest{}, Returns: Me{}},
		{Method: "PUT", Path: "/api/me/password", ID: "changeMyPassword", Tag: tagAuth,
			Summary: "Change one's own password",
			Doc:     "Every other session is signed out; this one continues.",
			Handler: s.putMyPassword, Body: PasswordChangeRequest{}, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/me/tokens", ID: "listMyTokens", Tag: tagAuth,
			Summary: "One's personal API tokens (never their secrets)",
			Handler: s.listMyTokens, Returns: []auth.Token{}},
		{Method: "POST", Path: "/api/me/tokens", ID: "createMyToken", Tag: tagAuth,
			Summary: "Mint a personal API token",
			Doc:     "The secret is in this response and nowhere else: it is stored hashed. Send it as a bearer token.",
			Handler: s.createMyToken, Body: TokenRequest{}, Returns: TokenCreated{}, Status: http.StatusCreated},
		{Method: "DELETE", Path: "/api/me/tokens/{id}", ID: "deleteMyToken", Tag: tagAuth,
			Summary: "Revoke a personal API token",
			Handler: s.deleteMyToken, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/limits", ID: "getLimits", Tag: tagSystem,
			Summary: "The instance's capacity caps (-1: no cap)",
			Handler: s.getLimits, Returns: LimitsInfo{}},
		{Method: "PUT", Path: "/api/limits", ID: "setLimits", Tag: tagSystem,
			Summary: "Set the instance's capacity caps (admin; -1: no cap)",
			Handler: s.setLimits, Body: LimitsInfo{}, Returns: LimitsInfo{}, Auth: AccessAdmin},

		// --- users (admin) ---------------------------------------------------
		{Method: "GET", Path: "/api/users", ID: "listUsers", Tag: tagUsers,
			Summary: "Every account",
			Handler: s.listUsers, Returns: []auth.User{}, Auth: AccessAdmin},
		{Method: "POST", Path: "/api/users", ID: "createUser", Tag: tagUsers,
			Summary: "Create an account with a password",
			Handler: s.createUser, Body: UserCreateRequest{}, Returns: auth.User{}, Status: http.StatusCreated, Auth: AccessAdmin},
		{Method: "PUT", Path: "/api/users/{id}", ID: "updateUser", Tag: tagUsers,
			Summary: "Change an account: its profile, whether it is an admin, whether it is disabled",
			Doc:     "Disabling an account signs it out everywhere, revokes its tokens, aborts its active runs and switches its automations off; its history and workspaces stay.",
			Handler: s.updateUser, Body: UserUpdateRequest{}, Returns: auth.User{}, Auth: AccessAdmin},
		{Method: "POST", Path: "/api/users/{id}/password", ID: "resetUserPassword", Tag: tagUsers,
			Summary: "Set an account's password",
			Doc:     "Signs the account out everywhere.",
			Handler: s.resetUserPassword, Body: PasswordResetRequest{}, Status: http.StatusNoContent, Auth: AccessAdmin},

		// --- runs -----------------------------------------------------------
		{Method: "GET", Path: "/api/runs", ID: "listRuns", Tag: tagRuns,
			Summary: "List one's runs, newest first",
			Query: []Param{
				{Name: "project", Doc: "only runs of this project"},
				{Name: "ticket", Doc: "only runs of this ticket"},
			},
			Handler: s.listRuns, Returns: []run.View{}},
		{Method: "GET", Path: "/api/runs/{id}", ID: "getRun", Tag: tagRuns,
			Summary: "One run with the tail of its event log",
			Handler: s.getRun, Returns: RunDetail{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/events", ID: "streamRunEvents", Tag: tagRuns,
			Summary: "Server-sent event stream of a run's events",
			Doc:     "Replays from after_seq, then streams live. The stream ends when the run reaches a terminal state.",
			Query:   []Param{{Name: "after_seq", Doc: "resume after this sequence number", Type: "integer"}},
			Handler: s.streamEvents, Produces: "text/event-stream", Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/logs/{stage}", ID: "getStageLog", Tag: tagRuns,
			Summary: "A stage's captured log (latest attempt)",
			Query:   tailQuery, Handler: s.getLog, Produces: "text/plain", Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace-log", ID: "getWorkspaceLog", Tag: tagWorkspace,
			Summary: "Live tail of the workspace's exec log",
			Query:   tailQuery, Handler: s.getWorkspaceLog, Produces: "text/plain", Scope: ScopeRun},

		{Method: "POST", Path: "/api/runs/{id}/abort", ID: "abortRun", Tag: tagRuns,
			Summary: "Stop a run for good", Handler: s.postAbort, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/phases", ID: "getRunPhases", Tag: tagRuns,
			Summary: "Every phase of the run's flow: what opens it, its runs per merge request, its live run and the signals holding for it",
			Handler: s.getRunPhases, Returns: []engine.PhaseInfo{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/phases/{phase}/start", ID: "startPhase", Tag: tagRuns,
			Summary: "Start a phase now, by hand",
			Doc:     "Only while the run is idle (between phases, or parked at a wait point). Any phase without a live run: the phase's signal need not hold — a person's start is its own decision — and its max still applies. Acts on the named checkout's merge request, else the run's single open one, else on none.",
			Body:    StartPhaseRequest{}, Handler: s.postStartPhase, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/pause", ID: "pauseRun", Tag: tagRuns,
			Summary: "Park a run; nothing moves until play",
			Doc:     "Works on a run that is parked waiting for an answer too: pausing one says it must not pick up when the answer lands.",
			Handler: s.postPause, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/play", ID: "playRun", Tag: tagRuns,
			Summary: "Continue a paused run where it left off",
			Handler: s.postPlay, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/archive", ID: "archiveRun", Tag: tagRuns,
			Summary: "Archive a finished run and destroy its workspace",
			Handler: s.postArchive, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/retry", ID: "retryRun", Tag: tagRuns,
			Summary: "Try again: re-enter the failed stage in the same workspace",
			Handler: s.postRetry, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/rerun", ID: "rerunRun", Tag: tagRuns,
			Summary: "Run the ticket again as a new run in a fresh workspace",
			Handler: s.postRerun, Body: RerunRequest{}, Returns: RunRef{}, Status: http.StatusCreated, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/rebuild", ID: "rebuildWorkspace", Tag: tagWorkspace,
			Summary: "Recreate the run's container in place, keeping the checkout",
			Doc:     "no_cache rebuilds the image from scratch — for the staleness a cache key cannot see, like a floating base tag that moved.",
			Handler: s.postRebuild, Body: RebuildRequest{}, Status: http.StatusAccepted, Scope: ScopeRun},

		// --- egress ---------------------------------------------------------
		{Method: "GET", Path: "/api/egress-defaults", ID: "getEgressDefaults", Tag: tagEgress,
			Summary: "The built-in allowlist every project starts from",
			Handler: s.getEgressDefaults, Returns: EgressDefaults{}},
		{Method: "GET", Path: "/api/runs/{id}/egress-denials", ID: "listEgressDenials", Tag: tagEgress,
			Summary: "Hosts the gateway blocked for this run",
			Handler: s.getEgressDenials, Returns: []ports.EgressDenial{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/egress-allow", ID: "allowEgressHost", Tag: tagEgress,
			Summary: "Unblock a host for a live run and persist it to the project",
			Handler: s.postEgressAllow, Body: EgressHostRequest{}, Returns: EgressAllowResult{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/egress-rule", ID: "setEgressRule", Tag: tagEgress,
			Summary: "Allow or force-block a host for a run",
			Handler: s.postEgressRule, Body: EgressRuleRequest{}, Returns: EgressRuleResult{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/egress-audit", ID: "getRunEgressAudit", Tag: tagEgress,
			Summary: "Everything this run's workspace reached or was blocked from",
			Query:   auditQuery, Handler: s.getRunEgressAudit, Returns: []ports.EgressAuditRow{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/egress-policy", ID: "getRunEgressPolicy", Tag: tagEgress,
			Summary: "The firewall policy in force for a run right now",
			Handler: s.getRunEgressPolicy, Returns: EgressPolicy{}, Scope: ScopeRun},

		// --- workspace ports / editor / resources ---------------------------
		{Method: "GET", Path: "/api/runs/{id}/ports", ID: "getRunPorts", Tag: tagWorkspace,
			Summary: "Exposed and newly observed workspace ports",
			Handler: s.getRunPorts, Returns: RunPorts{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/expose-port", ID: "exposeRunPort", Tag: tagWorkspace,
			Summary: "Publish a workspace port and get its browsable URL",
			Handler: s.postExposePort, Body: ExposePortRequest{}, Returns: ExposedPort{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/observed-ports", ID: "reportObservedPorts", Tag: tagWorkspace,
			Summary: "Report listening ports (in-workspace scanner)",
			Handler: s.postObservedPorts, Body: ObservedPortsRequest{}, Status: http.StatusNoContent,
			Auth: AccessRunToken},
		{Method: "POST", Path: "/api/runs/{id}/open-editor", ID: "openEditor", Tag: tagWorkspace,
			Summary: "Start code-server in the workspace and get its URL",
			Handler: s.postOpenEditor, Returns: EditorURL{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/messages", ID: "sendRunMessage", Tag: tagRuns,
			Summary: "Send a message to the run's running agent",
			Doc:     "Delivered into the agent's live session when its turn ends — or at once, cancelling the turn, with interrupt. 409 when no agent is running to take it.",
			Handler: s.postMessage, Body: SendMessageRequest{}, Returns: MessageAccepted{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/mrs/status", ID: "getRunMRStatus", Tag: tagRuns,
			Summary: "Each merge request as its repository reports it now: mergeability and pending review threads",
			Handler: s.getRunMRStatus, Returns: []engine.MRLive{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace/repos", ID: "getWorkspaceRepos", Tag: tagWorkspace,
			Summary: "The run's checkouts (one per repository)",
			Handler: s.getWorkspaceRepos, Returns: []event.Checkout{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace/tree", ID: "getWorkspaceTree", Tag: tagWorkspace,
			Summary: "One directory level of a run checkout",
			Query:   []Param{{Name: "repo", Doc: "checkout name (default: the primary)"}, {Name: "path", Doc: "repo-relative directory (default: the root)"}},
			Handler: s.getWorkspaceTree, Returns: []sidecar.TreeEntry{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace/file", ID: "getWorkspaceFile", Tag: tagWorkspace,
			Summary: "One file of a run checkout",
			Query:   []Param{{Name: "repo", Doc: "checkout name (default: the primary)"}, {Name: "path", Doc: "repo-relative file"}},
			Handler: s.getWorkspaceFile, Returns: sidecar.File{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace/changes", ID: "getWorkspaceChanges", Tag: tagWorkspace,
			Summary: "A checkout's changes against the branch it forked from",
			Doc:     "Committed and uncommitted, measured from the merge base with the source branch.",
			Query:   []Param{{Name: "repo", Doc: "checkout name (default: the primary)"}},
			Handler: s.getWorkspaceChanges, Returns: sidecar.Changes{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace/diff", ID: "getWorkspaceDiff", Tag: tagWorkspace,
			Summary: "One file's unified diff against the branch the checkout forked from",
			Query:   []Param{{Name: "repo", Doc: "checkout name (default: the primary)"}, {Name: "path", Doc: "repo-relative file"}},
			Handler: s.getWorkspaceDiff, Returns: sidecar.FileDiff{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/resources", ID: "getRunResources", Tag: tagWorkspace,
			Summary: "Rolling window of workspace cpu/memory/network samples",
			Handler: s.getRunResources, Returns: RunResources{}, Scope: ScopeRun},

		// --- services -------------------------------------------------------
		{Method: "GET", Path: "/api/runs/{id}/services", ID: "getRunServices", Tag: tagWorkspace,
			Summary: "The project's services and how each stands in the run's workspace",
			Doc:     "Live state read from the workspace's supervisor on each call — poll it while the pane is open; service_started, service_ready, service_stopped and service_failed also arrive on the run's event stream. A healthy service's ports carry their preview links. workspace is false when the run has none; error says its services could not be read just now.",
			Handler: s.getRunServices, Returns: engine.RunServices{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/services/{name}/start", ID: "startRunService", Tag: tagWorkspace,
			Summary: "Start a service by hand",
			Doc:     "Returns once the workspace runs it (already up: left alone); whether it comes up is its state on the services list, and service_ready or service_failed on the event stream. A service started by hand stays up until someone stops it or the workspace goes. 409 when the run has no workspace.",
			Handler: s.postStartService, Returns: engine.ServiceInfo{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/services/{name}/restart", ID: "restartRunService", Tag: tagWorkspace,
			Summary: "Stop a service and start it again",
			Handler: s.postRestartService, Returns: engine.ServiceInfo{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/services/{name}/stop", ID: "stopRunService", Tag: tagWorkspace,
			Summary: "Stop a service, whoever started it",
			Handler: s.postStopService, Returns: engine.ServiceInfo{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/services/{name}/logs", ID: "getRunServiceLogs", Tag: tagWorkspace,
			Summary: "The last lines of a service's output",
			Query:   []Param{{Name: "tail", Doc: "lines (default 200, at most 5000)", Type: "integer"}},
			Handler: s.getServiceLogs, Produces: "text/plain", Scope: ScopeRun},

		// --- pings ----------------------------------------------------------
		{Method: "POST", Path: "/api/runs/{id}/pings", ID: "raisePing", Tag: tagPings,
			Summary: "Ask the human a question (ask_human)",
			Doc:     "Holds for hold_ms and answers in-band when it can; 204 means not yet — re-poll with X-Ping-ID while the agent's turn waits. key makes a retried raise return the same question.",
			Handler: s.postPing, Body: RaisePingRequest{}, Returns: PingHold{},
			Auth: AccessRunToken},
		{Method: "GET", Path: "/api/runs/{id}/pings/{pid}", ID: "repollPing", Tag: tagPings,
			Summary: "Long-poll a raised question for its answer",
			Doc:     "200 with the answer, 204 still open (poll again), 410 the run parked on it (stop waiting).",
			Handler: s.repollPing, Returns: PingHold{},
			Auth: AccessRunToken},
		{Method: "GET", Path: "/api/pings", ID: "listPings", Tag: tagPings,
			Summary: "The inbox: one's questions waiting on a human",
			Query: []Param{
				{Name: "status", Doc: "pending|answered|all (default pending)", Enum: []string{"pending", "answered", "all"}},
				{Name: "limit", Doc: "max rows", Type: "integer"},
			},
			Handler: s.listPings, Returns: []run.PingView{}},
		{Method: "GET", Path: "/api/pings/{pid}", ID: "getPing", Tag: tagPings,
			Summary: "One question with its answer, if it has one",
			Handler: s.getStoredPing, Returns: run.PingView{}, Scope: ScopePing},
		{Method: "GET", Path: "/api/pings/{pid}/files", ID: "getPingFiles", Tag: tagPings,
			Summary: "The files a question attached, as captured when it was raised",
			Handler: s.getPingFiles, Returns: PingFiles{}, Scope: ScopePing},
		{Method: "POST", Path: "/api/pings/{pid}/answer", ID: "answerPing", Tag: tagPings,
			Summary: "Answer a question",
			Handler: s.answerPing, Body: AnswerPingRequest{}, Returns: AnswerAccepted{}, Scope: ScopePing},

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
			Handler: s.getAttachment, Produces: "application/octet-stream", Scope: ScopeAttachment},
		{Method: "GET", Path: "/api/snapshots/{id}", ID: "downloadSnapshot", Tag: tagFiles,
			Summary: "Download one engine-captured record",
			Handler: s.getSnapshot, Produces: "application/octet-stream", Scope: ScopeSnapshot},
		{Method: "GET", Path: "/api/runs/{id}/files", ID: "getRunFiles", Tag: tagFiles,
			Summary: "Everything filed under a run: human uploads and engine captures",
			Handler: s.getRunFiles, Returns: RunFiles{}, Scope: ScopeRun},

		// --- channels -------------------------------------------------------
		{Method: "GET", Path: "/api/channels", ID: "listChannels", Tag: tagChannels,
			Summary: "One's notification channels (secrets masked)",
			Handler: s.listChannels, Returns: []channels.Entry{}},
		{Method: "POST", Path: "/api/channels", ID: "addChannel", Tag: tagChannels,
			Summary: "Add a notification channel",
			Handler: s.addChannel, Body: ChannelRequest{}, Returns: channels.Entry{}, Status: http.StatusCreated},
		{Method: "DELETE", Path: "/api/channels/{id}", ID: "deleteChannel", Tag: tagChannels,
			Summary: "Remove a notification channel",
			Handler: s.deleteChannel, Status: http.StatusNoContent, Scope: ScopeChannel},

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
			Summary: "One's policy settings: the reaper and retention windows",
			Handler: s.getSettings, Returns: confstore.Settings{}, Enabled: haveConf},
		{Method: "PUT", Path: "/api/settings", ID: "updateSettings", Tag: tagSystem,
			Summary: "Replace one's policy settings",
			Handler: s.putSettings, Body: confstore.Settings{}, Returns: confstore.Settings{}, Enabled: haveConf},

		// --- integrations ---------------------------------------------------
		{Method: "GET", Path: "/api/integrations", ID: "listIntegrations", Tag: tagIntegrations,
			Summary: "One's integrations (secrets masked)",
			Handler: s.listIntegrations, Returns: []integrationJSON{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/integrations", ID: "createIntegration", Tag: tagIntegrations,
			Summary: "Configure an integration",
			Handler: s.createIntegration, Body: IntegrationRequest{}, Returns: integrationJSON{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/integrations/{id}", ID: "getIntegration", Tag: tagIntegrations,
			Summary: "One integration",
			Handler: s.getIntegration, Returns: integrationJSON{}, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "PUT", Path: "/api/integrations/{id}", ID: "updateIntegration", Tag: tagIntegrations,
			Summary: "Change an integration (secrets merge: present replaces, \"\" deletes, absent keeps)",
			Handler: s.updateIntegration, Body: IntegrationUpdate{}, Returns: integrationJSON{}, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "DELETE", Path: "/api/integrations/{id}", ID: "deleteIntegration", Tag: tagIntegrations,
			Summary: "Delete an integration",
			Handler: s.deleteIntegration, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "POST", Path: "/api/integrations/{id}/test", ID: "testIntegration", Tag: tagIntegrations,
			Summary: "Check an integration's credentials against the real service",
			Handler: s.testIntegration, Returns: TestResult{}, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "GET", Path: "/api/integrations/{id}/repos", ID: "listIntegrationRepos", Tag: tagIntegrations,
			Summary: "Repositories a repository integration (GitLab, GitHub) can see",
			Handler: s.listIntegrationRepos, Returns: []kinds.RepoInfo{}, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "GET", Path: "/api/integrations/{id}/models", ID: "listIntegrationModels", Tag: tagIntegrations,
			Summary: "Models a harness integration's credentials can reach",
			Handler: s.listIntegrationModels, Returns: []kinds.ModelInfo{}, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "GET", Path: "/api/integrations/{id}/tickets", ID: "searchSourceTickets", Tag: tagTickets,
			Summary: "Search a tracker for tickets to import (read-only)",
			Query: []Param{
				{Name: "q", Doc: "free-text query"},
				{Name: "limit", Doc: "max results (default 25)", Type: "integer"},
			},
			Handler: s.searchSourceTickets, Returns: []sourceTicketJSON{}, Enabled: haveConf, Scope: ScopeIntegration},
		{Method: "GET", Path: "/api/integrations/{id}/tickets/{key}", ID: "getSourceTicket", Tag: tagTickets,
			Summary: "One tracker ticket by key",
			Handler: s.getSourceTicket, Returns: sourceTicketJSON{}, Enabled: haveConf, Scope: ScopeIntegration},

		// --- projects -------------------------------------------------------
		{Method: "GET", Path: "/api/projects", ID: "listProjects", Tag: tagProjects,
			Summary: "One's projects", Handler: s.listProjects, Returns: []confstore.Project{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/projects", ID: "createProject", Tag: tagProjects,
			Summary: "Create a project",
			Handler: s.createProject, Body: projectBody{}, Returns: confstore.Project{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/projects/{id}", ID: "getProject", Tag: tagProjects,
			Summary: "One project", Handler: s.getProject, Returns: confstore.Project{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "PUT", Path: "/api/projects/{id}", ID: "updateProject", Tag: tagProjects,
			Summary: "Replace a project",
			Handler: s.updateProject, Body: projectBody{}, Returns: confstore.Project{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "DELETE", Path: "/api/projects/{id}", ID: "deleteProject", Tag: tagProjects,
			Summary: "Delete a project", Handler: s.deleteProject, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/branches", ID: "listProjectBranches", Tag: tagProjects,
			Summary: "Branches of the project's repository",
			Handler: s.listProjectBranches, Returns: []kinds.BranchInfo{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/mrs", ID: "listProjectMRs", Tag: tagProjects,
			Summary: "Open merge requests of a project's repository, with the runs on each",
			Doc:     "The repository's open merge requests, newest first: each with its author and head, whether the factory itself opened it, and the runs done on it here — its ticket and that ticket's newest run. What a run of a flow started on merge requests (operates_on: merge_request) may be started on.",
			Query:   []Param{{Name: "repo", Doc: "the repository: \"repo\" (the default) for the primary, an extra repository's name", Type: "string"}},
			Handler: s.listProjectMRs, Returns: []MRRow{}, Scope: ScopeProject},
		{Method: "POST", Path: "/api/projects/{id}/mrs/{iid}/runs", ID: "startMRRun", Tag: tagProjects,
			Summary: "Start a run of a flow on a merge request",
			Doc:     "Finds or makes the merge request's ticket and starts a run on the merge request of a flow whose runs are started on one (operates_on: merge_request; the project's default flow unless the body names another). 409 while a run of the same flow on it is going.",
			Body:    MRRunRequest{}, Returns: MRRunStarted{}, Status: http.StatusCreated,
			Handler: s.startMRRun, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/egress-audit", ID: "getProjectEgressAudit", Tag: tagEgress,
			Summary: "Everything this project's workspaces reached or were blocked from",
			Query:   auditQuery, Handler: s.getProjectEgressAudit, Returns: []ports.EgressAuditRow{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "POST", Path: "/api/projects/{id}/preflight", ID: "preflightProject", Tag: tagProjects,
			Summary: "Check a project is ready to run, step by step",
			Handler: s.preflightProject, Returns: PreflightResult{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "POST", Path: "/api/projects/{id}/envcheck", ID: "startEnvCheck", Tag: tagProjects,
			Summary: "Prove the project's workspace actually builds (throwaway provision)",
			Doc:     "Starting a check cancels the project's running one, if any: the newest check is the one that matters.",
			Handler: s.startEnvCheck, Body: EnvCheckRequest{}, Returns: EnvCheckRef{},
			Status: http.StatusAccepted, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/build-cache", ID: "getBuildCache", Tag: tagProjects,
			Summary: "What the project's devcontainer build cache holds",
			Doc:     "The cache is per project and outlives its runs — it is what stops every run rebuilding the image into its own throwaway daemon.",
			Handler: s.getBuildCache, Returns: BuildCacheInfo{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "DELETE", Path: "/api/projects/{id}/build-cache", ID: "clearBuildCache", Tag: tagProjects,
			Summary: "Empty the project's build cache",
			Doc:     "Safe: the next build repopulates it, so the cost of clearing needlessly is one slow build.",
			Handler: s.deleteBuildCache, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/secrets", ID: "listProjectSecrets", Tag: tagProjects,
			Summary: "The project's secrets, by name",
			Doc:     "What a flow's MCP servers read where they say ${secrets.<name>}. Names and when each was last set — never a value.",
			Handler: s.listProjectSecrets, Returns: []confstore.ProjectSecret{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "PUT", Path: "/api/projects/{id}/secrets/{name}", ID: "setProjectSecret", Tag: tagProjects,
			Summary: "Set one project secret",
			Doc:     "Creates or replaces it; sealed at rest. A name is a lowercase slug. The next agent stage that reads it gets the new value — no new run needed.",
			Body:    ProjectSecretRequest{}, Handler: s.putProjectSecret, Returns: confstore.ProjectSecret{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "DELETE", Path: "/api/projects/{id}/secrets/{name}", ID: "deleteProjectSecret", Tag: tagProjects,
			Summary: "Remove one project secret",
			Doc:     "A flow that reads it is refused at its next run's creation.",
			Handler: s.deleteProjectSecret, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/browser", ID: "getProjectBrowser", Tag: tagProjects,
			Summary: "The project's browser: its toggle and its container",
			Doc:     "One container per project, a Chromium per agent session (the builtin browser MCP server). The toggle is the project's workspace.browser.enabled; this says whether the container runs, with what image, and how many sessions use it.",
			Handler: s.getProjectBrowser, Returns: BrowserInfo{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/envchecks/{id}", ID: "getEnvCheck", Tag: tagProjects,
			Summary: "Poll a workspace check",
			Handler: s.getEnvCheck, Returns: engine.CheckRun{}, Enabled: haveConf, Scope: ScopeEnvCheck},
		{Method: "DELETE", Path: "/api/envchecks/{id}", ID: "cancelEnvCheck", Tag: tagProjects,
			Summary: "Cancel a running workspace check",
			Doc:     "The check stops at its next step and still destroys its workspace.",
			Handler: s.cancelEnvCheck, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeEnvCheck},
		{Method: "GET", Path: "/api/projects/{id}/envcheck", ID: "getLatestEnvCheck", Tag: tagProjects,
			Summary: "The project's most recent workspace check",
			Doc:     "Running or finished — what a page opened mid-check follows.",
			Handler: s.getLatestEnvCheck, Returns: engine.CheckRun{}, Enabled: haveConf, Scope: ScopeProject},

		// --- tickets --------------------------------------------------------
		{Method: "GET", Path: "/api/tickets", ID: "listTickets", Tag: tagTickets,
			Summary: "One's tickets",
			Query:   []Param{{Name: "project", Doc: "only tickets of this project"}},
			Handler: s.listTickets, Returns: []confstore.Ticket{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/tickets", ID: "createTicket", Tag: tagTickets,
			Summary: "Create a ticket",
			Handler: s.createTicket, Body: ticketBody{}, Returns: ticketResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/tickets/{id}", ID: "getTicket", Tag: tagTickets,
			Summary: "One ticket", Handler: s.getTicket, Returns: ticketResp{}, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "PUT", Path: "/api/tickets/{id}", ID: "updateTicket", Tag: tagTickets,
			Summary: "Replace a ticket",
			Handler: s.updateTicket, Body: ticketBody{}, Returns: ticketResp{}, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "DELETE", Path: "/api/tickets/{id}", ID: "deleteTicket", Tag: tagTickets,
			Summary: "Delete a ticket", Handler: s.deleteTicket, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "GET", Path: "/api/tickets/{id}/attachments", ID: "getTicketAttachments", Tag: tagTickets,
			Summary: "Files attached to a ticket",
			Handler: s.getTicketAttachments, Returns: AttachmentList{}, Enabled: haveConf, Scope: ScopeTicket},
		{Method: "POST", Path: "/api/tickets/{id}/runs", ID: "startTicketRun", Tag: tagTickets,
			Summary: "Start one attempt on a ticket",
			Handler: s.startTicketRun, Body: StartRunRequest{}, Returns: RunRef{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeTicket},

		// --- flows ----------------------------------------------------------
		// A flow belongs to a project: its stages name that project's commands
		// and its agents know that repository. The built-ins stay global
		// templates, and copying is how a good flow reaches another project.
		{Method: "GET", Path: "/api/flows", ID: "listAllFlows", Tag: tagFlows,
			Summary: "Every flow of one's projects, for the cross-project overview",
			Handler: s.listFlows, Returns: []flowResp{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/flow-templates", ID: "listFlowTemplates", Tag: tagFlows,
			Summary: "The compiled-in flow templates a project can adopt or copy",
			Handler: s.listBuiltinFlows, Returns: []flowResp{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/flow-templates/{name}/copy", ID: "copyFlowTemplate", Tag: tagFlows,
			Summary: "Copy a compiled-in template into a project",
			Doc:     "A template belongs to no project, so it is copied from here rather than from under one. Copying it under its own name adopts it: the project gets its own editable document, which shadows the template from then on.",
			Handler: s.copyFlowTemplate, Body: CopyFlowRequest{}, Returns: flowResp{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/flows/schema", ID: "getFlowSchema", Tag: tagFlows,
			Summary: "The JSON Schema of a flow document as it is written",
			Doc:     "The YAML with its shorthand, for an editor to validate, complete and document as a person types; its lists (stage types, signals, actions and their inputs) come from the registries. The parser stays the authority: validate a document for the rules a schema cannot say.",
			Handler: s.getFlowSchema, Returns: map[string]any{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/flows/validate", ID: "validateFlow", Tag: tagFlows,
			Summary: "Validate a flow document without saving it",
			Doc:     "Project-independent: the parser checks the document itself, and a flow carries its own agents.",
			Handler: s.validateFlow, Body: ValidateFlowRequest{}, Returns: FlowValidation{}, Enabled: haveConf},

		{Method: "GET", Path: "/api/projects/{id}/flows", ID: "listFlows", Tag: tagFlows,
			Summary: "A project's flows, with the templates it has not adopted",
			Handler: s.listFlows, Returns: []flowResp{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "POST", Path: "/api/projects/{id}/flows", ID: "createFlow", Tag: tagFlows,
			Summary: "Create a flow in a project",
			Handler: s.createFlow, Body: CreateFlowRequest{}, Returns: flowResp{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/flows/{name}", ID: "getFlow", Tag: tagFlows,
			Summary: "One of a project's flows",
			Doc:     "Falls back to the built-in template of that name, marked builtin, when the project has not saved its own.",
			Handler: s.getFlow, Returns: flowResp{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "PUT", Path: "/api/projects/{id}/flows/{name}", ID: "updateFlow", Tag: tagFlows,
			Summary: "Replace one of a project's flows",
			Doc:     "Saving a built-in template's name for the first time creates this project's own copy of it.",
			Handler: s.putFlow, Body: UpdateFlowRequest{}, Returns: flowResp{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "DELETE", Path: "/api/projects/{id}/flows/{name}", ID: "deleteFlow", Tag: tagFlows,
			Summary: "Delete a project's flow (resets to the template, when there is one)",
			Handler: s.deleteFlow, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject},
		{Method: "POST", Path: "/api/projects/{id}/flows/{name}/copy", ID: "copyFlow", Tag: tagFlows,
			Summary: "Copy a flow into another project",
			Handler: s.copyFlow, Body: CopyFlowRequest{}, Returns: flowResp{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/automations", ID: "listAutomations", Tag: tagAutomations,
			Summary: "The project's automations: how runs begin without a person",
			Handler: s.listAutomations, Returns: []confstore.Automation{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "POST", Path: "/api/projects/{id}/automations", ID: "createAutomation", Tag: tagAutomations,
			Summary: "Create an automation",
			Doc:     "Validated the way a firing would be: the integration is a ticket source, the schedule parses, the flow resolves and its operates_on fits the kind (merge_request for a merge_request automation, ticket otherwise), the params are ones it accepts.",
			Handler: s.createAutomation, Body: AutomationRequest{}, Returns: confstore.Automation{},
			Status: http.StatusCreated, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/automations/{aid}", ID: "getAutomation", Tag: tagAutomations,
			Summary: "One automation, with what the watcher last did with it",
			Handler: s.getAutomation, Returns: confstore.Automation{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "PUT", Path: "/api/projects/{id}/automations/{aid}", ID: "updateAutomation", Tag: tagAutomations,
			Summary: "Replace an automation (its last poll, firing and error are kept)",
			Handler: s.updateAutomation, Body: AutomationRequest{}, Returns: confstore.Automation{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "DELETE", Path: "/api/projects/{id}/automations/{aid}", ID: "deleteAutomation", Tag: tagAutomations,
			Summary: "Delete an automation (the tickets it imported stay)",
			Handler: s.deleteAutomation, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/flow-bindings", ID: "listFlowBindings", Tag: tagFlows,
			Summary: "The project's parameter bindings, one per configured flow",
			Handler: s.listFlowBindings, Returns: []confstore.FlowBinding{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/flows/{name}/binding", ID: "getFlowBinding", Tag: tagFlows,
			Summary: "The project's values for a flow's parameters",
			Doc:     "404 when the project bound nothing: the flow runs on its declared defaults. A template is configurable without being copied.",
			Handler: s.getFlowBinding, Returns: confstore.FlowBinding{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "PUT", Path: "/api/projects/{id}/flows/{name}/binding", ID: "putFlowBinding", Tag: tagFlows,
			Summary: "Bind values to a flow's parameters for this project",
			Doc:     "Replaces the binding. Every key must be a parameter the flow declares (the engine-known names included) and every value one it accepts — checked the way a run checks them; an empty value unbinds. A binding left with no values is removed. The document itself is untouched.",
			Handler: s.putFlowBinding, Body: FlowBindingRequest{}, Returns: confstore.FlowBinding{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "DELETE", Path: "/api/projects/{id}/flows/{name}/binding", ID: "deleteFlowBinding", Tag: tagFlows,
			Summary: "Put the project back to the flow's declared defaults",
			Handler: s.deleteFlowBinding, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/flows/{name}/versions", ID: "listFlowVersions", Tag: tagFlows,
			Summary: "A flow's version history within its project",
			Handler: s.listFlowVersions, Returns: []confstore.FlowVersion{}, Enabled: haveConf, Scope: ScopeProject},
		{Method: "GET", Path: "/api/projects/{id}/flow-versions/{hash}", ID: "getFlowVersion", Tag: tagFlows,
			Summary: "One flow version by content hash",
			Handler: s.getFlowVersion, Returns: confstore.FlowVersion{}, Enabled: haveConf, Scope: ScopeProject},
	}
}

// Handler registers every enabled route behind its declared access (guard),
// plus the CPUI bundle when one is configured. The bundle is not in the
// table: it is a static file server with an SPA fallback, not an API
// endpoint, and it would only muddy the spec.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		if rt.Enabled != nil && !rt.Enabled(s) {
			continue
		}
		mux.HandleFunc(rt.Method+" "+rt.Path, s.guard(rt))
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
		return s.Apps.wrap(s, mux)
	}
	return mux
}

// Compile-time anchors for types the table names only through helpers.
var (
	_ = tickets.Sources(nil)
	_ = ports.ObservedPort{}
)
