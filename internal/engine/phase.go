package engine

// Phases (PLAN6): a flow describes a ticket's whole life. The entry phase is
// what the ticket starts; when it ends with merge requests open the run is
// idle, and each reaction phase runs when its trigger holds for one merge
// request — its subject — one phase run at a time, in this run, in this
// workspace. The run ends from its merge requests: all merged completes it,
// one closed abandons it.

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

// StartPhase asks an idle run to start a reaction phase on one of its merge
// requests (repo "" = the primary's). Refused unless the run is idle.
func (e *Engine) StartPhase(runID, phase, repo string) error {
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	e.mu.Lock()
	h, ok := e.handles[runID]
	e.mu.Unlock()
	if !ok || !h.idle.Load() {
		return fmt.Errorf("%w: the run is not between phases", ErrPhaseNotStartable)
	}
	req := phaseStartReq{phase: phase, subject: repo, ack: make(chan error, 1)}
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

// entryPhaseStarted records the entry phase once, so a phased run's timeline
// opens with it like every later phase.
func (x *exec) entryPhaseStarted() error {
	rs := x.rs
	if !rs.fl.Phased() || rs.phase != "" || rs.idle {
		return nil
	}
	p := &event.PhaseStarted{Phase: rs.fl.Phases[0].ID, By: flow.TriggerTicket}
	if err := x.append(p); err != nil {
		return err
	}
	// Not rs.startPhase: the cursor is wherever replay put it (a run that
	// predates this event may already be mid-entry).
	rs.phase = p.Phase
	rs.phaseRuns[phaseRunKey(p.Phase, "")]++
	return nil
}

// phaseDone ends the phase in progress: the run goes idle — or completes,
// when the entry phase opened no merge request to react to.
func (x *exec) phaseDone() error {
	rs := x.rs
	if err := x.append(&event.PhaseCompleted{Phase: rs.phase, Subject: rs.subject}); err != nil {
		return err
	}
	rs.idle = true
	if len(rs.mrs) == 0 {
		return x.complete()
	}
	return nil
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

// evaluateIdle is one idle tick: ending first, then answered confirmations,
// then the triggers — subjects in checkout order, phases in document order
// (R4). Starts at most one phase (R3).
func (x *exec) evaluateIdle(st *idler) error {
	rs := x.rs
	obs, err := x.observe(st)
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

	allMerged, closed := true, ""
	for _, o := range obs {
		switch o.status.State {
		case ports.MRClosed:
			closed = o.mr.Ref.URL
		case ports.MRMerged:
		default:
			allMerged = false
		}
	}
	if closed != "" {
		return x.abandon(rs.view.CurrentStage, "", "merge request closed unmerged: "+closed)
	}
	if allMerged {
		return x.complete()
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

	for _, o := range obs {
		if o.status.State != ports.MROpen {
			continue
		}
		for _, ph := range rs.fl.Reactions() {
			if ph.On.Max != flow.UnlimitedMax && rs.phaseRuns[phaseRunKey(ph.ID, o.mr.Repo)] >= ph.On.Max {
				continue
			}
			holds, key, why := x.triggerHolds(&ph, o)
			if !holds || rs.triggerSeen[triggerKey(ph.ID, o.mr.Repo, key)] {
				continue
			}
			if ph.On.Confirm {
				if err := x.askToStart(&ph, o, key, why); err != nil {
					return err
				}
				continue
			}
			return x.beginPhase(ph.ID, o.mr.Repo, key, "signal", o.status.HeadSHA)
		}
	}
	return nil
}

// observe polls every merge request's status, and its threads when a
// debounced trigger needs them and they are due.
func (x *exec) observe(st *idler) ([]mrObservation, error) {
	rs := x.rs
	needThreads := false
	for _, ph := range rs.fl.Reactions() {
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
func (x *exec) askToStart(ph *flow.Phase, o mrObservation, key, why string) error {
	rs := x.rs
	for _, cp := range rs.confirms {
		if cp.Phase == ph.ID && cp.Subject == o.mr.Repo && (cp.TriggerKey == key || rs.answeredPings[cp.PingID] == nil) {
			return nil // already asked (or a question about it is still open)
		}
	}
	q := fmt.Sprintf("%s Run phase %q on it?", why, ph.ID)
	if ph.Description != "" {
		q += " (" + ph.Description + ")"
	}
	p := &Ping{ID: x.e.newID(), RunID: rs.runID, Type: event.PingPhaseConfirm, Question: q,
		Link: o.mr.Ref.URL, RaisedAt: x.e.clock(), AnswerKind: event.AnswerDecision, done: make(chan struct{})}
	x.e.mu.Lock()
	x.e.pings[p.ID] = p
	x.e.mu.Unlock()
	raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: q, Link: p.Link, AnswerKind: event.AnswerDecision,
		Phase: ph.ID, Subject: o.mr.Repo, TriggerKey: key}
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

// manualStart starts a phase a human asked for, from the idle loop.
func (x *exec) manualStart(req phaseStartReq) error {
	rs := x.rs
	ph := rs.fl.Phase(req.phase)
	if ph == nil || ph.Entry() {
		return fmt.Errorf("%w: no reaction phase %q in this flow", ErrPhaseNotStartable, req.phase)
	}
	co := rs.checkout(req.subject)
	if co == nil || rs.mrFor(co.Name, co.Branch) == nil {
		return fmt.Errorf("%w: no merge request for repository %q", ErrPhaseNotStartable, req.subject)
	}
	if ph.On.Max != flow.UnlimitedMax && rs.phaseRuns[phaseRunKey(ph.ID, co.Name)] >= ph.On.Max {
		return fmt.Errorf("%w: phase %q already ran %d times on %s (max)", ErrPhaseNotStartable, ph.ID, ph.On.Max, co.Name)
	}
	mr := rs.mrFor(co.Name, co.Branch)
	var tip string
	if forge := x.res.forgeFor(co.Name); forge != nil {
		if st, err := forge.MRStatus(x.ctx, mr.Ref); err == nil {
			tip = st.HeadSHA
		}
	}
	// A human's start is its own occurrence: it never consumes the key a
	// signal would fire for, and no signal key blocks it.
	return x.beginPhase(ph.ID, co.Name, "", "human", tip)
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

// PhaseInfo is one reaction phase of a run's flow, per merge request: what
// starts it and how many of its runs are left (the run page's buttons).
type PhaseInfo struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Signal      string `json:"signal"`
	Confirm     bool   `json:"confirm,omitempty"`
	// QuietFor is the debounce in seconds (review comments).
	QuietFor int `json:"quiet_for_s,omitempty"`
	// Max is the runs allowed per merge request (-1 = unlimited).
	Max  int            `json:"max"`
	Runs map[string]int `json:"runs"` // by checkout name
}

// RunPhases lists the reaction phases of the run's pinned flow with how many
// times each ran on each merge request. Empty for a flow without phases.
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
	fl, err := e.pinnedFlow(ctx, v.ProjectID, v.FlowName, v.FlowHash)
	if err != nil {
		return nil, err
	}
	runs := map[string]map[string]int{}
	for _, ev := range evs {
		if p, ok := ev.Payload.(*event.PhaseStarted); ok && p.Subject != "" {
			if runs[p.Phase] == nil {
				runs[p.Phase] = map[string]int{}
			}
			runs[p.Phase][p.Subject]++
		}
	}
	out := []PhaseInfo{}
	for _, ph := range fl.Reactions() {
		r := runs[ph.ID]
		if r == nil {
			r = map[string]int{}
		}
		out = append(out, PhaseInfo{ID: ph.ID, Description: ph.Description, Signal: ph.On.Signal, Confirm: ph.On.Confirm,
			QuietFor: int(ph.On.QuietFor.Seconds()), Max: ph.On.Max, Runs: r})
	}
	return out, nil
}
