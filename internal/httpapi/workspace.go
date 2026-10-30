package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/ask"
	"apothecary/internal/auth"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/mrs"
	"apothecary/internal/sidecar"
	"apothecary/internal/wsinfo"
)

// SendMessageRequest is a human message to the run's running agent.
type SendMessageRequest struct {
	Text string `json:"text"`
	// Interrupt cancels the agent's turn in flight so it reads this now;
	// otherwise the message is delivered when the turn ends.
	Interrupt bool `json:"interrupt,omitempty"`
}

type MessageAccepted struct {
	MessageID string `json:"message_id"`
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	var body SendMessageRequest
	if !readJSON(w, r, &body) {
		return
	}
	id, err := s.Runs.SendMessage(r.PathValue("id"), body.Text, body.Interrupt)
	switch {
	case errors.Is(err, engine.ErrRunNotActive), errors.Is(err, engine.ErrNoAgentRunning):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, MessageAccepted{MessageID: id})
	}
}

// postHold takes over the run's live agent session for the signed-in user.
func (s *Server) postHold(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	err := s.Runs.TakeOver(r.PathValue("id"), p.User.ID)
	switch {
	case errors.Is(err, engine.ErrRunNotActive), errors.Is(err, engine.ErrNoAgentRunning), errors.Is(err, engine.ErrAlreadyHeld):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil && strings.Contains(err.Error(), "not found"):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// deleteHold releases a take-over: the hold is closed on the record and the
// agent is handed the task back.
func (s *Server) deleteHold(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	err := s.Runs.Release(r.PathValue("id"), p.User.ID)
	switch {
	case errors.Is(err, engine.ErrRunNotActive), errors.Is(err, engine.ErrNoAgentRunning), errors.Is(err, engine.ErrNotHeld):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil && strings.Contains(err.Error(), "not found"):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// workspace resolves the run's sidecar and the checkout ?repo= names (the
// primary when absent), answering the error itself.
func (s *Server) workspace(w http.ResponseWriter, r *http.Request) (*sidecar.Client, wsinfo.Checkout, bool) {
	c, co, err := s.Workspaces.Workspace(r.Context(), r.PathValue("id"), r.URL.Query().Get("repo"))
	if err != nil {
		switch {
		case errors.Is(err, wsinfo.ErrNoSidecar):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, wsinfo.ErrNoSuchCheckout):
			writeErr(w, http.StatusNotFound, err.Error())
		default:
			writeErr(w, http.StatusBadGateway, err.Error())
		}
		return nil, wsinfo.Checkout{}, false
	}
	return c, co, true
}

func (s *Server) getWorkspaceRepos(w http.ResponseWriter, r *http.Request) {
	cos, err := s.Workspaces.Checkouts(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cos)
}

func (s *Server) getRunMRStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.MRs.MRStatuses(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sidecarErr passes the sidecar's own client errors (bad path, not found)
// through, and reports everything else as the gateway failure it is.
func sidecarErr(w http.ResponseWriter, err error) {
	var se *sidecar.Error
	if errors.As(err, &se) && se.Status >= 400 && se.Status < 500 {
		writeErr(w, se.Status, se.Msg)
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

func (s *Server) getWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	c, co, ok := s.workspace(w, r)
	if !ok {
		return
	}
	ents, err := c.Tree(r.Context(), co.Name, r.URL.Query().Get("path"))
	if err != nil {
		sidecarErr(w, err)
		return
	}
	if ents == nil {
		ents = []sidecar.TreeEntry{}
	}
	writeJSON(w, http.StatusOK, ents)
}

func (s *Server) getWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	c, co, ok := s.workspace(w, r)
	if !ok {
		return
	}
	f, err := c.File(r.Context(), co.Name, r.URL.Query().Get("path"))
	if err != nil {
		sidecarErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) getWorkspaceChanges(w http.ResponseWriter, r *http.Request) {
	c, co, ok := s.workspace(w, r)
	if !ok {
		return
	}
	ch, err := c.Changes(r.Context(), co.Name, co.Base)
	if err != nil {
		sidecarErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) getWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	c, co, ok := s.workspace(w, r)
	if !ok {
		return
	}
	d, err := c.Diff(r.Context(), co.Name, co.Base, r.URL.Query().Get("path"))
	if err != nil {
		sidecarErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// serveTerminal upgrades to a WebSocket terminal in the run's workspace,
// proxied to the sidecar. Not in the route table: a protocol switch is not
// something OpenAPI describes, and the generated client would not use it.
func (s *Server) serveTerminal(w http.ResponseWriter, r *http.Request) {
	c, co, ok := s.workspace(w, r)
	if !ok {
		return
	}
	c.TerminalProxy(co.Name).ServeHTTP(w, r)
}

// workspaceRoutes is a run's workspace: ports, the editor, resources, the Ask pane, SSH, files and changes.
func (s *Server) workspaceRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/runs/{id}/ports", ID: "getRunPorts", Tag: tagWorkspace,
			Summary: "Exposed and newly observed workspace ports",
			Handler: s.getRunPorts, Returns: RunPorts{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/ssh", ID: "getRunSsh", Tag: tagWorkspace,
			Summary: "How to reach the run's workspace over SSH: the command, the VS Code link, the door's fingerprint",
			Doc:     "The SSH door is the engine's own listener (APOTHECARY_SSH_LISTEN); the username is the workspace, the key one registered on the account. The workspace's sidecar is started on demand, so the first answer may take a moment.",
			Handler: s.getRunSSH, Returns: RunSSH{}, Scope: ScopeRun},
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
			Doc:     "Delivered into the agent's live session when its turn ends — or at once, cancelling the turn, with interrupt. 409 when no agent is running to take it (a run parked on the holder of a take-over is woken by it).",
			Handler: s.postMessage, Body: SendMessageRequest{}, Returns: MessageAccepted{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/hold", ID: "takeOverRun", Tag: tagRuns,
			Summary: "Take over the running agent's session",
			Doc:     "While held, the end of the agent's turn is not the end of the stage: it waits for your next message, each message a turn in the same session, until you release. 409 when no agent is running, or someone already holds it.",
			Handler: s.postHold, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "DELETE", Path: "/api/runs/{id}/hold", ID: "releaseRun", Tag: tagRuns,
			Summary: "Hand the task back to the agent",
			Doc:     "Closes the take-over on the record (duration, turns, cost) and sends the session the handback prompt — when the turn in flight ends, if one is. The stage ends as usual once that turn does. 409 when nobody holds it.",
			Handler: s.deleteHold, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/ask", ID: "getAsk", Tag: tagRuns,
			Summary: "The Ask pane: its conversation, model and whether it is on",
			Doc:     "A flow with an ask: section gives the run a read-only session beside its stages that answers questions about the code. Off (with the reason) when the flow has none, the workspace is gone, or the harness cannot promise a read-only session.",
			Handler: s.getAsk, Returns: ask.AskState{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/ask", ID: "askRun", Tag: tagRuns,
			Summary: "Ask the run's read-only session a question",
			Doc:     "The question is recorded and returned at once; the answer arrives on the pane's stream. 409 when the pane is off or an answer is being written.",
			Handler: s.postAsk, Body: AskRequest{}, Returns: ask.Message{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "PUT", Path: "/api/runs/{id}/ask/model", ID: "setAskModel", Tag: tagRuns,
			Summary: "Pick the model the Ask session answers with",
			Handler: s.putAskModel, Body: AskModelRequest{}, Status: http.StatusNoContent, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/ask/stream", ID: "streamAsk", Tag: tagRuns,
			Summary: "Server-sent event stream of the Ask pane",
			Doc:     "Replays the messages after after_seq (id = seq), then streams: answers as they are written (delta frames), each message as it lands, the pane's state as it changes.",
			Query:   []Param{{Name: "after_seq", Doc: "resume after this message sequence number", Type: "integer"}},
			Handler: s.streamAsk, Produces: "text/event-stream", Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/mrs/status", ID: "getRunMRStatus", Tag: tagRuns,
			Summary: "Each merge request as its repository reports it now: mergeability and pending review threads",
			Handler: s.getRunMRStatus, Returns: []mrs.MRLive{}, Scope: ScopeRun},
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
	}
}
