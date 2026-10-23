package httpapi

// The Reviews tab: a project repository's open merge requests, each with
// the review the factory has done of it — the merge request's ticket and its
// latest run — and a way to start one. Starting one finds or makes the merge
// request's ticket (the same one a forge_mr automation would use) and
// creates a run on the merge request at a flow's mr_opened entrypoint.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/engine"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// ReviewRow is one open merge request on the Reviews tab.
type ReviewRow struct {
	MR ports.MRInfo `json:"mr"`
	// Mine: the forge integration's own account opened it — the factory's
	// own runs did — so it is not one to review here.
	Mine bool `json:"mine,omitempty"`
	// TicketID is the merge request's ticket, once a review was started or
	// an automation picked it; Run its newest run, Runs how many it has.
	TicketID string     `json:"ticket_id,omitempty"`
	Run      *ReviewRun `json:"run,omitempty"`
	Runs     int        `json:"runs,omitempty"`
}

// ReviewRun is the newest run of a merge request's ticket.
type ReviewRun struct {
	ID        string    `json:"id"`
	State     run.State `json:"state"`
	Flow      string    `json:"flow"`
	CreatedAt time.Time `json:"created_at"`
}

// ReviewRequest starts a review: a run of Flow ("" = the built-in reviewer)
// at its mr_opened entrypoint Phase ("" = the first), on the merge request,
// in Repo ("" = the primary).
type ReviewRequest struct {
	Repo   string            `json:"repo,omitempty"`
	Flow   string            `json:"flow,omitempty"`
	Phase  string            `json:"phase,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// ReviewStarted names the run a review started and the ticket it belongs to.
type ReviewStarted struct {
	RunID    string `json:"run_id"`
	TicketID string `json:"ticket_id"`
}

// defaultReviewFlow is what a review runs when the request names no flow.
const defaultReviewFlow = "reviewer"

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, repo := r.PathValue("id"), r.URL.Query().Get("repo")
	mrs, err := s.Engine.ListMRs(ctx, projectID, repo)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Who the factory is on this forge: best effort — without it no row is
	// marked as the factory's own.
	me, _ := s.Engine.ForgeUser(ctx, projectID, repo)
	all, err := s.Conf.ListTickets(ctx)
	if err != nil {
		confErr(w, err)
		return
	}
	byKey := map[string]string{}
	for _, t := range all {
		if t.ProjectID == projectID && t.SourceKey != "" {
			byKey[strings.ToLower(t.SourceKey)] = t.ID
		}
	}
	out := make([]ReviewRow, 0, len(mrs))
	for _, mr := range mrs {
		row := ReviewRow{MR: mr, Mine: me != "" && strings.EqualFold(mr.Author, me), TicketID: byKey[strings.ToLower(mr.Ref.URL)]}
		if row.TicketID != "" {
			runs, err := s.Engine.RunsOfTicket(ctx, row.TicketID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			row.Runs = len(runs)
			for _, v := range runs {
				if row.Run == nil || v.CreatedAt.After(row.Run.CreatedAt) {
					row.Run = &ReviewRun{ID: v.RunID, State: v.State, Flow: v.FlowName, CreatedAt: v.CreatedAt}
				}
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := r.PathValue("id")
	iid, err := strconv.Atoi(r.PathValue("iid"))
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "iid must be a merge request number")
		return
	}
	var body ReviewRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "body: "+err.Error())
			return
		}
	}
	mr, err := s.Engine.MR(ctx, projectID, body.Repo, iid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if mr.State != ports.MROpen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("merge request %s is %s: only an open one is reviewed", mr.Ref.URL, mr.State))
		return
	}
	t, created, err := s.tickets().ForMR(ctx, projectID, mr)
	if err != nil {
		confErr(w, err)
		return
	}
	// A ticket made for a start that is then refused goes with the refusal:
	// nothing should be left behind for a review that never began.
	keep := false
	defer func() {
		if created && !keep {
			if derr := s.tickets().Delete(ctx, t.ID); derr != nil {
				s.Log.Warn("drop the ticket of a refused review", "ticket", t.ID, "err", derr)
			}
		}
	}()
	// One review at a time: a second while the first runs would post twice.
	runs, err := s.Engine.RunsOfTicket(ctx, t.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range runs {
		if !v.State.Terminal() {
			writeErr(w, http.StatusConflict, fmt.Sprintf("a review of %s is already running (run %s)", mr.Ref.URL, v.RunID))
			return
		}
	}
	flowName := body.Flow
	if flowName == "" {
		flowName = defaultReviewFlow
	}
	runID, err := s.Engine.CreateRunWith(ctx, engine.Ticket{
		TicketID: t.ID, Title: t.Title, Goal: t.Goal,
		SourceKind: t.SourceKind, SourceKey: t.SourceKey, SourceURL: t.SourceURL,
	}, flowName, projectID, engine.RunInput{Phase: body.Phase, Params: body.Params, MR: &engine.RunMR{Repo: body.Repo, IID: iid}})
	switch {
	case errors.Is(err, engine.ErrEnvCapacity):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		keep = true
		writeJSON(w, http.StatusCreated, ReviewStarted{RunID: runID, TicketID: t.ID})
	}
}
