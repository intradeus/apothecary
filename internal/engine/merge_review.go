package engine

// The merge request's two acts on the repository host beyond opening it:
// mr_merge merges it, at the head the phase began on; post_review puts a
// review on it from an agent's review_threads output. Both act on the phase's
// subject and record their intent first, so a crash is reconciled from the
// repository host.

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// maxReviewNotes bounds a review: more is a report, which belongs in the
// summary.
const maxReviewNotes = 50

// reviewFile is a review_threads output.
type reviewFile struct {
	Summary string `yaml:"summary"`
	Threads []struct {
		Path string `yaml:"path"`
		Line int    `yaml:"line"`
		Body string `yaml:"body"`
	} `yaml:"threads"`
}

// parseReviewThreads enforces the review_threads contract: {summary,
// threads: [{path, line, body}]}, every thread on a repo-relative path and a
// line from 1, with something to say. An empty file is an empty review.
func parseReviewThreads(data []byte) (string, []ports.ReviewNote, error) {
	var rf reviewFile
	if strings.TrimSpace(string(data)) != "" {
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&rf); err != nil {
			return "", nil, fmt.Errorf("review: not {summary, threads: [{path, line, body}]}: %v", err)
		}
	}
	if len(rf.Threads) > maxReviewNotes {
		return "", nil, fmt.Errorf("review: %d threads; at most %d — put the rest in the summary", len(rf.Threads), maxReviewNotes)
	}
	notes := make([]ports.ReviewNote, 0, len(rf.Threads))
	for i, th := range rf.Threads {
		p := strings.TrimPrefix(strings.TrimSpace(th.Path), "./")
		switch {
		case p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || strings.Contains(p, "/../"):
			return "", nil, fmt.Errorf("threads[%d]: path %q must be a file of the repository, relative to its root", i, th.Path)
		case th.Line < 1:
			return "", nil, fmt.Errorf("threads[%d]: line must be 1 or more (the line in the changed file), got %d", i, th.Line)
		case strings.TrimSpace(th.Body) == "":
			return "", nil, fmt.Errorf("threads[%d]: body is empty", i)
		}
		notes = append(notes, ports.ReviewNote{Path: p, Line: th.Line, Body: th.Body})
	}
	return strings.TrimSpace(rf.Summary), notes, nil
}

// checkReviewThreads validates an agent stage's review_threads output when
// the stage ends; "" = fine (or none declared).
func (x *exec) checkReviewThreads(st *flow.Stage) string {
	p := flow.ReviewThreadsPath(st, x.rs.fl.Agent(st.Agent))
	if p == "" {
		return ""
	}
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, p)
	if err == nil {
		_, _, err = parseReviewThreads(data)
	}
	if err != nil {
		return p + ": " + err.Error()
	}
	return ""
}

// actionPostReview posts the review file on the subject merge request. An
// empty review posts nothing. The review is named after this phase run and
// stage, so a retry after a crash posts only what is missing.
func (x *exec) actionPostReview(st *flow.Stage) (bool, error) {
	rs := x.rs
	repoHost, mr, err := x.subjectRepoHost(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	path := ""
	if in := st.Input("review"); in != nil {
		path = in.Value
	}
	data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, path)
	if err != nil {
		return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("review %s not readable: %v", path, err))
	}
	summary, notes, err := parseReviewThreads(data)
	if err != nil {
		return x.actionFail(st, event.ReasonInputInvalid, path+": "+err.Error())
	}
	if summary == "" && len(notes) == 0 {
		x.e.log.Info("empty review: nothing to post", "run", rs.runID, "stage", st.ID)
		return false, nil
	}
	if !rs.actionPending[st.ID] {
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
		rs.actionPending[st.ID] = true
	}
	res, err := repoHost.PostReview(x.ctx, mr.Ref, ports.Review{Head: rs.phaseTip, Summary: summary, Notes: notes, ID: x.roundID() + "/" + st.ID})
	if err != nil {
		return x.infraFail(st, "posting the review: "+err.Error())
	}
	if err := x.append(&event.ReviewPosted{StageID: st.ID, Subject: mr.Repo, Inline: res.Inline, Folded: res.Folded, Already: res.Already}); err != nil {
		return true, err
	}
	delete(rs.actionPending, st.ID)
	return false, nil
}

// actionMRMerge merges the subject merge request, pinned to the head the phase
// began on. A merge the repository host declines is the stage's failure, for
// on_fail; after a crash, a merge request already merged is this merge.
func (x *exec) actionMRMerge(st *flow.Stage) (bool, error) {
	rs := x.rs
	repoHost, mr, err := x.subjectRepoHost(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	method := actionspec.MethodMerge
	if in := st.Input("method"); in != nil && in.Value != "" {
		method = in.Value
	}
	deleteBranch := false
	if in := st.Input("branch"); in != nil {
		deleteBranch = in.Value == actionspec.BranchDelete
	}
	merged := &event.MRMerged{StageID: st.ID, Repo: mr.Repo, Method: method, Head: rs.phaseTip, BranchDeleted: deleteBranch}
	if rs.actionPending[st.ID] {
		status, err := repoHost.MRStatus(x.ctx, mr.Ref)
		if err != nil {
			return x.infraFail(st, "cannot tell whether the merge already happened: "+err.Error())
		}
		if status.State == ports.MRMerged {
			if err := x.append(merged); err != nil {
				return true, err
			}
			delete(rs.actionPending, st.ID)
			return false, nil
		}
	} else {
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
		rs.actionPending[st.ID] = true
	}
	err = repoHost.MergeMR(x.ctx, mr.Ref, ports.MergeOptions{Method: method, Head: rs.phaseTip, DeleteBranch: deleteBranch})
	var refused *ports.ErrMergeRefused
	if errors.As(err, &refused) {
		delete(rs.actionPending, st.ID)
		return x.actionFail(st, event.ReasonInputInvalid, fmt.Sprintf("%s: %s", mr.Ref.URL, refused.Error()))
	}
	if err != nil {
		return x.infraFail(st, "merging "+mr.Ref.URL+": "+err.Error())
	}
	if err := x.append(merged); err != nil {
		return true, err
	}
	delete(rs.actionPending, st.ID)
	return false, nil
}
