package engine

// Phases (PLAN6, FLOWLANG §1): a flow is chains of stages, each opened by a
// wait point. A run begins at an entrypoint (on: run_start — the one its
// creation named); when a phase run ends or parks at a wait point mid-chain
// the run is idle, and the idle loop is the one scheduler: a firing phase
// opens when its signal holds for one merge request (its subject), under its
// start policy; a person starts any phase from the run page; one phase run
// holds the workspace at a time. The run ends from its merge requests: all
// merged completes it, one closed abandons it.

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

// beginRun opens the run's first phase — the entrypoint its creation named
// (run_created) — through the same path every other phase start takes.
// Replay decides: a log that already holds a phase_started has begun.
func (x *exec) beginRun() error {
	rs := x.rs
	if rs.phase != "" || rs.idle || rs.done {
		return nil
	}
	begin := rs.begin
	if begin == "" {
		begin = rs.fl.Beginning().ID
	}
	if rs.fl.Phase(begin) == nil {
		return x.fail(fmt.Sprintf("flow %q changed: the run's beginning phase %q no longer exists in it", rs.fl.Name, begin))
	}
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
	return x.beginPhase(begin, "", "", by, "")
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
// chain exhausted, or a person giving up at the debug leaf. In an entrypoint
// or an outcome that ends the run — it is abandoned: the delivery failed, or
// its end did. Any other phase — a reaction, a phase started by hand — is
// one round of something the run may do any number of times: the round
// ends, its phase_completed saying why, and the run goes back to waiting
// (FLOWLANG §3).
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
// end ends the run rather than the round: an entrypoint or an outcome.
func (x *exec) endsRunAtDeadEnd() bool {
	ph := x.rs.fl.Phase(x.rs.phase)
	return ph == nil || ph.IsEntrypoint() || ph.IsOutcome()
}

// subjectCheckout is the checkout a reaction phase acts on, or nil (the
// entry phase, or a flow without phases).
func (x *exec) subjectCheckout() *event.Checkout {
	if x.rs.subject == "" {
		return nil
	}
	return x.rs.checkout(x.rs.subject)
}

// subjectMR is the merge request a reaction phase acts on, or nil.
func (x *exec) subjectMR() *mrRecord {
	co := x.subjectCheckout()
	if co == nil {
		return nil
	}
	return x.rs.mrFor(co.Name, co.Branch)
}

// mrObservation is one poll of one merge request.
type mrObservation struct {
	mr      *mrRecord
	status  ports.MRStatus
	threads []ports.ReviewThread // nil unless fetched this tick
}

// idler is the idle loop's memory between ticks: what is not run history
// (the forge is the source of truth for it) and so lives only in the loop.
type idler struct {
	threadsAt map[string]time.Time
	threads   map[string][]ports.ReviewThread
	failures  int
}

// idle runs while the run sits between phases: it polls the forge for the
// run's ending and the reaction triggers, takes confirmation answers and
// manual starts, and returns once a phase starts or the run ends.
func (x *exec) idle() error {
	rs := x.rs
	x.h.idle.Store(true)
	defer x.h.idle.Store(false)
	// The slot this loop held is free while it idles: admit a queued run.
	lim := x.e.limits(x.bg)
	x.e.mu.Lock()
	x.e.scheduleLocked(lim)
	x.e.mu.Unlock()

	st := &idler{threadsAt: map[string]time.Time{}, threads: map[string][]ports.ReviewThread{}}
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
		x.e.log.Warn("idle: forge poll failed", "run", rs.runID, "err", err, "streak", st.failures)
		if st.failures == 3 {
			x.e.notify(x.bg, ports.Notification{RunID: rs.runID, Type: event.PingBlocker,
				Title: "Cannot reach the forge to watch this run's merge requests", Body: err.Error()})
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

	// Outcomes first: an end that holds beats any reaction. Merged is every
	// merge request merged, with no subject; closed is any one closed
	// unmerged, that one being the subject. Each fires once (FLOWLANG §1).
	for i := range rs.fl.Phases {
		ph := &rs.fl.Phases[i]
		if !ph.IsOutcome() || rs.parked[ph.ID] != nil {
			continue
		}
		if held, _, _ := x.waitState(ph.On.Signal, obs); !held {
			continue
		}
		subject, link, key, why := outcomeOf(ph, obs)
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

	for _, o := range obs {
		if o.status.State != ports.MROpen {
			continue
		}
		for i := range rs.fl.Phases {
			ph := &rs.fl.Phases[i]
			if !ph.Fires() || rs.parked[ph.ID] != nil {
				continue
			}
			spent := ph.On.Max != flow.UnlimitedMax && rs.phaseRuns[phaseRunKey(ph.ID, o.mr.Repo)] >= ph.On.Max
			held, key, why := x.triggerHolds(ph, o)
			if !held {
				continue
			}
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

// outcomeOf names what an outcome fires on: the subject (the closed merge
// request's checkout; none for a merge), a link for the human, the
// occurrence key, and a sentence.
func outcomeOf(ph *flow.Phase, obs []mrObservation) (subject, link, key, why string) {
	switch ph.On.Signal {
	case signalspec.MRClosed:
		for _, o := range obs {
			if o.status.State == ports.MRClosed {
				return o.mr.Repo, o.mr.Ref.URL, "closed:" + o.mr.Repo,
					fmt.Sprintf("The merge request %s was closed without being merged.", o.mr.Ref.URL)
			}
		}
	case signalspec.MRMerged:
		if len(obs) > 0 {
			link = obs[0].mr.Ref.URL
		}
		return "", link, "merged", "Every merge request merged."
	}
	return "", "", ph.On.Signal, ""
}

// observe polls every merge request's status, and its threads when a
// debounced trigger needs them and they are due.
func (x *exec) observe(st *idler) ([]mrObservation, error) {
	rs := x.rs
	needThreads := false
	for _, ph := range rs.fl.Phases {
		if ph.On.Signal == signalspec.MRReviewSettled {
			needThreads = true
		}
	}
	every := x.e.cfg.ThreadPollInterval
	if every <= 0 {
		every = time.Minute
	}
	out := make([]mrObservation, 0, len(rs.mrs))
	for i := range rs.mrs {
		mr := &rs.mrs[i]
		forge := x.res.forgeFor(mr.Repo)
		if forge == nil {
			continue
		}
		status, err := forge.MRStatus(x.ctx, mr.Ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mr.Ref.URL, err)
		}
		o := mrObservation{mr: mr, status: status}
		if needThreads && status.State == ports.MROpen {
			k := mr.Repo + "\x00" + mr.Branch
			if last, ok := st.threadsAt[k]; !ok || x.e.clock().Sub(last) >= every {
				threads, err := forge.MRThreads(x.ctx, mr.Ref)
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

// triggerHolds evaluates one reaction phase's trigger for one merge request:
// whether it holds, the occurrence key, and a sentence for a human.
func (x *exec) triggerHolds(ph *flow.Phase, o mrObservation) (bool, string, string) {
	switch ph.On.Signal {
	case signalspec.Manual:
		// Never holds on its own: a person begins the phase (manualStart).
		return false, "", ""
	case signalspec.MRConflicts:
		if o.status.Mergeable != ports.MergeConflicts {
			return false, "", ""
		}
		target := o.status.Target
		if target == "" {
			target = o.mr.Target
		}
		return true, o.status.TargetSHA + ".." + o.status.HeadSHA,
			fmt.Sprintf("The merge request %s no longer merges cleanly into %s.", o.mr.Ref.URL, target)
	case signalspec.MRReviewSettled:
		pending := x.unskipped(pendingThreads(o.threads))
		newest, ok := newestPending(pending)
		if !ok || x.e.clock().Sub(newest.CreatedAt) < ph.On.QuietFor {
			return false, "", ""
		}
		return true, newest.ID, fmt.Sprintf("%d review thread(s) on %s are waiting, the last comment %s ago.",
			len(pending), o.mr.Ref.URL, x.e.clock().Sub(newest.CreatedAt).Round(time.Minute))
	}
	return false, "", ""
}

// askToStart raises a phase_confirm ping, once per (phase, subject, key).
func (x *exec) askToStart(ph *flow.Phase, subject, link, key, why string) error {
	rs := x.rs
	for _, cp := range rs.confirms {
		if cp.Phase == ph.ID && cp.Subject == subject && (cp.TriggerKey == key || rs.answeredPings[cp.PingID] == nil) {
			return nil // already asked (or a question about it is still open)
		}
	}
	q := fmt.Sprintf("%s Run phase %q on it?", why, ph.ID)
	if ph.IsOutcome() {
		q = fmt.Sprintf("%s Run phase %q and end the run?", why, ph.ID)
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

// manualStart starts a phase a human asked for, from the idle loop: any
// phase without a live run, on the named checkout's merge request, else on
// the run's single open one, else on none.
func (x *exec) manualStart(req phaseStartReq) error {
	rs := x.rs
	ph := rs.fl.Phase(req.phase)
	if ph == nil {
		return fmt.Errorf("%w: no phase %q in this flow", ErrPhaseNotStartable, req.phase)
	}
	if pr := rs.parked[ph.ID]; pr != nil {
		return fmt.Errorf("%w: phase %q is parked at %s", ErrPhaseNotStartable, ph.ID, pr.parkedAt)
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
		if forge := x.res.forgeFor(subject); forge != nil {
			if st, err := forge.MRStatus(x.ctx, mr.Ref); err == nil {
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

// subjectSection tells a reaction phase's agent which merge request it is
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

// PhaseInfo is one phase of a run's flow: what opens it, its start policy,
// how many of its runs are left per merge request, and its live run if any
// (the run page's buttons).
type PhaseInfo struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Signal      string `json:"signal"`
	// Start is the policy applied when the signal holds (auto, confirm,
	// manual); Confirm mirrors start == confirm for the run page's labels.
	Start   string `json:"start"`
	Confirm bool   `json:"confirm,omitempty"`
	// Entry marks a phase a run may begin at (on: run_start). Outcome marks
	// one that ends the run; Then is what a stage-less outcome does (done or
	// abort).
	Entry   bool   `json:"entry,omitempty"`
	Outcome bool   `json:"outcome,omitempty"`
	Then    string `json:"then,omitempty"`
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
			Confirm: ph.On.Start == flow.StartConfirm, Entry: ph.IsEntrypoint(), Outcome: ph.IsOutcome(), Then: ph.Then,
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
