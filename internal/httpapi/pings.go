package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/auth"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/run"
)

// Pings: the shim's ask_human (raise, hold for the answer), answering one,
// and the inbox's list and detail over the durable projection.

// authPing enforces the per-run bearer token on the ping endpoints (§8).
func (s *Server) authPing(w http.ResponseWriter, r *http.Request, runID string) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := s.Runs.RunToken(runID)
	if !ok || want == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		writeErr(w, http.StatusUnauthorized, "missing or invalid run token")
		return false
	}
	return true
}

// PingStore is the durable request/response projection surface (PLAN4
// M15): pgstore in production, EventPingStore over any event.Store in
// tests/tools.
type PingStore interface {
	// ListPings is one user's pings ("" = everyone's).
	ListPings(ctx context.Context, userID, status string, limit int) ([]run.PingView, error)
	GetPing(ctx context.Context, id string) (run.PingView, error)
}

// EventPingStore derives the projection by folding events — parity with
// pgstore's SQL view via the shared run.ReducePings reducer.
type EventPingStore struct{ Store event.Store }

func (e EventPingStore) ListPings(ctx context.Context, userID, status string, limit int) ([]run.PingView, error) {
	ids, err := e.Store.RunIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []run.PingView
	for _, id := range ids {
		evs, err := e.Store.Events(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		for _, v := range run.ReducePings(evs) {
			if userID != "" && v.UserID != userID {
				continue
			}
			if status == "" || status == "all" || v.Status == status {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RaisedAt.After(out[j].RaisedAt) })
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (e EventPingStore) GetPing(ctx context.Context, id string) (run.PingView, error) {
	all, err := e.ListPings(ctx, "", "all", 500)
	if err != nil {
		return run.PingView{}, err
	}
	for _, v := range all {
		if v.PingID == id {
			return v, nil
		}
	}
	return run.PingView{}, fmt.Errorf("ping %s: %w", id, engine.ErrPingNotFound)
}

// pingJSON is the registry-fallback shape (Pings == nil); the projection
// path serves run.PingView directly.
type pingJSON struct {
	PingID     string            `json:"ping_id"`
	RunID      string            `json:"run_id"`
	Type       string            `json:"type"`
	StageID    string            `json:"stage_id,omitempty"`
	Question   string            `json:"question"`
	Context    string            `json:"context,omitempty"`
	Files      []string          `json:"files,omitempty"`
	FileTypes  map[string]string `json:"file_types,omitempty"`
	Link       string            `json:"link,omitempty"`
	PayloadRef string            `json:"payload_ref,omitempty"`
	Form       *event.Form       `json:"form,omitempty"`
	AnswerKind string            `json:"answer_kind"`
	Phase      string            `json:"phase,omitempty"`
	EndsPhase  bool              `json:"ends_phase,omitempty"`
	Status     string            `json:"status"`
	RaisedAt   time.Time         `json:"raised_at"`
}

func toPingJSON(p *engine.Ping) pingJSON {
	return pingJSON{
		PingID: p.ID, RunID: p.RunID, Type: p.Type, StageID: p.StageID, Question: p.Question,
		Context: p.Context, Files: p.Files, FileTypes: p.FileTypes, Link: p.Link, PayloadRef: p.Primary(),
		Form: p.Form, AnswerKind: p.AnswerKind, Phase: p.Phase, EndsPhase: p.EndsPhase,
		Status: "pending", RaisedAt: p.RaisedAt,
	}
}

// postPing is the ask_human shim entry (§10): raise, then hold for the answer;
// 200 with the answer, else 204 (the ping id travels in the X-Ping-ID header,
// since the 204 has no body).
//
// The hold is a GRACE PERIOD, not the wait: hold_ms lets the shim ask for a
// window short enough to stay well inside any harness's tool-call timeout, so
// an answer that is already waiting costs the agent nothing, and a slower one
// turns the tool call into "stop and wait" — the engine then holds the real
// wait, untimed, and resumes the stage (see engine.stageAgent).
func (s *Server) postPing(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !s.authPing(w, r, runID) {
		return
	}
	var body RaisePingRequest
	if !readJSON(w, r, &body) {
		return
	}
	refs := body.Artifacts
	if len(refs) == 0 && body.Artifact != "" {
		refs = []string{body.Artifact}
	}
	p, err := s.Runs.RaiseQuestion(runID, engine.Question{Key: body.Key, Type: body.Type, Question: body.Question,
		Context: body.Context, Fields: body.Fields, Artifacts: refs})
	switch {
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.holdForAnswer(w, r, p, s.holdFor(body.HoldMS))
}

// holdFor clamps a requested grace hold to the server's own limit. A missing
// or nonsensical value keeps the historical behaviour (hold the maximum).
func (s *Server) holdFor(ms int) time.Duration {
	if ms <= 0 {
		return s.HoldTimeout
	}
	return min(time.Duration(ms)*time.Millisecond, s.HoldTimeout)
}

func (s *Server) repollPing(w http.ResponseWriter, r *http.Request) {
	runID, pid := r.PathValue("id"), r.PathValue("pid")
	if !s.authPing(w, r, runID) {
		return
	}
	p := s.Runs.GetPing(pid)
	if p == nil || p.RunID != runID {
		writeErr(w, http.StatusNotFound, "ping not found")
		return
	}
	s.holdForAnswer(w, r, p, s.holdFor(0))
}

func (s *Server) holdForAnswer(w http.ResponseWriter, r *http.Request, p *engine.Ping, hold time.Duration) {
	w.Header().Set("X-Ping-ID", p.ID)
	ctx, cancel := context.WithTimeout(r.Context(), hold)
	defer cancel()
	// A shutdown ends the hold like an expired one: the shim re-polls.
	go func() {
		select {
		case <-s.draining():
			cancel()
		case <-ctx.Done():
		}
	}()
	if ans, ok := p.Await(ctx); ok {
		// The agent gets the answer in-band: as far as it is concerned the
		// question is closed, so the stage need not pause on it.
		p.HandOff(true)
		// Text is the composed answer, rendered by the engine so the tool
		// return and a resumed stage's message say the same thing; Answer and
		// Feedback stay as the raw fields behind it.
		writeJSON(w, http.StatusOK, PingHold{
			PingID: p.ID, Text: engine.ComposeAnswer(p),
			Answer: ans.Answer, Feedback: ans.Feedback, Content: ans.Content,
			Artifacts: p.DeliveredArtifacts(), // workspace paths of the human's answer files
		})
		return
	}
	if p.HandedBack() {
		// Parked: the engine ended the agent's turn and will deliver the
		// answer by resuming its session. The tool call should stop waiting.
		w.WriteHeader(http.StatusGone)
		return
	}
	// Still open, still live: the ask_human tool call polls again. The wait
	// itself is the agent's (its turn blocks in the call) until the engine
	// decides to park the run.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) answerPing(w http.ResponseWriter, r *http.Request) {
	var body AnswerPingRequest
	if !readJSON(w, r, &body) {
		return
	}
	pid := r.PathValue("pid")
	// Claim the uploaded files for this ping BEFORE AnswerPing records the
	// answer, so delivery (which queries by ping_id) finds them. The run id
	// comes from the live registry when a loop is holding the ping, and from
	// the durable projection when the run is parked on it — a parked run has
	// no registry entry, and its answers are the ones most likely to carry
	// attachments (the human had time to gather them). The ids also ride on
	// the answer, so ping_answered records the association as a replayable
	// fact.
	if s.Attachments != nil && len(body.AttachmentIDs) > 0 {
		runID := ""
		if p := s.Runs.GetPing(pid); p != nil {
			runID = p.RunID
		} else if s.Pings != nil {
			if v, err := s.Pings.GetPing(r.Context(), pid); err == nil {
				runID = v.RunID
			}
		}
		if runID != "" {
			if err := s.Attachments.ClaimForRun(r.Context(), body.AttachmentIDs, runID, pid); err != nil {
				s.Log.Warn("claim answer attachments", "ping", pid, "err", err)
			}
		}
	}
	err := s.Runs.AnswerPing(pid, engine.PingAnswer{Answer: body.Answer, Feedback: body.Feedback,
		Content: body.Content, AttachmentIDs: body.AttachmentIDs, Review: body.Review, Curated: body.Curated})
	switch {
	case errors.Is(err, engine.ErrPingNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrPingAnswered):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, AnswerAccepted{Status: "answered"})
	}
}

func (s *Server) listPings(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", "all", "pending", "answered":
	default:
		writeErr(w, http.StatusBadRequest, "status must be pending|answered|all")
		return
	}
	me := auth.UserID(r.Context())
	if s.Pings == nil {
		// Bare test servers: pending only, from the live registry.
		pings := s.Runs.PendingPings()
		out := make([]pingJSON, 0, len(pings))
		for _, p := range pings {
			if owner, err := s.runOwner(r.Context(), p.RunID); err != nil || owner != me {
				continue
			}
			out = append(out, toPingJSON(p))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].RaisedAt.Before(out[j].RaisedAt) })
		writeJSON(w, http.StatusOK, out)
		return
	}
	if status == "" {
		status = "pending" // inbox default; history is opt-in
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Pings.ListPings(r.Context(), me, status, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == "" || status == "pending" {
		// Pending lists read oldest-first (inbox order).
		sort.Slice(rows, func(i, j int) bool { return rows[i].RaisedAt.Before(rows[j].RaisedAt) })
	}
	if rows == nil {
		rows = []run.PingView{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// getStoredPing serves one durable request/response pair (PLAN4 M15).
func (s *Server) getStoredPing(w http.ResponseWriter, r *http.Request) {
	if s.Pings == nil {
		writeErr(w, http.StatusServiceUnavailable, "ping projection is not wired")
		return
	}
	v, err := s.Pings.GetPing(r.Context(), r.PathValue("pid"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, engine.ErrPingNotFound) || strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// pingRoutes is pings: the shim's questions and a person's answers.
func (s *Server) pingRoutes() []Route {
	return []Route{
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
		{Method: "GET", Path: "/api/pings/{pid}/curation", ID: "getPingCuration", Tag: tagPings,
			Summary: "A question's review documents, decoded as the gate showed them",
			Doc:     "Each shown file of a review type (review_threads, review_replies, mr_threads) from its raise-time snapshot: what the curation page lists item by item. A file not captured lists with the reason and can only be approved as written.",
			Handler: s.getPingCuration, Returns: CurationView{}, Scope: ScopePing},
		{Method: "POST", Path: "/api/pings/{pid}/answer", ID: "answerPing", Tag: tagPings,
			Summary: "Answer a question",
			Handler: s.answerPing, Body: AnswerPingRequest{}, Returns: AnswerAccepted{}, Scope: ScopePing},
	}
}
