package httpapi

// A run started on nothing (FLOWLANG §1 operates_on: nothing): a workspace
// on a branch, with no ticket and no merge request behind it — a person's
// own sandbox. It is started from the project, named or not, on a fresh
// branch forked from one of the repository's — named after the run, or as
// the start says — or on that branch itself.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/flow"
	"apothecary/internal/run/runinput"
	"apothecary/internal/skills"
)

// NothingRunRequest starts a run on nothing: of Flow ("" = the project's
// default flow), which must start its runs on nothing (operates_on:
// nothing), with Params over the project's bindings. Everything is optional.
type NothingRunRequest struct {
	// Title names the run ("" = "manual run - " and the time, UTC).
	Title string `json:"title,omitempty"`
	Flow  string `json:"flow,omitempty"`
	// Branch is the branch the run starts from ("" = the repository's
	// default branch).
	Branch string `json:"branch,omitempty"`
	// OnBranch works on Branch itself. Without it the run gets a fresh
	// branch of its own, forked from Branch, that exists only in the
	// workspace until something pushes it.
	OnBranch bool `json:"on_branch,omitempty"`
	// NewBranch names that fresh branch, used as written ("" = named after
	// the run, with the run's suffix). Not with OnBranch, and not Branch's
	// own name: working on an existing branch is what OnBranch says.
	NewBranch string            `json:"new_branch,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	// Skillsets are the caller's to install into the workspace (seq 0 = latest).
	Skillsets []skills.Ref `json:"skillsets,omitempty"`
}

// manualRunTitle is what a run on nothing is called when no one named it.
func manualRunTitle(now time.Time) string {
	return "manual run - " + now.UTC().Format("2006-01-02 15:04")
}

func (s *Server) startNothingRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := r.PathValue("id")
	var body NothingRunRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "body: "+err.Error())
			return
		}
	}
	p, err := s.Conf.GetProject(ctx, projectID)
	if err != nil {
		confErr(w, err)
		return
	}
	flowName := body.Flow
	if flowName == "" {
		flowName = p.DefaultFlow
	}
	// The engine takes a bare start for a ticket flow too (a ticket is not
	// required of one): here, the flow must be one that starts on nothing.
	f, err := s.Conf.ParsedFlow(ctx, projectID, flowName)
	if err != nil {
		confErr(w, err)
		return
	}
	if !f.OperatesOnNothing() {
		noun := "ticket"
		if f.OperatesOnMR() {
			noun = "merge request"
		}
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("flow %q starts its runs on a %s (operates_on: %s): start it from the %s's page, or pick a flow that starts on nothing (operates_on: %s)",
			flowName, noun, f.OperatesOn, noun, flow.OperatesOnNothing))
		return
	}
	t := runinput.Ticket{Title: strings.TrimSpace(body.Title), SourceBranch: strings.TrimSpace(body.Branch)}
	if t.Title == "" {
		t.Title = manualRunTitle(time.Now())
	}
	source := cmp.Or(t.SourceBranch, p.Repo.DefaultBranch)
	switch newBranch := strings.TrimSpace(body.NewBranch); {
	case body.OnBranch && newBranch != "":
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("new_branch names the run's fresh branch (%q) and on_branch has it work on %q itself: say one of the two", newBranch, source))
		return
	case body.OnBranch:
		// The run's branch is the one it starts from: nothing is forked.
		t.SourceBranch, t.Branch = source, source
	case newBranch == source:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("new_branch %q is the branch the run starts from: to work on it itself, say on_branch", newBranch))
		return
	default:
		// "" leaves the name to the engine: after the run, with its suffix.
		t.Branch = newBranch
	}
	runID, err := s.Runs.CreateRunWith(ctx, t, flowName, projectID,
		runinput.Input{Params: body.Params, Skillsets: body.Skillsets, Starter: auth.UserID(ctx)})
	if err != nil {
		runStartErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, RunRef{RunID: runID})
}
