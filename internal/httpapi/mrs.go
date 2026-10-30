package httpapi

// A project repository's merge requests as a source of runs, the way the
// tracker's issues are tickets: each open merge request with the runs done on
// it — through its ticket, the one a merge_request automation would use too —
// and a way to start a run of any flow whose runs are started on one
// (operates_on: merge_request). Which flow — a review, a rebase, anything — is
// the person's
// pick; nothing here knows one from another.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/auth"
	"apothecary/internal/engine"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/skills"
)

// MRRow is one open merge request: what the repository host says of it,
// whether the factory itself opened it, what it is stacked on, and the runs
// done on it here.
type MRRow struct {
	MR ports.MRInfo `json:"mr"`
	// Mine: the repository host integration's own account opened it (the
	// factory's own runs did).
	Mine bool `json:"mine,omitempty"`
	// StackedOn is the open merge request it targets the branch of: it
	// merges into that branch, not the repository's main line.
	StackedOn *ports.MRBase `json:"stacked_on,omitempty"`
	// TicketID is the merge request's ticket, once a run was started on it
	// or an automation picked it; Run its newest run, Runs how many it has.
	TicketID string `json:"ticket_id,omitempty"`
	Run      *MRRun `json:"run,omitempty"`
	Runs     int    `json:"runs,omitempty"`
}

// MRRun is the newest run on a merge request.
type MRRun struct {
	ID        string    `json:"id"`
	State     run.State `json:"state"`
	Flow      string    `json:"flow"`
	CreatedAt time.Time `json:"created_at"`
}

// MRRunRequest starts a run on a merge request: of Flow ("" = the project's
// default flow), which must start its runs on one (operates_on:
// merge_request), with Params over the project's bindings, on the merge
// request in Repo ("" = the primary).
type MRRunRequest struct {
	Repo   string            `json:"repo,omitempty"`
	Flow   string            `json:"flow,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	// Skillsets to install into the workspace (seq 0 = latest).
	Skillsets []skills.Ref `json:"skillsets,omitempty"`
}

// MRRunStarted names the run started and the ticket it belongs to.
type MRRunStarted struct {
	RunID    string `json:"run_id"`
	TicketID string `json:"ticket_id"`
}

func (s *Server) listProjectMRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, repo := r.PathValue("id"), r.URL.Query().Get("repo")
	mrs, err := s.MRs.ListMRs(ctx, projectID, repo)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Who the factory is on this repository host: best effort — without it no
	// row is marked as the factory's own.
	me, _ := s.MRs.RepoHostUser(ctx, projectID, repo)
	byKey := map[string]string{}
	if s.Conf != nil {
		all, err := s.Conf.ListTickets(ctx, auth.UserID(ctx))
		if err != nil {
			confErr(w, err)
			return
		}
		for _, t := range all {
			if t.ProjectID == projectID && t.SourceKey != "" {
				byKey[strings.ToLower(t.SourceKey)] = t.ID
			}
		}
	}
	out := make([]MRRow, 0, len(mrs))
	for _, mr := range mrs {
		row := MRRow{MR: mr, Mine: me != "" && strings.EqualFold(mr.Author, me), TicketID: byKey[strings.ToLower(mr.Ref.URL)],
			StackedOn: ports.StackedOn(mr.Target, mrs)}
		if row.TicketID != "" {
			runs, err := s.Runs.RunsOfTicket(ctx, row.TicketID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			row.Runs = len(runs)
			for _, v := range runs {
				if row.Run == nil || v.CreatedAt.After(row.Run.CreatedAt) {
					row.Run = &MRRun{ID: v.RunID, State: v.State, Flow: v.FlowName, CreatedAt: v.CreatedAt}
				}
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startMRRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := r.PathValue("id")
	iid, err := strconv.Atoi(r.PathValue("iid"))
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "iid must be a merge request number")
		return
	}
	var body MRRunRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "body: "+err.Error())
			return
		}
	}
	mr, err := s.MRs.MR(ctx, projectID, body.Repo, iid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if mr.State != ports.MROpen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("merge request %s is %s: a run starts on an open one", mr.Ref.URL, mr.State))
		return
	}
	t, created, err := s.tickets().ForMR(ctx, projectID, mr)
	if err != nil {
		confErr(w, err)
		return
	}
	// A ticket made for a start that is then refused goes with the refusal:
	// nothing is left behind for a run that never began.
	keep := false
	defer func() {
		if created && !keep {
			if derr := s.tickets().Delete(ctx, t.ID); derr != nil {
				s.Log.Warn("drop the ticket of a refused run", "ticket", t.ID, "err", derr)
			}
		}
	}()
	flowName := body.Flow
	if flowName == "" {
		p, err := s.Conf.GetProject(ctx, projectID)
		if err != nil {
			confErr(w, err)
			return
		}
		flowName = p.DefaultFlow
	}
	// The same flow twice at once on one merge request is a double click, not
	// a plan: refused while one of its runs is going. Other flows may run.
	runs, err := s.Runs.RunsOfTicket(ctx, t.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range runs {
		if !v.State.Terminal() && v.FlowName == flowName {
			writeErr(w, http.StatusConflict, fmt.Sprintf("a run of %s on %s is already going (run %s)", flowName, mr.Ref.URL, v.RunID))
			return
		}
	}
	runID, err := s.Runs.CreateRunWith(ctx, engine.Ticket{
		TicketID: t.ID, Title: t.Title, Goal: t.Goal, ChangeType: string(t.ChangeType),
		SourceKind: t.SourceKind, SourceKey: t.SourceKey, SourceURL: t.SourceURL,
	}, flowName, projectID, engine.RunInput{Params: body.Params, MR: &engine.RunMR{Repo: body.Repo, IID: iid}, Skillsets: body.Skillsets})
	switch {
	case errors.Is(err, engine.ErrEnvCapacity):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		keep = true
		writeJSON(w, http.StatusCreated, MRRunStarted{RunID: runID, TicketID: t.ID})
	}
}
