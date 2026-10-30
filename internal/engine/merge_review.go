package engine

// The merge request's two acts on the repository host beyond opening it:
// mr_merge merges it, at the newest head the chain run knows (what it pushed,
// else the head it began on); post_review puts a review on it from an agent's
// review_threads output — its notes as new threads, its replies in threads
// already there. Both act on the chain's subject and record their intent
// first, so a crash is reconciled from the repository host.

import (
	"errors"
	"fmt"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/reviewfile"
)

// checkReviewThreads validates an agent stage's review_threads output when
// the stage ends: its shape (reviewfile), then what only the round knows —
// a reply names a thread this phase run loaded and resolves only one the
// factory opened; a note does not reopen what a thread of ours already
// says, nor what a person dropped at a gate earlier in the run. "" = fine
// (or none declared). The failure is the retry's edge.failure, so each
// message says what to write instead.
func (x *exec) checkReviewThreads(st *flow.Stage) string {
	p := flow.TypedPath(st, agentspec.TypeReviewThreads)
	if p == "" {
		return ""
	}
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, p)
	if err != nil {
		return at(p, err)
	}
	rev, err := reviewfile.ParseReview(p, data)
	if err != nil {
		return at(p, err)
	}
	if msg := x.reviewGuards(st, rev); msg != "" {
		return p + ": " + msg
	}
	return ""
}

// reviewGuards are the checks a review_threads file gets against the round:
// the engine's part of "do not repeat yourself". A reply (G3) continues a
// loaded thread, once, resolving only ours. A note (G1) does not sit on the
// place of an open thread of ours, (G1b) nor say what an open thread of ours
// already says on the same file — the threads input gives the words — and
// (G2) is not one a person dropped at a gate in this run. Exact rules on
// purpose: a note on a line a push moved is not caught, and the prompt
// carries the paraphrase case; what IS caught is never a false positive.
func (x *exec) reviewGuards(st *flow.Stage, rev *reviewfile.Review) string {
	rs := x.rs
	for i, c := range rev.Replies {
		if !rs.roundLoaded {
			return fmt.Sprintf("replies[%d]: no thread was loaded in this phase run, so there is none to reply in — a load_review_comments stage (with: { scope: all }) before this agent loads them; without one, write notes only", i)
		}
		lt := rs.roundThread(c.Thread)
		if lt == nil {
			return fmt.Sprintf("replies[%d]: thread %q is not one of the threads loaded (%s)", i, c.Thread, strings.Join(rs.roundIDs(), ", "))
		}
		if c.Resolve && !lt.Ours {
			return fmt.Sprintf("replies[%d]: thread %q is someone else's — only a thread apothecary opened is resolved here; reply without resolve", i, c.Thread)
		}
	}
	// The threads input, for the guard that reads words.
	var shown *reviewfile.Threads
	if tp := typedInput(st, agentspec.TypeMRThreads); tp != "" {
		if data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, tp); err == nil {
			shown, _ = reviewfile.ParseThreads(tp, data)
		}
	}
	for i, n := range rev.Notes {
		body := reviewfile.NormalizeBody(n.Body)
		for _, lt := range rs.round {
			if !lt.Ours || lt.Resolved {
				continue
			}
			if lt.Path == n.Path && lt.Line == n.Line {
				return fmt.Sprintf("notes[%d]: %s:%d already carries apothecary's thread %s, still open — continue it under replies (thread: %s) instead of opening another", i, n.Path, n.Line, lt.ID, lt.ID)
			}
			if th := shown.Thread(lt.ID); th != nil && th.Path == n.Path && len(th.Comments) > 0 && reviewfile.NormalizeBody(th.Comments[0].Body) == body {
				return fmt.Sprintf("notes[%d]: says what apothecary's thread %s already says on %s — continue it under replies instead of opening another", i, lt.ID, n.Path)
			}
		}
		for _, d := range rs.dropped {
			if d.Path == n.Path && reviewfile.NormalizeBody(d.Body) == body {
				return fmt.Sprintf("notes[%d]: a person decided not to post this note (%s:%d) at a gate earlier in this run — do not propose it again", i, d.Path, d.Line)
			}
		}
	}
	return ""
}

// actionPostReview posts the review file on the subject merge request: its
// summary and notes as one review, its replies in their threads, resolving
// where it says so. An empty review posts nothing. The review is named
// after this phase run and stage, and every reply carries the round, so a
// retry after a crash posts only what is missing.
func (x *exec) actionPostReview(st *flow.Stage) (bool, error) {
	rs := x.rs
	repoHost, mr, err := x.subjectRepoHost(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	path := inputPath(st, "review")
	data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, path)
	if err != nil {
		return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("review %s not readable: %v", path, err))
	}
	rev, err := reviewfile.ParseReview(path, data)
	if err != nil {
		return x.actionFail(st, event.ReasonInputInvalid, at(path, err))
	}
	for i, c := range rev.Replies {
		if rs.roundThread(c.Thread) == nil {
			return x.actionFail(st, event.ReasonInputInvalid, fmt.Sprintf("%s: replies[%d]: thread %q is not one of the threads loaded in this phase run", path, i, c.Thread))
		}
	}
	if rev.Empty() {
		x.e.log.Info("empty review: nothing to post", "run", rs.runID, "stage", st.ID)
		return false, nil
	}
	reentry := rs.actionPending[st.ID]
	if !reentry {
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
		rs.actionPending[st.ID] = true
	}
	reviewID := x.roundID() + "/" + st.ID
	posted := &event.ReviewPosted{StageID: st.ID, Subject: mr.Repo, ReviewID: reviewID}
	notes := make([]repoport.ReviewNote, 0, len(rev.Notes))
	for _, n := range rev.Notes {
		notes = append(notes, repoport.ReviewNote{Path: n.Path, Line: n.Line, Body: n.Posted(), Kind: n.Kind})
	}
	if rev.Summary != "" || len(notes) > 0 {
		res, err := repoHost.PostReview(x.ctx, mr.Ref, repoport.Review{Head: rs.phaseHead, Summary: rev.Summary, Notes: notes, ID: reviewID})
		if err != nil {
			return x.infraFail(st, "posting the review: "+err.Error())
		}
		posted.Inline, posted.Folded, posted.Already = res.Inline, res.Folded, res.Already
	}
	if len(rev.Replies) > 0 {
		// A reply already there from this round (a crash between two) is
		// not posted again; resolving twice is harmless.
		done := map[string]bool{}
		if reentry {
			threads, err := repoHost.MRThreads(x.ctx, mr.Ref)
			if err != nil {
				return x.infraFail(st, "cannot tell which replies were already posted: "+err.Error())
			}
			for _, t := range threads {
				for _, c := range t.Comments {
					if repoport.IsReplyTo(c.Body, x.roundID(), t.ID) {
						done[t.ID] = true
					}
				}
			}
		}
		for _, c := range rev.Replies {
			if !done[c.Thread] {
				body := strings.TrimSpace(c.Body) + "\n\n" + repoport.ReplyMarker(x.roundID(), c.Thread)
				if err := repoHost.ReplyThread(x.ctx, mr.Ref, c.Thread, body); err != nil {
					return x.infraFail(st, fmt.Sprintf("replying in thread %s: %v", c.Thread, err))
				}
			}
			posted.Replied = append(posted.Replied, c.Thread)
			if c.Resolve {
				if err := repoHost.ResolveThread(x.ctx, mr.Ref, c.Thread); err != nil {
					return x.infraFail(st, fmt.Sprintf("resolving thread %s: %v", c.Thread, err))
				}
				posted.Resolved = append(posted.Resolved, c.Thread)
			}
		}
	}
	if err := x.append(posted); err != nil {
		return true, err
	}
	delete(rs.actionPending, st.ID)
	return false, nil
}

// actionMRMerge merges the subject merge request, pinned to the newest head
// the phase run knows (phaseHead): what it pushed, else the head it began
// on. A merge the repository host declines is the stage's failure, for
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
	method := st.Setting("method", actionspec.MethodMerge)
	deleteBranch := st.Setting("branch", actionspec.BranchKeep) == actionspec.BranchDelete
	merged := &event.MRMerged{StageID: st.ID, Repo: mr.Repo, Method: method, Head: rs.phaseHead, BranchDeleted: deleteBranch}
	if rs.actionPending[st.ID] {
		status, err := repoHost.MRStatus(x.ctx, mr.Ref)
		if err != nil {
			return x.infraFail(st, "cannot tell whether the merge already happened: "+err.Error())
		}
		if status.State == repoport.MRMerged {
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
	err = repoHost.MergeMR(x.ctx, mr.Ref, repoport.MergeOptions{Method: method, Head: rs.phaseHead, DeleteBranch: deleteBranch})
	var refused *repoport.ErrMergeRefused
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
