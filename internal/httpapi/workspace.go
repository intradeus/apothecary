package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/auth"
	"apothecary/internal/engine"
	"apothecary/internal/sidecar"
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
	id, err := s.Engine.SendMessage(r.PathValue("id"), body.Text, body.Interrupt)
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
	err := s.Engine.TakeOver(r.PathValue("id"), p.User.ID)
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
	err := s.Engine.Release(r.PathValue("id"), p.User.ID)
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
func (s *Server) workspace(w http.ResponseWriter, r *http.Request) (*sidecar.Client, engine.Checkout, bool) {
	c, co, err := s.Engine.Workspace(r.Context(), r.PathValue("id"), r.URL.Query().Get("repo"))
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrNoSidecar):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, engine.ErrNoSuchCheckout):
			writeErr(w, http.StatusNotFound, err.Error())
		default:
			writeErr(w, http.StatusBadGateway, err.Error())
		}
		return nil, engine.Checkout{}, false
	}
	return c, co, true
}

func (s *Server) getWorkspaceRepos(w http.ResponseWriter, r *http.Request) {
	cos, err := s.Engine.Checkouts(r.Context(), r.PathValue("id"))
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
