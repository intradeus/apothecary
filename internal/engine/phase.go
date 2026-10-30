package engine

// Phases (FLOWLANG §1): a flow is chains of stages — its main chain, its
// watchers and its outcomes, each a phase opened by a wait point. A run
// begins with its main chain (run_start); when a phase run ends or parks at
// a wait point mid-chain the run is idle, and the idle loop is the one
// scheduler: a watcher opens when its signal holds for one merge request
// (its subject), under its start policy; an outcome fires once and ends the
// run; a person starts any watcher from the run page; one phase run holds
// the workspace at a time.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/runcore"
	"apothecary/internal/shell"
	"apothecary/internal/signalspec"
)

// ErrPhaseNotStartable: a manual phase start the run cannot take now.
var ErrPhaseNotStartable = errors.New("phase cannot start")

// phaseStartReq is a human's "run this phase now", handed to the idle loop.
type phaseStartReq struct {
	phase, subject string
	ack            chan error
}

// StartPhase asks an idle run to start a phase by hand: on the named
// checkout's merge request, or (subject "") on the run's single open one,
// else on none. Refused unless the run is idle, or when a run of that phase
// is parked at a wait point.
func (e *Engine) StartPhase(runID, phase, subject string) error {
	e.mu.Lock()
	h, ok := e.handles[runID]
	e.mu.Unlock()
	if !ok || !h.idle.Load() {
		return fmt.Errorf("%w: the run is not between phases", ErrPhaseNotStartable)
	}
	req := phaseStartReq{phase: phase, subject: subject, ack: make(chan error, 1)}
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	select {
	case h.phaseStarts <- req:
	case <-h.done:
		return fmt.Errorf("%w: the run ended", ErrPhaseNotStartable)
	case <-t.C:
		return fmt.Errorf("%w: the run is busy", ErrPhaseNotStartable)
	}
	select {
	case err := <-req.ack:
		return err
	case <-h.done:
		return fmt.Errorf("%w: the run ended", ErrPhaseNotStartable)
	}
}

// beginRun opens the run's main chain through the same path every other
// phase start takes. A flow with no main chain (a caretaker) does its work
// in watchers: the run begins idle. Replay decides: a log that already holds
// a phase_started has begun.
func (x *exec) beginRun() error {
	rs := x.rs
	if rs.Phase != "" || rs.Idle || rs.Done {
		return nil
	}
	main := rs.Flow.Main()
	if main == nil {
		rs.Idle = true
		return nil
	}
	begin := main.ID
	by := rs.View.StartedBy
	if by == "" {
		by = "human"
	}
	if rs.AnyStarted {
		// Stage events but no phase start (a log written by an older build,
		// or by hand): record the start without moving the cursor replay
		// placed — the run is already mid-phase.
		return x.append(&event.PhaseStarted{Phase: begin, By: by})
	}
	// A run started on someone's merge request begins on it: the merge
	// request is the main chain's subject, at the head it was started at.
	if m := rs.Adopted; m != nil && main.OnMR() {
		return x.beginPhase(begin, m.Repo, "", by, m.Head)
	}
	return x.beginPhase(begin, "", "", by, "")
}

// followAdopted brings the checkout of a merge request the run was created
// on to the head its phase run began on: someone else's branch moves under
// the run, and a reviewer reads what is there now. Fast-forward only — a
// checkout the run committed on that diverged is left as it is. Once per
// head; best effort (the stage sees whatever the checkout holds).
func (x *exec) followAdopted() {
	rs := x.rs
	co := x.rs.SubjectCheckout()
	if co == nil || co.Fetch == "" || rs.PhaseTip == "" || x.followed == rs.PhaseTip {
		return
	}
	x.followed = rs.PhaseTip
	res, err := x.gitIn(co.Dir, fmt.Sprintf("git fetch origin %s && git merge --ff-only FETCH_HEAD", shell.Quote(co.Fetch)))
	if err != nil || res.ExitCode != 0 {
		x.e.log.Warn("could not bring the merge request's checkout to its head", "run", rs.RunID, "repo", co.Name,
			"head", rs.PhaseTip, "err", err, "out", shell.Tail(res.Output, 512))
	}
}

// phaseDone ends the phase in progress (FLOWLANG §3 end): the run goes
// idle, and the next idle tick decides what follows — a parked phase
// resuming, another phase opening, or the run's end (runcore.State.PlanIdle). A phase that
// ended at a dead end says why.
func (x *exec) phaseDone() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.Phase, Subject: rs.Subject, Reason: rs.EndReason}); err != nil {
		return err
	}
	return nil
}

// runDone is the flow's own end of the run (FLOWLANG §3 done): the phase
// finishes and the run completes with it, whatever else is parked or armed —
// their open questions are withdrawn with the run.
func (x *exec) runDone() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.Phase, Subject: rs.Subject}); err != nil {
		return err
	}
	return x.complete()
}

// runAbort is the flow abandoning the run from a success: an outcome's
// `then: abort`, or `on_success: abort` (FLOWLANG §3).
func (x *exec) runAbort() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.Phase, Subject: rs.Subject}); err != nil {
		return err
	}
	return x.abandon(rs.AbortAt, "", rs.AbortReason)
}

// deadEnd is where a failure has nowhere left to go: no edge for it, its
// chain exhausted, or a person giving up at the debug leaf. In the main
// chain or an outcome that ends the run — it is abandoned: the work failed,
// or its end did. A watcher's round — opened by its signal or by hand — is
// one of any number the run may do: the round ends, its phase_completed
// saying why, and the run goes back to waiting (FLOWLANG §3).
func (x *exec) deadEnd(st *flow.Stage, edgeKind, reason string) error {
	if x.endsRunAtDeadEnd() {
		return x.abandon(st.ID, edgeKind, reason)
	}
	x.e.log.Info("phase ended at a dead end; the run goes back to waiting",
		"run", x.rs.RunID, "phase", x.rs.Phase, "stage", st.ID, "reason", reason)
	x.rs.EndPhase()
	x.rs.EndReason = reason
	return nil
}

// endsRunAtDeadEnd reports whether the phase in progress is one whose dead
// end ends the run rather than the round: the main chain or an outcome.
func (x *exec) endsRunAtDeadEnd() bool {
	ph := x.rs.Flow.Phase(x.rs.Phase)
	return ph == nil || ph.Kind != flow.PhaseWatcher
}

// idler is the idle loop's memory between ticks: what is not run history (the
// repository host is the source of truth for it) and so lives only in the
// loop. Everything is per merge request (checkout + branch).
type idler struct {
	threadsAt map[string]time.Time
	threads   map[string][]repoport.ReviewThread
	checks    map[string]polled[repoport.Checks]
	approvals map[string]polled[repoport.Approval]
	heads     map[string]polled[string]
	failures  int
}

// polled is a value read from the repository host and when.
type polled[T any] struct {
	v  T
	at time.Time
}

func newIdler() *idler {
	return &idler{threadsAt: map[string]time.Time{}, threads: map[string][]repoport.ReviewThread{},
		checks: map[string]polled[repoport.Checks]{}, approvals: map[string]polled[repoport.Approval]{}, heads: map[string]polled[string]{}}
}

// repoHostReads says which of a merge request's facts beyond its status the
// flow's signals read — on phase heads, wait stages and gates alike — so an
// idle run asks the repository host for nothing it will not look at.
type repoHostReads struct{ threads, checks, approval bool }

func readsOf(fl *flow.Flow) repoHostReads {
	var r repoHostReads
	note := func(slug string) {
		switch slug {
		case signalspec.MRReviewSettled:
			r.threads = true
		case signalspec.CIFailed:
			r.checks = true
		case signalspec.MRApproved:
			r.approval = true
		}
	}
	for i := range fl.Phases {
		note(fl.Phases[i].On.Signal)
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		note(st.WaitFor)
		note(st.AutoApprove)
		note(st.AutoReject)
	}
	return r
}

// idle runs while the run sits between phases: it polls the repository host
// for the outcomes' and the watchers' signals, takes confirmation answers
// and manual starts, and returns once a phase starts or the run ends.
func (x *exec) idle() error {
	rs := x.rs
	x.h.idle.Store(true)
	defer x.h.idle.Store(false)
	// The slot this loop held is free while it idles: admit a queued run.
	lim := x.e.limits(x.bg)
	x.e.mu.Lock()
	x.e.scheduleLocked(lim)
	x.e.mu.Unlock()

	st := newIdler()
	if err := x.evaluateIdle(st); err != nil || !rs.Idle || rs.Done {
		return err
	}
	t := time.NewTicker(x.e.cfg.MRPollInterval)
	defer t.Stop()
	for {
		select {
		case ar := <-x.h.answers:
			if _, err := x.processAnswer(ar); err != nil {
				return err
			}
		case req := <-x.h.phaseStarts:
			req.ack <- x.manualStart(req)
		case <-t.C:
		case <-x.ctx.Done():
			return context.Cause(x.ctx)
		}
		if err := x.evaluateIdle(st); err != nil || !rs.Idle || rs.Done {
			return err
		}
	}
}

// evaluateIdle is one idle tick: observe once, then carry out what the
// observation means (runcore.State.PlanIdle).
func (x *exec) evaluateIdle(st *idler) error {
	defer x.checkFold()
	rs := x.rs
	obs, err := x.observe(st)
	x.lastObs = obs
	if err != nil {
		if errors.Is(err, context.Canceled) || x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		st.failures++
		x.e.log.Warn("idle: repository poll failed", "run", rs.RunID, "err", err, "streak", st.failures)
		if st.failures == 3 {
			x.e.notify(x.bg, notifyport.Notification{RunID: rs.RunID, Type: event.PingBlocker,
				Title: "Cannot reach the repository to watch this run's merge requests", Body: err.Error()})
		}
		return nil
	}
	st.failures = 0

	plan := rs.PlanIdle(obs, x.e.clock())
	if plan.Holds != nil {
		defer x.e.holds.set(rs.RunID, plan.Holds)
	}
	for _, a := range plan.Actions {
		var err error
		switch a.Kind {
		case runcore.IdleResume:
			err = x.resumeParked(a.Resume)
		case runcore.IdleDecline:
			err = x.append(&event.PhaseDeclined{Phase: a.Phase.ID, Subject: a.Subject, Key: a.Key})
		case runcore.IdleAsk:
			err = x.askToStart(a.Phase, a.Subject, a.Link, a.Key, a.Why)
		case runcore.IdleBegin:
			err = x.beginPhase(a.Phase.ID, a.Subject, a.Key, "signal", a.Tip)
		case runcore.IdleComplete:
			err = x.complete()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// observe polls every merge request's status, and its threads when a
// debounced trigger needs them and they are due.
func (x *exec) observe(st *idler) ([]runcore.MRObservation, error) {
	rs := x.rs
	reads := readsOf(rs.Flow)
	every := x.e.cfg.ThreadPollInterval
	if every <= 0 {
		every = time.Minute
	}
	now := x.e.clock()
	out := make([]runcore.MRObservation, 0, len(rs.MRs))
	for i := range rs.MRs {
		mr := &rs.MRs[i]
		repoHost := x.res.RepoHostFor(mr.Repo)
		if repoHost == nil {
			continue
		}
		status, err := repoHost.MRStatus(x.ctx, mr.Ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mr.Ref.URL, err)
		}
		k := mr.Repo + "\x00" + mr.Branch
		if h, ok := st.heads[k]; !ok || h.v != status.HeadSHA {
			st.heads[k] = polled[string]{status.HeadSHA, now}
		}
		o := runcore.MRObservation{MR: mr, Status: status, HeadSince: st.heads[k].at}
		if reads.checks && status.State == repoport.MROpen {
			// A head's checks are asked for when the head is new, while they
			// run, and otherwise at the thread cadence (a re-run turns them).
			c, ok := st.checks[k]
			stale := !ok || c.v.Head != status.HeadSHA || c.v.State == repoport.CheckPending || now.Sub(c.at) >= every ||
				(c.v.State == repoport.ChecksNone && now.Sub(o.HeadSince) < every)
			if stale {
				checks, err := repoHost.MRChecks(x.ctx, mr.Ref, status.HeadSHA)
				if err != nil {
					return nil, fmt.Errorf("%s checks: %w", mr.Ref.URL, err)
				}
				st.checks[k] = polled[repoport.Checks]{checks, now}
			}
			v := st.checks[k].v
			o.Checks = &v
		}
		if reads.approval && status.State == repoport.MROpen {
			if a, ok := st.approvals[k]; !ok || now.Sub(a.at) >= every {
				ap, err := repoHost.MRApproval(x.ctx, mr.Ref)
				if err != nil {
					return nil, fmt.Errorf("%s approval: %w", mr.Ref.URL, err)
				}
				st.approvals[k] = polled[repoport.Approval]{ap, now}
			}
			v := st.approvals[k].v
			o.Approval = &v
		}
		if reads.threads && status.State == repoport.MROpen {
			if last, ok := st.threadsAt[k]; !ok || x.e.clock().Sub(last) >= every {
				threads, err := repoHost.MRThreads(x.ctx, mr.Ref)
				if err != nil {
					return nil, fmt.Errorf("%s threads: %w", mr.Ref.URL, err)
				}
				st.threads[k], st.threadsAt[k] = threads, x.e.clock()
			}
			o.Threads = st.threads[k]
		}
		out = append(out, o)
	}
	return out, nil
}

// askToStart raises a phase_confirm ping.
func (x *exec) askToStart(ph *flow.Phase, subject, link, key, why string) error {
	rs := x.rs
	q := fmt.Sprintf("%s Run the %q watcher on it?", why, ph.ID)
	if ph.IsOutcome() {
		q = fmt.Sprintf("%s Run the %q outcome and end the run?", why, ph.ID)
	}
	if ph.Description != "" {
		q += " (" + ph.Description + ")"
	}
	p := &Ping{ID: x.e.newID(), RunID: rs.RunID, Type: event.PingPhaseConfirm, Question: q,
		Link: link, RaisedAt: x.e.clock(), AnswerKind: event.AnswerYesNo, Phase: ph.ID,
		done: make(chan struct{})}
	x.e.mu.Lock()
	x.e.pings[p.ID] = p
	x.e.mu.Unlock()
	raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: q, Link: p.Link, AnswerKind: event.AnswerYesNo,
		Phase: ph.ID, Subject: subject, TriggerKey: key}
	if err := x.append(raised); err != nil {
		return err
	}
	x.e.notify(x.bg, notifyport.Notification{RunID: rs.RunID, PingID: p.ID, Type: p.Type, Title: q, Body: p.Link})
	return nil
}

// beginPhase records a phase start and moves the cursor to its first stage.
func (x *exec) beginPhase(phase, subject, key, by, tip string) error {
	p := &event.PhaseStarted{Phase: phase, Subject: subject, Key: key, By: by, Tip: tip}
	if ph := x.rs.Flow.Phase(phase); ph != nil && x.rs.Flow.ReadsCIFailure(ph) {
		p.CI = x.readCI(subject, tip)
	}
	return x.append(p)
}

// readCI reads what failed in CI on the subject's merge request at head
// (its current head when the start did not say), once, for the chain's
// run.ci.failure. What cannot be read is said, not fatal: the agent can
// still run the project's checks itself.
func (x *exec) readCI(subject, head string) *event.CIReport {
	rep := &event.CIReport{Head: head}
	co := x.rs.Checkout(subject)
	var mr *runcore.MRRecord
	if co != nil {
		mr = x.rs.MRFor(co.Name, co.Branch)
	}
	repoHost := x.res.RepoHostFor(subject)
	if mr == nil || repoHost == nil {
		rep.Error = "this chain has no merge request to read CI from"
		return rep
	}
	if rep.Head == "" {
		st, err := repoHost.MRStatus(x.ctx, mr.Ref)
		if err != nil {
			rep.Error = "reading the merge request: " + err.Error()
			return rep
		}
		rep.Head = st.HeadSHA
	}
	failures, err := repoHost.CIFailures(x.ctx, mr.Ref, rep.Head)
	if err != nil {
		rep.Error = "reading its CI: " + err.Error()
	}
	for _, f := range failures {
		rep.Checks = append(rep.Checks, event.CICheck{Name: f.Name, URL: f.URL, File: f.File, Job: f.Job,
			Stage: f.Stage, Steps: f.Steps, Summary: f.Summary})
	}
	return rep
}

// ciFailureText is run.ci.failure as an agent reads it: each failed check
// and where it is declared, so the agent finds its script and runs it — the
// log is not handed over.
func ciFailureText(rep *event.CIReport) string {
	if rep == nil {
		return "(CI was not read for this chain)"
	}
	var b strings.Builder
	head := rep.Head
	if len(head) > 12 {
		head = head[:12]
	}
	switch {
	case len(rep.Checks) > 0:
		fmt.Fprintf(&b, "CI failed on %s:\n", head)
	case rep.Error != "":
		fmt.Fprintf(&b, "What failed in CI could not be read (%s).\n", rep.Error)
	default:
		fmt.Fprintf(&b, "No CI check is failing on %s.\n", head)
	}
	for _, c := range rep.Checks {
		fmt.Fprintf(&b, "- %s", c.Name)
		var where []string
		if c.File != "" {
			where = append(where, c.File)
		}
		if c.Job != "" && c.Job != c.Name {
			where = append(where, "job "+quoted(c.Job))
		}
		if c.Stage != "" {
			where = append(where, "stage "+c.Stage)
		}
		if len(c.Steps) > 0 {
			where = append(where, "failed step "+strings.Join(quoteAll(c.Steps), ", "))
		}
		if len(where) > 0 {
			fmt.Fprintf(&b, " — %s", strings.Join(where, ", "))
		}
		if c.Summary != "" {
			fmt.Fprintf(&b, ": %s", c.Summary)
		}
		if c.URL != "" {
			fmt.Fprintf(&b, " (%s)", c.URL)
		}
		b.WriteString("\n")
	}
	if len(rep.Checks) > 0 {
		b.WriteString("The logs are not included: find each job in its CI file, run the script it runs here, and fix what it reports.\n")
	}
	return b.String()
}

func quoted(s string) string { return `"` + s + `"` }

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = quoted(s)
	}
	return out
}

// manualStart starts a phase a human asked for, from the idle loop: a
// watcher or an outcome without a live run, on the named checkout's merge
// request, else on the run's single open one, else on none. The main chain
// runs once, when the run is started.
func (x *exec) manualStart(req phaseStartReq) error {
	rs := x.rs
	ph := rs.Flow.Phase(req.phase)
	if ph == nil {
		return fmt.Errorf("%w: no phase %q in this flow", ErrPhaseNotStartable, req.phase)
	}
	if pr := rs.Parked[ph.ID]; pr != nil {
		return fmt.Errorf("%w: phase %q is parked at %s", ErrPhaseNotStartable, ph.ID, pr.ParkedAt)
	}
	if ph.Kind == flow.PhaseMain {
		return fmt.Errorf("%w: the main chain runs once, when the run is started — rerun the run to run it again", ErrPhaseNotStartable)
	}
	var subject string
	var mr *runcore.MRRecord
	if req.subject != "" {
		co := rs.Checkout(req.subject)
		if co == nil || rs.MRFor(co.Name, co.Branch) == nil {
			return fmt.Errorf("%w: no merge request for repository %q", ErrPhaseNotStartable, req.subject)
		}
		subject, mr = co.Name, rs.MRFor(co.Name, co.Branch)
	} else if open := x.openMRs(); len(open) == 1 {
		subject, mr = open[0].Repo, open[0]
	}
	if ph.On.Max != flow.UnlimitedMax && rs.PhaseRuns[runcore.PhaseRunKey(ph.ID, subject)] >= ph.On.Max {
		on := "with no merge request"
		if subject != "" {
			on = "on " + subject
		}
		return fmt.Errorf("%w: phase %q already ran %d times %s (max)", ErrPhaseNotStartable, ph.ID, ph.On.Max, on)
	}
	var tip string
	if mr != nil {
		if repoHost := x.res.RepoHostFor(subject); repoHost != nil {
			if st, err := repoHost.MRStatus(x.ctx, mr.Ref); err == nil {
				tip = st.HeadSHA
			}
		}
	}
	// A human's start is its own occurrence: it never consumes the key a
	// signal would fire for, and no signal key blocks it.
	return x.beginPhase(ph.ID, subject, "", "human", tip)
}

// openMRs lists the run's merge requests the last poll saw open — every
// recorded one before the first poll.
func (x *exec) openMRs() []*runcore.MRRecord {
	if x.lastObs == nil {
		out := make([]*runcore.MRRecord, 0, len(x.rs.MRs))
		for i := range x.rs.MRs {
			out = append(out, &x.rs.MRs[i])
		}
		return out
	}
	var out []*runcore.MRRecord
	for _, o := range x.lastObs {
		if o.Status.State == repoport.MROpen {
			out = append(out, o.MR)
		}
	}
	return out
}

// subjectSection tells the agent of a phase run on a subject which merge
// request it is
// working on, and that every other checkout is context only (R12).
func (x *exec) subjectSection() string {
	co := x.rs.SubjectCheckout()
	if co == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# SUBJECT\nThis phase (%s) works on the merge request of %s: ~/%s, branch %s, merging into %s.\n",
		x.rs.Phase, co.Name, co.Dir, co.Branch, co.Source)
	if mr := x.rs.SubjectMR(); mr != nil {
		fmt.Fprintf(&b, "Merge request: %s\n", mr.Ref.URL)
	}
	if len(x.rs.Checkouts) > 1 {
		b.WriteString("Edit files in that checkout only; the other repositories are read-only context here.\n")
	}
	return b.String()
}

// PhaseInfo is one phase of a run's flow — its main chain, a watcher or an
// outcome: what opens it, its start policy, how many of its runs are left
// per merge request, and its live run if any (the run page's buttons).
type PhaseInfo struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Signal opens the phase: manual, or one the engine observes; empty for
	// the main chain, which runs when the run is started.
	Signal string `json:"signal"`
	// Start is the policy applied when the signal holds (auto, confirm,
	// manual); Confirm mirrors start == confirm for the run page's labels.
	Start   string `json:"start"`
	Confirm bool   `json:"confirm,omitempty"`
	// Kind is the section the phase is declared in: main (what the run
	// started with), watcher, or outcome (fires once and ends the run —
	// Then says how: done or abort).
	Kind string `json:"kind"`
	Then string `json:"then,omitempty"`
	// QuietFor is the debounce in seconds (review comments).
	QuietFor int `json:"quiet_for_s,omitempty"`
	// Max is the runs allowed per merge request (-1 = unlimited).
	Max int `json:"max"`
	// Runs counts the phase's runs by checkout name ("" = on no merge
	// request); Total sums them.
	Runs  map[string]int `json:"runs"`
	Total int            `json:"total"`
	// Live is the phase's run in progress or parked at a wait point.
	Live *PhaseLive `json:"live,omitempty"`
	// Holds are the subjects the phase's signal held for at the last idle
	// tick (what a start: manual phase offers to start by hand).
	Holds []runcore.TriggerHold `json:"holds,omitempty"`
}

// PhaseLive describes a phase's live run.
type PhaseLive struct {
	Subject  string `json:"subject,omitempty"`
	ParkedAt string `json:"parked_at,omitempty"`
}

// RunPhases lists every phase of the run's pinned flow with how many times
// each ran on each merge request and which one is live.
func (e *Engine) RunPhases(ctx context.Context, runID string) ([]PhaseInfo, error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	v, err := e.runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	fl, err := e.RunFlow(ctx, v)
	if err != nil {
		return nil, err
	}
	rs := runcore.Replay(evs, fl)
	runs := map[string]map[string]int{}
	for _, ev := range evs {
		if p, ok := ev.Payload.(*event.PhaseStarted); ok {
			if runs[p.Phase] == nil {
				runs[p.Phase] = map[string]int{}
			}
			runs[p.Phase][p.Subject]++
		}
	}
	out := []PhaseInfo{}
	for i := range fl.Phases {
		ph := &fl.Phases[i]
		r := runs[ph.ID]
		if r == nil {
			r = map[string]int{}
		}
		total := 0
		for _, n := range r {
			total += n
		}
		info := PhaseInfo{ID: ph.ID, Description: ph.Description, Signal: ph.On.Signal, Start: string(ph.On.Start),
			Confirm: ph.On.Start == flow.StartConfirm, Kind: string(ph.Kind), Then: ph.Then,
			QuietFor: int(ph.On.QuietFor.Seconds()), Max: ph.On.Max, Runs: r, Total: total,
			Holds: e.holds.of(runID, ph.ID)}
		switch {
		case rs.Parked[ph.ID] != nil:
			info.Live = &PhaseLive{Subject: rs.Parked[ph.ID].Subject, ParkedAt: rs.Parked[ph.ID].ParkedAt}
		case !rs.Done && !rs.Idle && rs.Phase == ph.ID:
			info.Live = &PhaseLive{Subject: rs.Subject}
		}
		out = append(out, info)
	}
	return out, nil
}
