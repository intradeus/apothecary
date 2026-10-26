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
	"apothecary/internal/ports"
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
	if rs.phase != "" || rs.idle || rs.done {
		return nil
	}
	main := rs.fl.Main()
	if main == nil {
		rs.idle = true
		return nil
	}
	begin := main.ID
	by := rs.view.StartedBy
	if by == "" {
		by = "human"
	}
	if rs.anyStarted {
		// Stage events but no phase start (a log written by an older build,
		// or by hand): record the start without moving the cursor replay
		// placed — the run is already mid-phase.
		p := &event.PhaseStarted{Phase: begin, By: by}
		if err := x.append(p); err != nil {
			return err
		}
		rs.phase = begin
		rs.phaseRuns[phaseRunKey(begin, "")]++
		return nil
	}
	// A run started on someone's merge request begins on it: the merge
	// request is the main chain's subject, at the head it was started at.
	if m := rs.adopted; m != nil && main.OnMR() {
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
	co := x.subjectCheckout()
	if co == nil || co.Fetch == "" || rs.phaseTip == "" || rs.followed == rs.phaseTip {
		return
	}
	rs.followed = rs.phaseTip
	res, err := x.gitIn(co.Dir, fmt.Sprintf("git fetch origin %s && git merge --ff-only FETCH_HEAD", shellQuote(co.Fetch)))
	if err != nil || res.ExitCode != 0 {
		x.e.log.Warn("could not bring the merge request's checkout to its head", "run", rs.runID, "repo", co.Name,
			"head", rs.phaseTip, "err", err, "out", tail(res.Output, 512))
	}
}

// phaseDone ends the phase in progress (FLOWLANG §3 end): the run goes
// idle, and the next idle tick decides what follows — a parked phase
// resuming, another phase opening, or the run's end (endCheck). A phase that
// ended at a dead end says why.
func (x *exec) phaseDone() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.phase, Subject: rs.subject, Reason: rs.endReason}); err != nil {
		return err
	}
	rs.endReason = ""
	rs.idle = true
	return nil
}

// runDone is the flow's own end of the run (FLOWLANG §3 done): the phase
// finishes and the run completes with it, whatever else is parked or armed —
// their open questions are withdrawn with the run.
func (x *exec) runDone() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.phase, Subject: rs.subject}); err != nil {
		return err
	}
	return x.complete()
}

// runAbort is the flow abandoning the run from a success: an outcome's
// `then: abort`, or `on_success: abort` (FLOWLANG §3).
func (x *exec) runAbort() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.phase, Subject: rs.subject}); err != nil {
		return err
	}
	return x.abandon(rs.abortAt, "", rs.abortReason)
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
		"run", x.rs.runID, "phase", x.rs.phase, "stage", st.ID, "reason", reason)
	x.endPhase()
	x.rs.endReason = reason
	return nil
}

// endsRunAtDeadEnd reports whether the phase in progress is one whose dead
// end ends the run rather than the round: the main chain or an outcome.
func (x *exec) endsRunAtDeadEnd() bool {
	ph := x.rs.fl.Phase(x.rs.phase)
	return ph == nil || ph.Kind != flow.PhaseWatcher
}

// subjectCheckout is the checkout the phase run acts on, or nil (the main
// chain of a run started on a ticket).
func (x *exec) subjectCheckout() *event.Checkout {
	if x.rs.subject == "" {
		return nil
	}
	return x.rs.checkout(x.rs.subject)
}

// subjectMR is the merge request the phase run acts on, or nil.
func (x *exec) subjectMR() *mrRecord {
	co := x.subjectCheckout()
	if co == nil {
		return nil
	}
	return x.rs.mrFor(co.Name, co.Branch)
}

// mrObservation is one poll of one merge request. threads, checks and
// approval are read only when the flow has a signal that needs them (nil
// otherwise); headSince is when this poll's head was first seen.
type mrObservation struct {
	mr        *mrRecord
	status    ports.MRStatus
	threads   []ports.ReviewThread
	checks    *ports.Checks
	approval  *ports.Approval
	headSince time.Time
}

// idler is the idle loop's memory between ticks: what is not run history (the
// repository host is the source of truth for it) and so lives only in the
// loop. Everything is per merge request (checkout + branch).
type idler struct {
	threadsAt map[string]time.Time
	threads   map[string][]ports.ReviewThread
	checks    map[string]polled[ports.Checks]
	approvals map[string]polled[ports.Approval]
	heads     map[string]polled[string]
	failures  int
}

// polled is a value read from the repository host and when.
type polled[T any] struct {
	v  T
	at time.Time
}

func newIdler() *idler {
	return &idler{threadsAt: map[string]time.Time{}, threads: map[string][]ports.ReviewThread{},
		checks: map[string]polled[ports.Checks]{}, approvals: map[string]polled[ports.Approval]{}, heads: map[string]polled[string]{}}
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
	if err := x.evaluateIdle(st); err != nil || !rs.idle || rs.done {
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
		if err := x.evaluateIdle(st); err != nil || !rs.idle || rs.done {
			return err
		}
	}
}

// evaluateIdle is one idle tick (the scheduler): observe once, resume the
// first parked phase run whose wait point settled, act on answered
// confirmations, open the first firing phase whose signal holds under its
// start policy — subjects in checkout order, phases in document order, one
// start per tick — record every hold for the run page, and decide whether
// the run is over.
func (x *exec) evaluateIdle(st *idler) error {
	rs := x.rs
	obs, err := x.observe(st)
	x.lastObs = obs
	if err != nil {
		if errors.Is(err, context.Canceled) || x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		st.failures++
		x.e.log.Warn("idle: repository poll failed", "run", rs.runID, "err", err, "streak", st.failures)
		if st.failures == 3 {
			x.e.notify(x.bg, ports.Notification{RunID: rs.runID, Type: event.PingBlocker,
				Title: "Cannot reach the repository to watch this run's merge requests", Body: err.Error()})
		}
		return nil
	}
	st.failures = 0

	// Parked phase runs first, in document order: a wait point that settled
	// resumes its phase before any other phase opens.
	for i := range rs.fl.Phases {
		pr := rs.parked[rs.fl.Phases[i].ID]
		if pr == nil {
			continue
		}
		if resumed, err := x.tryResume(pr, obs); resumed || err != nil {
			return err
		}
	}

	// A confirmation answered (here, or while another phase ran): approve
	// starts it now, reject records the decline (R5, R6).
	for _, cp := range rs.confirms {
		ans := rs.answeredPings[cp.PingID]
		if ans == nil || rs.triggerSeen[triggerKey(cp.Phase, cp.Subject, cp.TriggerKey)] {
			continue
		}
		if ans.Answer != "approve" {
			if err := x.append(&event.PhaseDeclined{Phase: cp.Phase, Subject: cp.Subject, Key: cp.TriggerKey}); err != nil {
				return err
			}
			rs.triggerSeen[triggerKey(cp.Phase, cp.Subject, cp.TriggerKey)] = true
			continue
		}
		var tip string
		for _, o := range obs {
			if o.mr.Repo == cp.Subject {
				tip = o.status.HeadSHA
			}
		}
		return x.beginPhase(cp.Phase, cp.Subject, cp.TriggerKey, "signal", tip)
	}

	holds := map[string][]TriggerHold{}
	defer x.e.holds.set(rs.runID, holds)

	// Outcomes first: an end that holds beats any watcher. Each fires once
	// (FLOWLANG §1).
	for i := range rs.fl.Phases {
		ph := &rs.fl.Phases[i]
		if !ph.IsOutcome() || rs.parked[ph.ID] != nil {
			continue
		}
		held, subject, link, key, why := x.outcomeHolds(ph.On.Signal, obs)
		if !held {
			continue
		}
		seen := rs.triggerSeen[triggerKey(ph.ID, subject, key)]
		holds[ph.ID] = append(holds[ph.ID], TriggerHold{Subject: subject, Key: key, Why: why, Seen: seen, At: x.e.clock()})
		if seen {
			continue
		}
		switch ph.On.Start {
		case flow.StartManual:
			continue
		case flow.StartConfirm:
			if err := x.askToStart(ph, subject, link, key, why); err != nil {
				return err
			}
			continue
		}
		return x.beginPhase(ph.ID, subject, key, "signal", "")
	}

	// Watchers, per merge request: each new occurrence of the signal on one
	// is a round of the watcher on it.
	for _, o := range obs {
		for i := range rs.fl.Phases {
			ph := &rs.fl.Phases[i]
			if !ph.Fires() || rs.parked[ph.ID] != nil {
				continue
			}
			s := x.signalOn(ph.On.Signal, ph.On.QuietFor, o)
			if !s.holds {
				continue
			}
			key, why := s.key, s.why
			spent := ph.On.Max != flow.UnlimitedMax && rs.phaseRuns[phaseRunKey(ph.ID, o.mr.Repo)] >= ph.On.Max
			seen := spent || rs.triggerSeen[triggerKey(ph.ID, o.mr.Repo, key)]
			holds[ph.ID] = append(holds[ph.ID], TriggerHold{Subject: o.mr.Repo, Key: key, Why: why, Seen: seen, At: x.e.clock()})
			if seen {
				continue
			}
			switch ph.On.Start {
			case flow.StartManual:
				// Surfaced, never started by itself: a person starts it from
				// the run page (manualStart).
				continue
			case flow.StartConfirm:
				if err := x.askToStart(ph, o.mr.Repo, o.mr.Ref.URL, key, why); err != nil {
					return err
				}
				continue
			}
			return x.beginPhase(ph.ID, o.mr.Repo, key, "signal", o.status.HeadSHA)
		}
	}
	return x.endCheck(obs)
}

// outcomeHolds evaluates an outcome's signal on the run's merge requests:
// whether it holds, its subject, a link for the human, the occurrence key
// and a sentence. A signal that waits for every merge request
// (signalspec.All: merged, approved, mergeable) has no subject; any other
// holds as soon as one merge request has it, and that one is the subject.
func (x *exec) outcomeHolds(slug string, obs []mrObservation) (held bool, subject, link, key, why string) {
	sig := signalspec.Get(slug)
	if !sig.Observed() || len(obs) == 0 {
		return false, "", "", "", ""
	}
	if sig.All {
		if held, _, _ := x.waitState(slug, "", obs); !held {
			return false, "", "", "", ""
		}
		why = everyOne[slug]
		if s := x.signalOn(slug, 0, obs[0]); len(obs) == 1 && s.holds {
			why = s.why
		}
		return true, "", obs[0].mr.Ref.URL, slug, why
	}
	for _, o := range obs {
		if s := x.signalOn(slug, 0, o); s.holds {
			return true, o.mr.Repo, o.mr.Ref.URL, s.key, s.why
		}
	}
	return false, "", "", "", ""
}

// observe polls every merge request's status, and its threads when a
// debounced trigger needs them and they are due.
func (x *exec) observe(st *idler) ([]mrObservation, error) {
	rs := x.rs
	reads := readsOf(rs.fl)
	every := x.e.cfg.ThreadPollInterval
	if every <= 0 {
		every = time.Minute
	}
	now := x.e.clock()
	out := make([]mrObservation, 0, len(rs.mrs))
	for i := range rs.mrs {
		mr := &rs.mrs[i]
		repoHost := x.res.repoHostFor(mr.Repo)
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
		o := mrObservation{mr: mr, status: status, headSince: st.heads[k].at}
		if reads.checks && status.State == ports.MROpen {
			// A head's checks are asked for when the head is new, while they
			// run, and otherwise at the thread cadence (a re-run turns them).
			c, ok := st.checks[k]
			stale := !ok || c.v.Head != status.HeadSHA || c.v.State == ports.CheckPending || now.Sub(c.at) >= every ||
				(c.v.State == ports.ChecksNone && now.Sub(o.headSince) < every)
			if stale {
				checks, err := repoHost.MRChecks(x.ctx, mr.Ref, status.HeadSHA)
				if err != nil {
					return nil, fmt.Errorf("%s checks: %w", mr.Ref.URL, err)
				}
				st.checks[k] = polled[ports.Checks]{checks, now}
			}
			v := st.checks[k].v
			o.checks = &v
		}
		if reads.approval && status.State == ports.MROpen {
			if a, ok := st.approvals[k]; !ok || now.Sub(a.at) >= every {
				ap, err := repoHost.MRApproval(x.ctx, mr.Ref)
				if err != nil {
					return nil, fmt.Errorf("%s approval: %w", mr.Ref.URL, err)
				}
				st.approvals[k] = polled[ports.Approval]{ap, now}
			}
			v := st.approvals[k].v
			o.approval = &v
		}
		if reads.threads && status.State == ports.MROpen {
			if last, ok := st.threadsAt[k]; !ok || x.e.clock().Sub(last) >= every {
				threads, err := repoHost.MRThreads(x.ctx, mr.Ref)
				if err != nil {
					return nil, fmt.Errorf("%s threads: %w", mr.Ref.URL, err)
				}
				st.threads[k], st.threadsAt[k] = threads, x.e.clock()
			}
			o.threads = st.threads[k]
		}
		out = append(out, o)
	}
	return out, nil
}

// everyOne is what a signal that waits for every merge request says once
// it holds on a run with several.
var everyOne = map[string]string{
	signalspec.MRMerged:    "Every merge request merged.",
	signalspec.MRApproved:  "Every merge request is approved.",
	signalspec.MRMergeable: "Every merge request can be merged now.",
}

// mrSignal is what an observed signal says about one merge request: it
// holds — an occurrence, with its key and a sentence for a human; the merge
// request is past it (merged: past approval and past being mergeable — held
// for a wait or an outcome, never an occurrence for a watcher); or it can
// never hold there again (gone, and why).
type mrSignal struct {
	holds, passed, gone bool
	key, why            string
}

// signalOn evaluates an observed signal on one merge request. quiet is the
// debounce: a watcher's quiet_for, zero for a wait, a gate or an outcome.
func (x *exec) signalOn(slug string, quiet time.Duration, o mrObservation) mrSignal {
	url := o.mr.Ref.URL
	switch o.status.State {
	case ports.MRMerged:
		switch slug {
		case signalspec.MRMerged:
			return mrSignal{holds: true, key: "merged", why: fmt.Sprintf("The merge request %s merged.", url)}
		case signalspec.MRApproved, signalspec.MRMergeable:
			return mrSignal{passed: true}
		}
		return mrSignal{gone: true, why: "the merge request merged: " + url}
	case ports.MRClosed:
		if slug == signalspec.MRClosed {
			return mrSignal{holds: true, key: "closed", why: fmt.Sprintf("The merge request %s was closed without being merged.", url)}
		}
		return mrSignal{gone: true, why: "merge request closed unmerged: " + url}
	}
	switch slug {
	case signalspec.MRApproved:
		// One occurrence per head it was approved at: a push after the
		// approval, approved again, is a new one.
		if o.approval == nil || !o.approval.Approved {
			return mrSignal{}
		}
		return mrSignal{holds: true, key: o.status.HeadSHA, why: fmt.Sprintf("The merge request %s is approved.", url)}
	case signalspec.MRConflicts:
		if o.status.Mergeable != ports.MergeConflicts {
			return mrSignal{}
		}
		target := o.status.Target
		if target == "" {
			target = o.mr.Target
		}
		return mrSignal{holds: true, key: o.status.TargetSHA + ".." + o.status.HeadSHA,
			why: fmt.Sprintf("The merge request %s no longer merges cleanly into %s.", url, target)}
	case signalspec.MRReviewSettled:
		pending := x.unskipped(pendingThreads(o.threads))
		newest, ok := newestPending(pending)
		if !ok || x.e.clock().Sub(newest.CreatedAt) < quiet {
			return mrSignal{}
		}
		return mrSignal{holds: true, key: newest.ID, why: fmt.Sprintf("%d review thread(s) on %s are waiting, the last comment %s ago.",
			len(pending), url, x.e.clock().Sub(newest.CreatedAt).Round(time.Minute))}
	case signalspec.CIFailed:
		// The checks of the head this poll saw, failed: one occurrence per
		// head and set of failed checks.
		if o.checks == nil || o.checks.Head != o.status.HeadSHA || o.checks.State != ports.CheckFailure {
			return mrSignal{}
		}
		failed := o.checks.Failed()
		return mrSignal{holds: true, key: o.status.HeadSHA + ":" + strings.Join(failed, ","),
			why: fmt.Sprintf("CI failed on %s: %s.", url, strings.Join(failed, ", "))}
	case signalspec.MRMergeable:
		if !o.status.MergeableNow {
			return mrSignal{}
		}
		return mrSignal{holds: true, key: o.status.HeadSHA, why: fmt.Sprintf("The repository says %s can be merged now.", url)}
	case signalspec.MRHeadMoved:
		head := o.status.HeadSHA
		if head == "" || x.rs.knownHead(o.mr.Repo, head) {
			return mrSignal{}
		}
		if quiet > 0 && x.e.clock().Sub(o.headSince) < quiet {
			return mrSignal{}
		}
		return mrSignal{holds: true, key: head, why: fmt.Sprintf("Someone pushed to %s: its head is now %s, a commit this run did not push.", url, shortSHA(head))}
	}
	return mrSignal{}
}

// shortSHA is a commit id as people read it.
func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// askToStart raises a phase_confirm ping, once per (phase, subject, key).
func (x *exec) askToStart(ph *flow.Phase, subject, link, key, why string) error {
	rs := x.rs
	for _, cp := range rs.confirms {
		if cp.Phase == ph.ID && cp.Subject == subject && (cp.TriggerKey == key || rs.answeredPings[cp.PingID] == nil) {
			return nil // already asked (or a question about it is still open)
		}
	}
	q := fmt.Sprintf("%s Run the %q watcher on it?", why, ph.ID)
	if ph.IsOutcome() {
		q = fmt.Sprintf("%s Run the %q outcome and end the run?", why, ph.ID)
	}
	if ph.Description != "" {
		q += " (" + ph.Description + ")"
	}
	p := &Ping{ID: x.e.newID(), RunID: rs.runID, Type: event.PingPhaseConfirm, Question: q,
		Link: link, RaisedAt: x.e.clock(), AnswerKind: event.AnswerDecision, Phase: ph.ID,
		done: make(chan struct{})}
	x.e.mu.Lock()
	x.e.pings[p.ID] = p
	x.e.mu.Unlock()
	raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: q, Link: p.Link, AnswerKind: event.AnswerDecision,
		Phase: ph.ID, Subject: subject, TriggerKey: key}
	if err := x.append(raised); err != nil {
		return err
	}
	rs.confirms = append(rs.confirms, raised)
	x.e.notify(x.bg, ports.Notification{RunID: rs.runID, PingID: p.ID, Type: p.Type, Title: q, Body: p.Link})
	return nil
}

// beginPhase records a phase start and moves the cursor to its first stage.
func (x *exec) beginPhase(phase, subject, key, by, tip string) error {
	p := &event.PhaseStarted{Phase: phase, Subject: subject, Key: key, By: by, Tip: tip}
	if err := x.append(p); err != nil {
		return err
	}
	x.rs.startPhase(p, x.rs.fl)
	return nil
}

// manualStart starts a phase a human asked for, from the idle loop: a
// watcher or an outcome without a live run, on the named checkout's merge
// request, else on the run's single open one, else on none. The main chain
// runs once, when the run is started.
func (x *exec) manualStart(req phaseStartReq) error {
	rs := x.rs
	ph := rs.fl.Phase(req.phase)
	if ph == nil {
		return fmt.Errorf("%w: no phase %q in this flow", ErrPhaseNotStartable, req.phase)
	}
	if pr := rs.parked[ph.ID]; pr != nil {
		return fmt.Errorf("%w: phase %q is parked at %s", ErrPhaseNotStartable, ph.ID, pr.parkedAt)
	}
	if ph.Kind == flow.PhaseMain {
		return fmt.Errorf("%w: the main chain runs once, when the run is started — rerun the run to run it again", ErrPhaseNotStartable)
	}
	var subject string
	var mr *mrRecord
	if req.subject != "" {
		co := rs.checkout(req.subject)
		if co == nil || rs.mrFor(co.Name, co.Branch) == nil {
			return fmt.Errorf("%w: no merge request for repository %q", ErrPhaseNotStartable, req.subject)
		}
		subject, mr = co.Name, rs.mrFor(co.Name, co.Branch)
	} else if open := x.openMRs(); len(open) == 1 {
		subject, mr = open[0].Repo, open[0]
	}
	if ph.On.Max != flow.UnlimitedMax && rs.phaseRuns[phaseRunKey(ph.ID, subject)] >= ph.On.Max {
		on := "with no merge request"
		if subject != "" {
			on = "on " + subject
		}
		return fmt.Errorf("%w: phase %q already ran %d times %s (max)", ErrPhaseNotStartable, ph.ID, ph.On.Max, on)
	}
	var tip string
	if mr != nil {
		if repoHost := x.res.repoHostFor(subject); repoHost != nil {
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
func (x *exec) openMRs() []*mrRecord {
	if x.lastObs == nil {
		out := make([]*mrRecord, 0, len(x.rs.mrs))
		for i := range x.rs.mrs {
			out = append(out, &x.rs.mrs[i])
		}
		return out
	}
	var out []*mrRecord
	for _, o := range x.lastObs {
		if o.status.State == ports.MROpen {
			out = append(out, o.mr)
		}
	}
	return out
}

// subjectSection tells the agent of a phase run on a subject which merge
// request it is
// working on, and that every other checkout is context only (R12).
func (x *exec) subjectSection() string {
	co := x.subjectCheckout()
	if co == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# SUBJECT\nThis phase (%s) works on the merge request of %s: ~/%s, branch %s, merging into %s.\n",
		x.rs.phase, co.Name, co.Dir, co.Branch, co.Source)
	if mr := x.subjectMR(); mr != nil {
		fmt.Fprintf(&b, "Merge request: %s\n", mr.Ref.URL)
	}
	if len(x.rs.checkouts) > 1 {
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
	Holds []TriggerHold `json:"holds,omitempty"`
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
	v, err := e.runView(ctx, runID)
	if err != nil {
		return nil, err
	}
	fl, err := e.runFlow(ctx, v)
	if err != nil {
		return nil, err
	}
	rs := replay(evs, fl)
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
		case rs.parked[ph.ID] != nil:
			info.Live = &PhaseLive{Subject: rs.parked[ph.ID].subject, ParkedAt: rs.parked[ph.ID].parkedAt}
		case !rs.done && !rs.idle && rs.phase == ph.ID:
			info.Live = &PhaseLive{Subject: rs.subject}
		}
		out = append(out, info)
	}
	return out, nil
}
