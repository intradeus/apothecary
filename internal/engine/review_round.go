package engine

// Review threads (PLAN6 M31, TODO 45): load the subject merge request's
// threads for an agent — the pending ones for a round that fixes them (a
// person picks which), every one for a reviewer about to write, so it reads
// what is already said before it says anything — validate the replier's
// answers when its stage ends, and post them, each carrying a marker, so a
// crash between two posts is reconciled from the repository host and
// nothing is posted twice.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/reviewfile"
	"apothecary/internal/runcore"
)

// at prefixes what is wrong with the file it is wrong in, once.
func at(path string, err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, path+":") {
		return msg
	}
	return path + ": " + msg
}

// typedInput is the workspace path of the stage's file input of that type,
// or "" when it reads none.
func typedInput(st *flow.Stage, typ string) string {
	for _, in := range st.Inputs {
		if in.Type == typ && !flow.IsEngineSource(in.Value) {
			return flow.FilePath(in.Value)
		}
	}
	return ""
}

// checkReviewReplies validates an agent stage's review_replies output when
// the stage ends: its shape, and that it answers every thread this round
// loaded, once each — a declined one too; "" = fine (or none declared).
func (x *exec) checkReviewReplies(st *flow.Stage) string {
	p := flow.TypedPath(st, agentspec.TypeReviewReplies)
	if p == "" {
		return ""
	}
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.EnvID, p)
	if err != nil {
		return at(p, err)
	}
	replies, err := reviewfile.ParseReplies(p, data)
	if err == nil {
		err = reviewfile.CheckReplies(replies, x.rs.RoundIDs())
	}
	if err != nil {
		return at(p, err)
	}
	return ""
}

// errNoSubject: a subject action in a phase run that acts on no merge request
// — routable (on_fail), unlike a repository host that cannot be reached.
var errNoSubject = errors.New("no merge request subject")

// noSubjectMsg explains a subject action's failure in a subject-less phase
// run: the phase was opened for no merge request (started by hand with none
// open, or the main chain of a run on a ticket), so there is nothing for it to act on.
func noSubjectMsg(st *flow.Stage) string {
	return fmt.Sprintf("%s: %s acts on the merge request a phase is opened for, and this phase run has none — guard the stage with `run_if: run.subject`, or start the phase on a merge request", errNoSubject, st.Action)
}

// subjectRepoHost is the repository host and merge request a subject action
// acts on.
func (x *exec) subjectRepoHost(st *flow.Stage) (repoport.RepoHost, *runcore.MRRecord, error) {
	mr := x.rs.SubjectMR()
	if mr == nil {
		return nil, nil, fmt.Errorf("%w: %s", errNoSubject, noSubjectMsg(st))
	}
	repoHost := x.res.RepoHostFor(mr.Repo)
	if repoHost == nil {
		return nil, nil, fmt.Errorf("stage %s: no integration reaches repository %s", st.ID, mr.Repo)
	}
	return repoHost, mr, nil
}

// actionLoadReviewComments writes the threads a phase run works on, as an
// mr_threads document. With scope: pending (a fixing round) they are the
// threads waiting on the factory — with select: human a person first picks
// which (an inbox question; the run parks until it is answered), the rest
// are skipped until someone comments on them again. With scope: all (a
// reviewer) they are every thread, ours included, and nothing is picked.
// The first entry picks the set; a re-entry rewrites the same set, so one
// phase run works on what it started with.
func (x *exec) actionLoadReviewComments(st *flow.Stage) (bool, error) {
	rs := x.rs
	repoHost, mr, err := x.subjectRepoHost(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	threads, err := repoHost.MRThreads(x.ctx, mr.Ref)
	if err != nil {
		return x.infraFail(st, "listing review threads: "+err.Error())
	}
	scope := st.Setting("scope", actionspec.ScopePending)
	var round []repoport.ReviewThread
	if rs.RoundLoaded {
		ids := rs.RoundIDs()
		for _, t := range threads {
			if slices.Contains(ids, t.ID) {
				round = append(round, t)
			}
		}
	} else {
		chosen := threads
		skipped := map[string]string{}
		if scope == actionspec.ScopePending {
			pending := x.rs.Unskipped(repoport.PendingThreads(threads))
			chosen = pending
			if mode := st.Setting("select", actionspec.SelectHuman); mode == actionspec.SelectHuman && len(pending) > 0 {
				picked, waiting, err := x.selectThreads(st, mr, pending)
				if err != nil || waiting {
					return true, err
				}
				chosen = picked
			}
			for _, t := range pending {
				if !slices.ContainsFunc(chosen, func(c repoport.ReviewThread) bool { return c.ID == t.ID }) {
					skipped[t.ID] = t.Comments[len(t.Comments)-1].ID
				}
			}
		}
		loaded := make([]event.LoadedThread, 0, len(chosen))
		for _, t := range chosen {
			loaded = append(loaded, loadedThread(t))
		}
		newest := ""
		if c, ok := repoport.NewestPending(chosen); ok {
			newest = c.ID
		}
		if err := x.append(&event.ReviewCommentsLoaded{StageID: st.ID, Subject: mr.Repo, Scope: scope, Threads: loaded, Newest: newest, Skipped: skipped}); err != nil {
			return true, err
		}
		round = chosen
	}
	// Where the stage said the threads go (its `threads` output port).
	path := "artifacts/threads.yaml"
	if o := st.Output("threads"); o != nil {
		path = o.Value
	}
	data, err := reviewfile.Encode(path, threadsDocument(mr, round, rs.Dropped))
	if err != nil {
		return x.infraFail(st, "rendering "+path+": "+err.Error())
	}
	if err := x.res.Envp.WriteFile(x.ctx, rs.EnvID, path, data, 0o644); err != nil {
		return x.infraFail(st, "writing "+path+": "+err.Error())
	}
	return false, nil
}

// inputPath is the workspace path of a stage's file input (repo: stripped),
// or "" when the stage states no such port.
func inputPath(st *flow.Stage, name string) string {
	if in := st.Input(name); in != nil {
		return flow.FilePath(in.Value)
	}
	return ""
}

// threadOurs reports whether the factory opened a thread: its first comment
// carries one of its markers (a review note, or a reply that became a
// conversation of its own on the host).
func threadOurs(t repoport.ReviewThread) bool {
	return len(t.Comments) > 0 && repoport.IsFactoryReply(t.Comments[0].Body)
}

// loadedThread is what the engine keeps of a loaded thread (the event).
func loadedThread(t repoport.ReviewThread) event.LoadedThread {
	lt := event.LoadedThread{ID: t.ID, Path: t.Path, Line: t.Line, Ours: threadOurs(t), Resolved: t.Resolved}
	if n := len(t.Comments); n > 0 {
		lt.Last = t.Comments[n-1].ID
	}
	return lt
}

// threadsDocument renders the threads a phase run loaded for the agents and
// the gate page: each thread with whose it is, its kind when the factory
// opened it, its hunk while it is open (a prompt has a cap), and its
// comments — the factory's marked ours, what came after the factory's last
// word marked new, what an earlier reply did read off its marker — plus the
// notes a person dropped at a gate earlier in the run.
func threadsDocument(mr *runcore.MRRecord, round []repoport.ReviewThread, dropped []reviewfile.Note) *reviewfile.Threads {
	doc := &reviewfile.Threads{MergeRequest: mr.Ref.URL, Threads: make([]reviewfile.Thread, 0, len(round)), Dropped: dropped}
	for _, t := range round {
		th := reviewfile.Thread{ID: t.ID, Path: t.Path, Line: t.Line, Resolved: t.Resolved, Ours: threadOurs(t), Comments: []reviewfile.Comment{}}
		if th.Ours {
			th.Kind = repoport.MarkerAttr(t.Comments[0].Body, "kind")
		}
		if !t.Resolved {
			th.Hunk = strings.TrimRight(t.DiffHunk, "\n")
		}
		lastOurs := -1
		for i, c := range t.Comments {
			if repoport.IsFactoryReply(c.Body) {
				lastOurs = i
			}
		}
		for i, c := range t.Comments {
			ours := repoport.IsFactoryReply(c.Body)
			cm := reviewfile.Comment{ID: c.ID, Author: c.Author, Ours: ours, New: i > lastOurs, Body: reviewfile.StripMarkers(c.Body)}
			if ours {
				cm.Did = repoport.MarkerAttr(c.Body, "did")
			}
			if !c.CreatedAt.IsZero() {
				cm.At = c.CreatedAt.UTC().Format(time.RFC3339)
			}
			th.Comments = append(th.Comments, cm)
		}
		doc.Threads = append(doc.Threads, th)
	}
	return doc
}

// threadOption is how a thread is offered for selection: its id to answer
// with, where it is and who started it as the label, its first words as
// the detail.
func threadOption(t repoport.ReviewThread) event.Option {
	where := "conversation"
	if t.Path != "" {
		where = t.Path
		if t.Line > 0 {
			where += fmt.Sprintf(":%d", t.Line)
		}
	}
	first := t.Comments[0]
	text := strings.Join(strings.Fields(reviewfile.StripMarkers(first.Body)), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return event.Option{ID: t.ID, Label: where + " — " + first.Author, Detail: text}
}

// selectThreads asks a human which pending threads this round fixes. It
// raises the question once per round and parks the run until it is
// answered (waiting=true); the answer is the chosen threads, possibly none.
func (x *exec) selectThreads(st *flow.Stage, mr *runcore.MRRecord, pending []repoport.ReviewThread) (picked []repoport.ReviewThread, waiting bool, err error) {
	rs := x.rs
	if rs.SelectPing == nil {
		// One array field offering every pending thread, all of them picked
		// to start with: unpicking is the deliberate act.
		fd := event.Field{ID: event.SelectField, Type: event.FieldArray, Options: make([]event.Option, len(pending))}
		all := make([]any, len(pending))
		for i, t := range pending {
			fd.Options[i] = threadOption(t)
			all[i] = t.ID
		}
		fd.Default = all
		form := &event.Form{Fields: []event.Field{fd}}
		q := fmt.Sprintf("Which review comments on %s should be fixed? Uncheck the ones that are wrong, already handled, or not worth changing — they are left alone until someone comments on them again. Selecting none ends this round.", mr.Ref.URL)
		p := &Ping{ID: x.e.newID(), RunID: rs.RunID, Type: event.PingSelect, Question: q, Link: mr.Ref.URL,
			StageID: st.ID, Form: form, AnswerKind: event.AnswerForm, RaisedAt: x.e.clock(), done: make(chan struct{})}
		x.e.mu.Lock()
		x.e.pings[p.ID] = p
		x.e.mu.Unlock()
		raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: q, Link: p.Link, StageID: st.ID, Form: form, AnswerKind: event.AnswerForm}
		if err := x.append(raised); err != nil {
			return nil, true, err
		}
		x.e.notify(x.bg, notifyport.Notification{RunID: rs.RunID, PingID: p.ID, Type: p.Type, Title: q, Body: p.Link})
		return nil, true, errWaitingOnHuman
	}
	ans := rs.AnsweredPings[rs.SelectPing.PingID]
	if ans == nil {
		return nil, true, errWaitingOnHuman
	}
	// A decline fixes none of them, as selecting none does.
	for _, id := range formPicks(ans.Content, event.SelectField) {
		for _, t := range pending {
			if t.ID == id {
				picked = append(picked, t)
			}
		}
	}
	return picked, false, nil
}

// emptyRound reports a fixing round that loaded nothing to fix (a human
// selected none, or nothing was pending): the phase ends right after the
// load instead of spending agent turns on nothing. A reviewer's load (scope
// all) ends nothing: a merge request with no thread yet is still reviewed.
func (x *exec) emptyRound(st *flow.Stage) bool {
	return st.Type == flow.StageAction && st.Action == actionspec.LoadReviewComments &&
		x.rs.RoundLoaded && x.rs.RoundScope != actionspec.ScopeAll && len(x.rs.Round) == 0
}

// actionReplyReviewComments posts the round's replies. Intent first; on a
// re-entry after a crash, threads that already carry this run's reply are
// skipped — a duplicate reply on a reviewer's thread is worse than a retry.
func (x *exec) actionReplyReviewComments(st *flow.Stage) (bool, error) {
	rs := x.rs
	repoHost, mr, err := x.subjectRepoHost(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	path := inputPath(st, "replies")
	data, err := x.res.Envp.ReadFile(x.ctx, rs.EnvID, path)
	if err != nil {
		return x.infraFail(st, fmt.Sprintf("replies %s not readable: %v", path, err))
	}
	// Every thread the round loaded is answered — minus the ones a person
	// held a reply back from at the gate, which are skipped until someone
	// writes there again.
	replies, err := reviewfile.ParseReplies(path, data)
	if err == nil {
		err = reviewfile.CheckReplies(replies, rs.UnskippedRound())
	}
	if err != nil {
		return x.infraFail(st, at(path, err))
	}
	done := map[string]bool{}
	if rs.ActionPending[st.ID] {
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
	} else {
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
	}
	var posted, resolved, declined []string
	for _, r := range replies {
		if !done[r.Thread] {
			body := strings.TrimSpace(r.Reply) + "\n\n" + repoport.ReplyMarker(x.roundID(), r.Thread, "did="+r.Did)
			if err := repoHost.ReplyThread(x.ctx, mr.Ref, r.Thread, body); err != nil {
				return x.infraFail(st, fmt.Sprintf("replying to thread %s: %v", r.Thread, err))
			}
		}
		posted = append(posted, r.Thread)
		if r.Did == reviewfile.DidDeclined {
			declined = append(declined, r.Thread)
		}
		if r.Resolve {
			if err := repoHost.ResolveThread(x.ctx, mr.Ref, r.Thread); err != nil {
				return x.infraFail(st, fmt.Sprintf("resolving thread %s: %v", r.Thread, err))
			}
			resolved = append(resolved, r.Thread)
		}
	}
	if err := x.append(&event.ReviewRepliesPosted{StageID: st.ID, Subject: mr.Repo, ThreadIDs: posted, Resolved: resolved, Declined: declined}); err != nil {
		return true, err
	}
	return false, nil
}

// roundID names this phase run in reply markers — "<run>/<phase>-<n>" —
// so reconciling after a crash recognises this round's replies only, never
// an earlier round's answer on the same thread.
func (x *exec) roundID() string {
	rs := x.rs
	return fmt.Sprintf("%s/%s-%d", rs.RunID, rs.Phase, rs.PhaseRuns[runcore.PhaseRunKey(rs.Phase, rs.Subject)])
}
