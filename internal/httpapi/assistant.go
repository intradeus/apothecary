package httpapi

// The assistant's routes (docs/assistant-plan.md, phase 3): a person's
// sessions beside the flow, ticket and skill pages, their stream, and the
// fill a session's agent calls through the MCP face.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	"apothecary/internal/assistant"
)

func haveAssistant(s *Server) bool { return s.Assistant != nil }

// AssistantChoiceRequest is a person's harness integration and model.
type AssistantChoiceRequest struct {
	Integration string `json:"integration"`
	Model       string `json:"model"`
}

// AssistantOpenRequest opens (or finds) the session about a subject.
type AssistantOpenRequest struct {
	Kind     string `json:"kind"`
	Workshop string `json:"workshop_id,omitempty"`
	Subject  string `json:"subject"`
}

// AssistantSessionView is a session and its messages.
type AssistantSessionView struct {
	Session  assistant.Session   `json:"session"`
	Messages []assistant.Message `json:"messages"`
	// Busy: the session is answering now.
	Busy bool `json:"busy"`
}

// AssistantMessageRequest is a question and the page as it stands.
type AssistantMessageRequest struct {
	Text    string          `json:"text"`
	Context json.RawMessage `json:"context,omitempty"`
}

// AssistantModelRequest picks a session's model.
type AssistantModelRequest struct {
	Model string `json:"model"`
}

// AssistantSubjectRequest moves a draft's session to what it was saved as.
type AssistantSubjectRequest struct {
	Subject string `json:"subject"`
}

// AssistantRefreshRequest fetches a workshop's repositories now.
type AssistantRefreshRequest struct {
	Workshop string `json:"workshop_id"`
}

// AssistantFillRequest is the fill tool's call: the page's fill.
type AssistantFillRequest struct {
	Fill json.RawMessage `json:"fill"`
}

// AssistantFillResult is what the agent reads back.
type AssistantFillResult struct {
	OK     bool     `json:"ok"`
	Errors []string `json:"errors,omitempty"`
}

func assistantStatus(err error) int {
	switch {
	case errors.Is(err, assistant.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, assistant.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, assistant.ErrBusy), errors.Is(err, assistant.ErrStale):
		return http.StatusConflict
	case errors.Is(err, assistant.ErrNoChoice):
		return http.StatusConflict
	case errors.Is(err, assistant.ErrNotYours):
		return http.StatusForbidden
	case errors.Is(err, assistant.ErrNotEnabled):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func (s *Server) getAssistant(w http.ResponseWriter, r *http.Request) {
	st, err := s.Assistant.Status(r.Context(), auth.UserID(r.Context()), r.URL.Query().Get("workshop"))
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putAssistantChoice(w http.ResponseWriter, r *http.Request) {
	var body AssistantChoiceRequest
	if !readJSON(w, r, &body) {
		return
	}
	c := assistant.Choice{Integration: body.Integration, Model: body.Model}
	if err := s.Assistant.SetChoice(r.Context(), auth.UserID(r.Context()), c); err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	c.UserID = auth.UserID(r.Context())
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) openAssistantSession(w http.ResponseWriter, r *http.Request) {
	var body AssistantOpenRequest
	if !readJSON(w, r, &body) {
		return
	}
	if body.Workshop != "" && !s.projectIn(w, r, authz.ProjectRead, body.Workshop) {
		return
	}
	sess, msgs, err := s.Assistant.Open(r.Context(), auth.UserID(r.Context()), assistant.Kind(body.Kind), body.Workshop, body.Subject)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	if msgs == nil {
		msgs = []assistant.Message{}
	}
	writeJSON(w, http.StatusOK, AssistantSessionView{Session: sess, Messages: msgs, Busy: s.Assistant.Busy(sess.ID)})
}

func (s *Server) listAssistantMessages(w http.ResponseWriter, r *http.Request) {
	var after int64
	if q := r.URL.Query().Get("after"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid after")
			return
		}
		after = v
	}
	msgs, err := s.Assistant.Messages(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), after)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	if msgs == nil {
		msgs = []assistant.Message{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) sendAssistantMessage(w http.ResponseWriter, r *http.Request) {
	var body AssistantMessageRequest
	if !readJSON(w, r, &body) {
		return
	}
	m, err := s.Assistant.Send(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), body.Text, body.Context)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

func (s *Server) cancelAssistantTurn(w http.ResponseWriter, r *http.Request) {
	if err := s.Assistant.Cancel(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putAssistantModel(w http.ResponseWriter, r *http.Request) {
	var body AssistantModelRequest
	if !readJSON(w, r, &body) {
		return
	}
	sess, err := s.Assistant.SetModel(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), body.Model)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) putAssistantSubject(w http.ResponseWriter, r *http.Request) {
	var body AssistantSubjectRequest
	if !readJSON(w, r, &body) {
		return
	}
	sess, err := s.Assistant.Rebind(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), body.Subject)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) refreshAssistantRepos(w http.ResponseWriter, r *http.Request) {
	var body AssistantRefreshRequest
	if !readJSON(w, r, &body) {
		return
	}
	if !s.projectIn(w, r, authz.ProjectRead, body.Workshop) {
		return
	}
	views, err := s.Assistant.Refresh(r.Context(), auth.UserID(r.Context()), body.Workshop)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	if views == nil {
		views = []assistant.RepoView{}
	}
	writeJSON(w, http.StatusOK, views)
}

// fillAssistantSession is the fill tool: only the session's own token,
// through the MCP face.
func (s *Server) fillAssistantSession(w http.ResponseWriter, r *http.Request) {
	var body AssistantFillRequest
	if !readJSON(w, r, &body) {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	tokenID := ""
	if p.Token != nil {
		tokenID = p.Token.ID
	}
	errs, err := s.Assistant.Fill(r.Context(), tokenID, p.User.ID, r.PathValue("id"), body.Fill)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusOK, AssistantFillResult{Errors: errs})
		return
	}
	writeJSON(w, http.StatusOK, AssistantFillResult{OK: true})
}

// AssistantActsRequest is the request tool's call: acts for the person to
// confirm.
type AssistantActsRequest struct {
	Why  string           `json:"why" doc:"One line for the person: what the acts are for."`
	Acts []AssistantActIn `json:"acts"`
}

// AssistantActIn is one act: a write tool of the MCP face and its arguments.
type AssistantActIn struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) requestAssistantActs(w http.ResponseWriter, r *http.Request) {
	var body AssistantActsRequest
	if !readJSON(w, r, &body) {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	tokenID := ""
	if p.Token != nil {
		tokenID = p.Token.ID
	}
	req, errs := s.checkActs(body)
	if len(errs) > 0 {
		writeJSON(w, http.StatusOK, AssistantFillResult{Errors: errs})
		return
	}
	errs, err := s.Assistant.Propose(r.Context(), tokenID, p.User.ID, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, assistantStatus(err), err.Error())
		return
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusOK, AssistantFillResult{Errors: errs})
		return
	}
	writeJSON(w, http.StatusOK, AssistantFillResult{OK: true})
}

// checkActs holds each act to the face's write tools: one that exists, is
// not a read, not the assistant's own, and whose arguments fit its input.
func (s *Server) checkActs(body AssistantActsRequest) (assistant.Request, []string) {
	all, err := mcpCatalog()
	if err != nil {
		return assistant.Request{}, []string{err.Error()}
	}
	out := assistant.Request{Why: strings.TrimSpace(body.Why), State: assistant.RequestPending}
	var errs []string
	for i, a := range body.Acts {
		j := slices.IndexFunc(all, func(t *mcpTool) bool { return t.def.Name == a.Tool })
		if j < 0 || boundRoute(all[j].route.ID) || readRoute(all[j].route) {
			errs = append(errs, fmt.Sprintf("act %d: %q is not a write tool of the platform", i+1, a.Tool))
			continue
		}
		t := all[j]
		if t.route.Enabled != nil && !t.route.Enabled(s) {
			errs = append(errs, fmt.Sprintf("act %d: %s is not served here", i+1, a.Tool))
			continue
		}
		args := a.Arguments
		if args == nil {
			args = map[string]any{}
		}
		if err := validateArgs(t, args); err != nil {
			schema, _ := json.Marshal(t.def.InputSchema)
			errs = append(errs, fmt.Sprintf("act %d: %s's arguments: %v. Its input: %s", i+1, a.Tool, err, schema))
			continue
		}
		raw, _ := json.Marshal(args)
		out.Acts = append(out.Acts, assistant.Act{Tool: a.Tool, Title: t.route.Summary, Arguments: raw})
	}
	return out, errs
}

// validateArgs checks arguments against a tool's input schema.
func validateArgs(t *mcpTool, args map[string]any) error {
	raw, err := json.Marshal(t.def.InputSchema)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("tool.json", doc); err != nil {
		return err
	}
	sch, err := c.Compile("tool.json")
	if err != nil {
		return err
	}
	// Through JSON: the validator wants JSON's own types.
	data, _ := json.Marshal(args)
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(v)
}

// decideAssistantRequest confirms (each act run as the caller, through its
// route) or declines a request.
func (s *Server) decideAssistantRequest(confirm bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
		if err != nil || seq < 1 {
			writeErr(w, http.StatusBadRequest, "invalid seq")
			return
		}
		all, err := mcpCatalog()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		run := func(ctx context.Context, a assistant.Act) (int, string) {
			j := slices.IndexFunc(all, func(t *mcpTool) bool { return t.def.Name == a.Tool })
			if j < 0 || boundRoute(all[j].route.ID) || readRoute(all[j].route) {
				return http.StatusBadRequest, a.Tool + " is not a write tool"
			}
			t := *all[j]
			served := false
			for _, rt := range s.routes() {
				if rt.ID == t.route.ID && (rt.Enabled == nil || rt.Enabled(s)) {
					t.route, served = rt, true
				}
			}
			if !served {
				return http.StatusServiceUnavailable, a.Tool + " is not served here"
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(a.Arguments, &args); err != nil {
				return http.StatusBadRequest, err.Error()
			}
			// As the person: their session's cookie (with what proved it
			// same-origin) or their own token.
			res, status := s.runTool(ctx, r, &t, args, "Authorization", "Cookie", "Origin", "Referer", "Sec-Fetch-Site", "User-Agent")
			if status == 0 {
				status = http.StatusBadRequest
			}
			text := ""
			if len(res.Content) > 0 {
				text = res.Content[0].Text
			}
			return status, text
		}
		m, err := s.Assistant.Decide(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), seq, confirm, run)
		if err != nil {
			writeErr(w, assistantStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, m)
	}
}

func (s *Server) streamAssistant(w http.ResponseWriter, r *http.Request) {
	// No position: from now. A panel reads what it opens from the record;
	// what the stream kept is for a reconnect, which names where it was.
	after := int64(-1)
	if q := r.URL.Query().Get("after"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil || v < 0 {
			writeErr(w, http.StatusBadRequest, "invalid after")
			return
		}
		after = v
	}
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if v, err := strconv.ParseInt(h, 10, 64); err == nil && v > after {
			after = v
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	backlog, gap, live, stop := s.Assistant.Watch(auth.UserID(r.Context()), after)
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(f assistant.Frame) {
		data, err := json.Marshal(f)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Kind, data)
		flusher.Flush()
	}
	// Where this stream starts: a reconnect resumes after it.
	fmt.Fprintf(w, "event: head\ndata: {\"seq\":%d}\n\n", s.Assistant.Head())
	if gap {
		// Frames past after are gone: the panel reloads its sessions.
		fmt.Fprintf(w, "event: gap\ndata: {}\n\n")
	}
	for _, f := range backlog {
		send(f)
	}
	flusher.Flush()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.draining():
			return
		case f := <-live:
			send(f)
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// assistantRoutes is the assistant beside the pages.
func (s *Server) assistantRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/assistant", ID: "getAssistant", Tag: tagAssistant,
			Summary: "One's assistant: whether it can run and why not, its harness and models, its pod, a workshop's repositories",
			Query:   []Param{{Name: "workshop", Doc: "the workshop whose repositories to report"}},
			Handler: s.getAssistant, Returns: assistant.Status{}, Enabled: haveAssistant, Action: authz.AssistantRead},
		{Method: "PUT", Path: "/api/assistant/choice", ID: "setAssistantChoice", Tag: tagAssistant,
			Summary: "Pick the harness integration (one's own) the assistant runs on, and the model it starts with",
			Handler: s.putAssistantChoice, Body: AssistantChoiceRequest{}, Returns: assistant.Choice{}, Enabled: haveAssistant, Action: authz.AssistantWrite},
		{Method: "POST", Path: "/api/assistant/sessions", ID: "openAssistantSession", Tag: tagAssistant,
			Summary: "Open the session about a subject: one's latest about it, or a new one",
			Doc:     "kind is flow, ticket or skill; subject the flow's name, the ticket's key, the skill's id, or a draft id before the first save. A flow's or a ticket's session names its workshop.",
			Handler: s.openAssistantSession, Body: AssistantOpenRequest{}, Returns: AssistantSessionView{}, Enabled: haveAssistant, Action: authz.AssistantWrite},
		{Method: "GET", Path: "/api/assistant/sessions/{id}/messages", ID: "listAssistantMessages", Tag: tagAssistant,
			Summary: "A session's messages, after a sequence",
			Query:   []Param{{Name: "after", Doc: "the last sequence already held", Type: "integer"}},
			Handler: s.listAssistantMessages, Returns: []assistant.Message{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantRead},
		{Method: "POST", Path: "/api/assistant/sessions/{id}/messages", ID: "sendAssistantMessage", Tag: tagAssistant,
			Summary: "Ask: the question is recorded, its answer arrives on the stream",
			Doc:     "context is the page as it stands, unsaved edits included. 409 while the session answers, or when no harness is chosen.",
			Handler: s.sendAssistantMessage, Body: AssistantMessageRequest{}, Returns: assistant.Message{}, Status: http.StatusAccepted, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantWrite},
		{Method: "POST", Path: "/api/assistant/sessions/{id}/cancel", ID: "cancelAssistantTurn", Tag: tagAssistant,
			Summary: "Stop the session's answer",
			Handler: s.cancelAssistantTurn, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantWrite},
		{Method: "PUT", Path: "/api/assistant/sessions/{id}/model", ID: "setAssistantModel", Tag: tagAssistant,
			Summary: "Pick the model the session answers with from its next question",
			Handler: s.putAssistantModel, Body: AssistantModelRequest{}, Returns: assistant.Session{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantWrite},
		{Method: "PUT", Path: "/api/assistant/sessions/{id}/subject", ID: "setAssistantSubject", Tag: tagAssistant,
			Summary: "Move a draft's session to what it was saved as",
			Handler: s.putAssistantSubject, Body: AssistantSubjectRequest{}, Returns: assistant.Session{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantWrite},
		{Method: "POST", Path: "/api/assistant/repos/refresh", ID: "refreshAssistantRepos", Tag: tagAssistant,
			Summary: "Fetch a workshop's repositories into one's assistant pod now",
			Handler: s.refreshAssistantRepos, Body: AssistantRefreshRequest{}, Returns: []assistant.RepoView{}, Enabled: haveAssistant, Action: authz.AssistantWrite},
		{Method: "GET", Path: "/api/assistant/stream", ID: "streamAssistant", Tag: tagAssistant,
			Summary: "Server-sent event stream of one's assistant",
			Doc:     "Every session's answers as they are written (delta), messages as they land, a session's state, fills for the page, repository fetches. id is the frame's sequence: resume with after or Last-Event-ID; a gap event says frames past it are gone.",
			Query:   []Param{{Name: "after", Doc: "resume after this frame sequence", Type: "integer"}},
			Handler: s.streamAssistant, Produces: "text/event-stream", Enabled: haveAssistant, Action: authz.AssistantRead},
		{Method: "POST", Path: "/api/assistant/sessions/{id}/fill", ID: "fillAssistantSession", Tag: tagAssistant,
			Summary: "Fill the session's page: checked, then sent to the page once; nothing is saved",
			Doc:     "Only the session's own token (the one its agent holds) fills it: 403 for any other. A fill that fails its check is ok false with its errors.",
			Handler: s.fillAssistantSession, Body: AssistantFillRequest{}, Returns: AssistantFillResult{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantFill,
			Tool: &Tool{Name: "fill", Doc: "Fill the person's page with what you propose: it appears in their form or editor for them to read, change and save. Nothing is saved. Answers ok, or the errors to correct.",
				Args: map[string]string{"id": "Your session's id.", "fill": "The page's fill."}}},
		{Method: "POST", Path: "/api/assistant/sessions/{id}/request", ID: requestRouteID, Tag: tagAssistant,
			Summary: "Ask the person to confirm acts: each a write tool of the MCP face, with its arguments; nothing runs until they do",
			Doc: "Only the session's own token, while its question is answered. Each act's tool must be a write tool and its arguments must fit its input; ok false with the errors otherwise. " +
				"The person confirms or declines in the panel; the session's next prompt says what came of it.",
			Handler: s.requestAssistantActs, Body: AssistantActsRequest{}, Returns: AssistantFillResult{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantFill,
			Tool: &Tool{Name: "request", Doc: "Ask the person to confirm acts on the platform: filing tickets, starting or steering runs, saving flows, skills, automations or dashboards. Each act is one of the platform's write tools (create_ticket, update_ticket, start_run, pause_run, resume_run, retry_run, abort_run, create_flow, update_flow, create_automation, update_automation, create_skill, update_skill, create_skillset, update_skillset, create_dashboard, update_dashboard, create_scorecard, update_scorecard) with the arguments that tool takes. why is one line for the person. Nothing runs until they confirm; their next message says what came of it. Answers ok, or the errors to correct (a wrong argument names the tool's input).",
				Args: map[string]string{"id": "Your session's id."}}},
		{Method: "POST", Path: "/api/assistant/sessions/{id}/messages/{seq}/confirm", ID: "confirmAssistantRequest", Tag: tagAssistant,
			Summary: "Run the acts an answer asked for, in order, as the caller, until one fails",
			Doc:     "Each act goes through its route as if the caller called it: their actions decide. Answers the message with each act's status and result; 409 for a request decided already or being decided.",
			Handler: s.decideAssistantRequest(true), Returns: assistant.Message{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantWrite},
		{Method: "POST", Path: "/api/assistant/sessions/{id}/messages/{seq}/decline", ID: "declineAssistantRequest", Tag: tagAssistant,
			Summary: "Decline the acts an answer asked for: none runs",
			Handler: s.decideAssistantRequest(false), Returns: assistant.Message{}, Enabled: haveAssistant, Scope: ScopeAssistant, Action: authz.AssistantWrite},
	}
}
