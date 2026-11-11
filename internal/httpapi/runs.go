package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/envstate"
)

// Runs: listing, reading and the lifecycle verbs (abort, pause, play,
// archive, rerun, retry, rebuild, phases) a person applies to a run.

// runViews is every run's read-model row (run.Reader.All: the store's
// projection when it keeps one, else every log reduced).
func (s *Server) runViews(ctx context.Context) ([]run.View, error) {
	return (&run.Reader{Store: s.Store}).All(ctx)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	ticket := r.URL.Query().Get("ticket")
	// One snapshot of the reconciler's live-status map (overlaid with any
	// in-flight op) for the whole list — the workspace badge reads env_status
	// to tell a live env from a stopped one, and to show rebuilding/destroying/
	// error transitions (env_id alone can't, see run.View.EnvStatus).
	envStatus := s.EnvState.Badges()
	envDirty := s.EnvState.Dirty()
	// Scoped to the caller (and project/ticket) in the store's query, newest
	// first: a list never reads someone else's runs.
	views, err := (&run.Reader{Store: s.Store}).List(r.Context(),
		run.ListFilter{UserID: auth.UserID(r.Context()), ProjectID: project, TicketID: ticket})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range views {
		// Overlay may carry a provisioning/error op before env_id exists, so
		// don't gate on EnvID here.
		if st, ok := envStatus[views[i].RunID]; ok {
			views[i].EnvStatus = st
		}
		views[i].EnvDirty = envDirty[views[i].RunID]
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	evs, ok := s.runEvents(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	last := evs
	if len(last) > 100 {
		last = last[len(last)-100:]
	}
	v := run.Reduce(evs)
	if st, ok := s.EnvState.Badges()[v.RunID]; ok {
		v.EnvStatus = st
	}
	v.EnvDirty = s.EnvState.Dirty()[v.RunID]
	writeJSON(w, http.StatusOK, RunDetail{Run: v, Events: last})
}

func (s *Server) postAbort(w http.ResponseWriter, r *http.Request) {
	err := s.Runs.Abort(r.PathValue("id"))
	switch {
	case err != nil && strings.Contains(err.Error(), "not found"):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// postPause parks an active run (202); 409 when it has no live goroutine.
func (s *Server) getRunPhases(w http.ResponseWriter, r *http.Request) {
	out, err := s.Runs.RunPhases(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// postStartPhase starts a phase by hand (202); 409 when the run is not idle
// or the phase cannot start (parked, past its max, no such merge request).
// The body is optional: {subject} names the checkout whose merge request
// the phase acts on.
func (s *Server) postStartPhase(w http.ResponseWriter, r *http.Request) {
	var body StartPhaseRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	err := s.Runs.StartPhase(r.PathValue("id"), r.PathValue("phase"), body.Subject)
	switch {
	case errors.Is(err, engine.ErrPhaseNotStartable):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *Server) postPause(w http.ResponseWriter, r *http.Request) {
	err := s.Runs.Pause(r.PathValue("id"))
	switch {
	case errors.Is(err, engine.ErrRunNotActive):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// postPlay restarts a paused run (202, same id); 409 when active or not
// paused, 404 when unknown.
func (s *Server) postPlay(w http.ResponseWriter, r *http.Request) {
	err := s.Runs.Play(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case strings.Contains(msg, "still active"), strings.Contains(msg, "can be played"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) postArchive(w http.ResponseWriter, r *http.Request) {
	if err := s.Runs.Archive(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// postRerun starts a terminal run over as a fresh run; 201 carries the
// new run id. 409 when the run is active/non-terminal, or another run of its
// ticket is (run_id names that one), 404 when unknown, 400 when the
// re-preflight refuses (config drifted since the original).
// The optional body {flow, goal, attachment_ids} steers THIS attempt —
// a rerun after a failure is when a human most wants to give direction.
func (s *Server) postRerun(w http.ResponseWriter, r *http.Request) {
	var body RerunRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	newID, err := s.Runs.Rerun(r.Context(), r.PathValue("id"), runinput.Input{
		Flow: body.Flow, Goal: body.Goal, SourceBranch: body.SourceBranch, AttachmentIDs: body.AttachmentIDs,
		Params: body.Params, MR: body.MR, Skillsets: body.Skillsets,
	})
	var busy runBusy
	if errors.As(err, &busy) {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error(), RunID: busy.ActiveRunID()})
		return
	}
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case strings.Contains(msg, "still active"), strings.Contains(msg, "only terminal"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusCreated, RunRef{RunID: newID})
}

// postRetry re-enters the failed stage of an abandoned/failed run in its
// existing workspace ("try again") — 202, the run continues under the same
// id. 409 when active/not retryable, 404 when unknown.
func (s *Server) postRetry(w http.ResponseWriter, r *http.Request) {
	err := s.Runs.RetryRun(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case strings.Contains(msg, "still active"), strings.Contains(msg, "can be retried"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// postRebuild recreates the run's workspace container in place (keeps the run
// + its commits, re-runs bootstrap). Blocks until done — the run page shows
// progress via the workspace-log tail and the rebuilding badge.
func (s *Server) postRebuild(w http.ResponseWriter, r *http.Request) {
	// The body is optional: a plain rebuild is the common case, and an older
	// client that sends nothing still gets one.
	var body RebuildRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	err := s.Runs.RebuildEnv(r.Context(), r.PathValue("id"), body.NoCache)
	if err != nil {
		msg := err.Error()
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		case errors.Is(err, envstate.ErrInFlight), errors.Is(err, engine.ErrRunActive),
			strings.Contains(msg, "does not support"), strings.Contains(msg, "no workspace"),
			strings.Contains(msg, "no project"):
			status = http.StatusConflict
		}
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// runRoutes is tickets' runs: create, read, stream and the lifecycle verbs.
func (s *Server) runRoutes() []Route {
	return []Route{
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
			Doc:     "Replays from after_seq, then streams live. The stream ends at the event that puts the run in a terminal state; a connection resuming past it stays open for what comes later (a retry, the workspace's teardown).",
			Query:   []Param{{Name: "after_seq", Doc: "resume after this sequence number", Type: "integer"}},
			Handler: s.streamEvents, Produces: "text/event-stream", Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/logs/{stage}", ID: "getStageLog", Tag: tagRuns,
			Summary: "A stage's captured log (latest attempt)",
			Query:   tailQuery, Handler: s.getLog, Produces: "text/plain", Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/workspace-log", ID: "getWorkspaceLog", Tag: tagWorkspace,
			Summary: "A range of the workspace's live exec log",
			Doc:     "Without from: the last n bytes. With from: up to n bytes from that offset — the end of the last read, to keep up, or a range ending at the start of what is on screen, to go back. A size below the offset kept up to means the log was replaced: read the tail again.",
			Query: []Param{
				{Name: "from", Doc: "byte offset to read from (default: the tail)", Type: "integer"},
				{Name: "n", Doc: "bytes to read (default 64 KiB, at most 8 MiB)", Type: "integer"},
			},
			Handler: s.getWorkspaceLog, Returns: WorkspaceLog{}, Scope: ScopeRun},

		{Method: "POST", Path: "/api/runs/{id}/abort", ID: "abortRun", Tag: tagRuns,
			Summary: "Stop a run for good", Handler: s.postAbort, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/phases", ID: "getRunPhases", Tag: tagRuns,
			Summary: "Every phase of the run's flow: what opens it, its runs per merge request, its live run and the signals holding for it",
			Handler: s.getRunPhases, Returns: []engine.PhaseInfo{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/phases/{phase}/start", ID: "startPhase", Tag: tagRuns,
			Summary: "Start a phase now, by hand",
			Doc:     "Only while the cursor is at the hub: no chain is running or parked at a wait point. The phase's signal need not hold — a person's start is its own decision — and its max still applies. Acts on the named checkout's merge request, else the run's single open one, else on none.",
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
	}
}
