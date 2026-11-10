package httpapi

// A project repository's merge requests as a source of runs, the way the
// tracker's issues are tickets: each open merge request with the runs that
// went through it, and a way to start a run of any flow whose runs are
// started on one (operates_on: merge_request). A merge request is no ticket
// and gets none: its runs are found by what they worked on. Which flow — a
// review, a rebase, anything — is the person's pick; nothing here knows one
// from another.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/skills"
	"apothecary/internal/workitem/mergerequest"
)

// MRRow is one merge request: what the repository host says of it, what it
// is stacked on, and the runs that went through it here.
type MRRow struct {
	MR repoport.MRInfo `json:"mr"`
	// StackedOn is the open merge request it targets the branch of: it
	// merges into that branch, not the repository's main line.
	StackedOn *repoport.MRBase `json:"stacked_on,omitempty"`
	// Runs is how many runs have it in their data — started on it, or
	// opened it — and Run the newest of them.
	Run  *MRRun `json:"run,omitempty"`
	Runs int    `json:"runs,omitempty"`
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

// MRRunStarted names the run started.
type MRRunStarted struct {
	RunID string `json:"run_id"`
}

func (s *Server) listProjectMRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, repo := r.PathValue("id"), r.URL.Query().Get("repo")
	mrs, err := s.MRs.ListMRs(ctx, projectID, repo)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	byKey, err := s.Runs.ItemRuns(ctx, projectID, mergerequest.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]MRRow, 0, len(mrs))
	for _, mr := range mrs {
		out = append(out, mrRow(mr, mrs, byKey[mergerequest.Key(repo, mr.Ref.IID)]))
	}
	writeJSON(w, http.StatusOK, out)
}

// getProjectMR is one merge request, open or not, with its description:
// what a run started on it is given.
func (s *Server) getProjectMR(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, repo := r.PathValue("id"), r.URL.Query().Get("repo")
	iid, err := strconv.Atoi(r.PathValue("iid"))
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "iid must be a merge request number")
		return
	}
	mr, err := s.MRs.MR(ctx, projectID, repo, iid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// What it is stacked on is among the open ones; without them it is
	// only not said.
	open, _ := s.MRs.ListMRs(ctx, projectID, repo)
	runs, err := s.Runs.RunsOf(ctx, mergerequest.Ref(projectID, repo, iid))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mrRow(mr, open, runs))
}

// mrRow is a merge request with what it is stacked on among open, and the
// newest of the runs that went through it.
func mrRow(mr repoport.MRInfo, open []repoport.MRInfo, runs []run.View) MRRow {
	row := MRRow{MR: mr, StackedOn: repoport.StackedOn(mr.Target, open), Runs: len(runs)}
	for _, v := range runs {
		if row.Run == nil || v.CreatedAt.After(row.Run.CreatedAt) {
			row.Run = &MRRun{ID: v.RunID, State: v.State, Flow: v.FlowName, CreatedAt: v.CreatedAt}
		}
	}
	return row
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
	if err := mergerequest.CheckOpen(mr); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	flowName := body.Flow
	if flowName == "" {
		p, err := s.Conf.GetProject(ctx, projectID)
		if err != nil {
			confErr(w, err)
			return
		}
		flowName = p.DefaultFlow
	}
	// A second run on the merge request while one is still working on it —
	// a double click, or another flow — is refused by the engine: 409
	// naming the run.
	runID, err := s.Runs.CreateRunWith(ctx, mergerequest.Work(mr), flowName, projectID,
		runinput.Input{Params: body.Params, MR: &runinput.MR{Repo: body.Repo, IID: iid}, Skillsets: body.Skillsets})
	if err != nil {
		runStartErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, MRRunStarted{RunID: runID})
}
