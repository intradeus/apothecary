package runcore

import (
	"fmt"
	"strings"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/flow/signalspec"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run/event"
)

// The scheduler's decisions, pure (FLOWLANG §1). Every wait point — a phase's
// opening `on:`, a wait stage, a gate — is the same thing: a place a chain
// stops for something outside the run, with a policy for what happens when it
// comes. PlanIdle reads one poll of the run's merge requests and says what one
// idle tick does; the engine polls and carries the plan out.

// MRObservation is one poll of one merge request. Threads, Checks and
// Approval are read only when the flow has a signal that needs them (nil
// otherwise); HeadSince is when this poll's head was first seen.
type MRObservation struct {
	MR        *MRRecord
	Status    repoport.MRStatus
	Threads   []repoport.ReviewThread
	Checks    *repoport.Checks
	Approval  *repoport.Approval
	HeadSince time.Time
}

// TriggerHold is one observation that a phase's signal holds for a subject:
// what the run page shows for a phase with start: manual, and the record
// behind every automatic start. Memory only — external state, re-observed
// on the next tick after a restart.
type TriggerHold struct {
	Subject string `json:"subject"`
	Key     string `json:"key"`
	Why     string `json:"why"`
	// Seen: a run already started (or a person declined) for this
	// occurrence, or the phase's runs on this subject are used up.
	Seen bool      `json:"seen"`
	At   time.Time `json:"observed_at"`
}

// everyOne is what a signal that waits for every merge request says once
// it holds on a run with several.
var everyOne = map[string]string{
	signalspec.MRMerged:    "Every merge request merged.",
	signalspec.MRApproved:  "Every merge request is approved.",
	signalspec.MRMergeable: "Every merge request can be merged now.",
}

// MRSignal is what an observed signal says about one merge request: it
// holds — an occurrence, with its key and a sentence for a human; the merge
// request is past it (merged: past approval and past being mergeable — held
// for a wait or an outcome, never an occurrence for a watcher); or it can
// never hold there again (gone, and why).
type MRSignal struct {
	Holds, Passed, Gone bool
	Key, Why            string
}

// SignalOn evaluates an observed signal on one merge request. quiet is the
// debounce: a watcher's quiet_for, zero for a wait, a gate or an outcome.
func (rs *State) SignalOn(slug string, quiet time.Duration, o MRObservation, now time.Time) MRSignal {
	url := o.MR.Ref.URL
	switch o.Status.State {
	case repoport.MRMerged:
		switch slug {
		case signalspec.MRMerged:
			return MRSignal{Holds: true, Key: "merged", Why: fmt.Sprintf("The merge request %s merged.", url)}
		case signalspec.MRApproved, signalspec.MRMergeable:
			return MRSignal{Passed: true}
		}
		return MRSignal{Gone: true, Why: "the merge request merged: " + url}
	case repoport.MRClosed:
		if slug == signalspec.MRClosed {
			return MRSignal{Holds: true, Key: "closed", Why: fmt.Sprintf("The merge request %s was closed without being merged.", url)}
		}
		return MRSignal{Gone: true, Why: "merge request closed unmerged: " + url}
	}
	switch slug {
	case signalspec.MRApproved:
		// One occurrence per head it was approved at: a push after the
		// approval, approved again, is a new one.
		if o.Approval == nil || !o.Approval.Approved {
			return MRSignal{}
		}
		return MRSignal{Holds: true, Key: o.Status.HeadSHA, Why: fmt.Sprintf("The merge request %s is approved.", url)}
	case signalspec.MRConflicts:
		if o.Status.Mergeable != repoport.MergeConflicts {
			return MRSignal{}
		}
		target := o.Status.Target
		if target == "" {
			target = o.MR.Target
		}
		return MRSignal{Holds: true, Key: o.Status.TargetSHA + ".." + o.Status.HeadSHA,
			Why: fmt.Sprintf("The merge request %s no longer merges cleanly into %s.", url, target)}
	case signalspec.MRReviewSettled:
		pending := rs.Unskipped(repoport.PendingThreads(o.Threads))
		newest, ok := repoport.NewestPending(pending)
		if !ok || now.Sub(newest.CreatedAt) < quiet {
			return MRSignal{}
		}
		return MRSignal{Holds: true, Key: newest.ID, Why: fmt.Sprintf("%d review thread(s) on %s are waiting, the last comment %s ago.",
			len(pending), url, now.Sub(newest.CreatedAt).Round(time.Minute))}
	case signalspec.CIFailed:
		// The checks of the head this poll saw, failed: one occurrence per
		// head and set of failed checks.
		if o.Checks == nil || o.Checks.Head != o.Status.HeadSHA || o.Checks.State != repoport.CheckFailure {
			return MRSignal{}
		}
		failed := o.Checks.Failed()
		return MRSignal{Holds: true, Key: o.Status.HeadSHA + ":" + strings.Join(failed, ","),
			Why: fmt.Sprintf("CI failed on %s: %s.", url, strings.Join(failed, ", "))}
	case signalspec.MRMergeable:
		if !o.Status.MergeableNow {
			return MRSignal{}
		}
		return MRSignal{Holds: true, Key: o.Status.HeadSHA, Why: fmt.Sprintf("The repository says %s can be merged now.", url)}
	case signalspec.MRHeadMoved:
		head := o.Status.HeadSHA
		if head == "" || rs.KnownHead(o.MR.Repo, head) {
			return MRSignal{}
		}
		if quiet > 0 && now.Sub(o.HeadSince) < quiet {
			return MRSignal{}
		}
		return MRSignal{Holds: true, Key: head, Why: fmt.Sprintf("Someone pushed to %s: its head is now %s, a commit this run did not push.", url, shortSHA(head))}
	}
	return MRSignal{}
}

// shortSHA is a commit id as people read it.
func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// Unskipped drops the threads a human skipped in an earlier round, unless
// someone has written there since.
func (rs *State) Unskipped(threads []repoport.ReviewThread) []repoport.ReviewThread {
	var out []repoport.ReviewThread
	for _, t := range threads {
		if last, ok := rs.Skipped[t.ID]; ok && len(t.Comments) > 0 && t.Comments[len(t.Comments)-1].ID == last {
			continue
		}
		out = append(out, t)
	}
	return out
}

// WaitState evaluates an observed signal for a wait point mid-chain — a wait
// stage, a gate's auto-answer — or an outcome: it holds, it can no longer
// hold (with why), or neither yet. On the chain's subject when it has one;
// otherwise on the run's merge requests: a signal that waits for every one
// (signalspec.All — merged, approved, mergeable) holds once each has it or is
// past it and can no longer hold once one cannot; any other holds as soon as
// one merge request has it — merged and closed alike, someone decided about
// part of the run.
func (rs *State) WaitState(slug, subject string, obs []MRObservation, now time.Time) (holds, impossible bool, why string) {
	sig := signalspec.Get(slug)
	if sig != nil && sig.Needs == signalspec.NeedsMR && len(rs.MRs) == 0 {
		return false, true, "this run has opened no merge request"
	}
	if !sig.Observed() {
		return false, false, ""
	}
	var on []MRSignal
	for _, o := range obs {
		if subject == "" || o.MR.Repo == subject {
			on = append(on, rs.SignalOn(slug, 0, o, now))
		}
	}
	if len(on) == 0 {
		return false, false, ""
	}
	if sig.All {
		holds = true
		for _, s := range on {
			if s.Gone {
				return false, true, s.Why
			}
			holds = holds && (s.Holds || s.Passed)
		}
		return holds, false, ""
	}
	gone := 0
	for _, s := range on {
		if s.Holds {
			return true, false, ""
		}
		if s.Gone {
			gone, why = gone+1, s.Why
		}
	}
	if gone == len(on) {
		if len(on) > 1 {
			why = "every merge request is merged or closed"
		}
		return false, true, why
	}
	return false, false, ""
}

// outcomeHeld is an outcome's signal holding on the run's merge requests.
type outcomeHeld struct{ subject, link, key, why string }

// outcomeHolds evaluates an outcome's signal on the run's merge requests:
// whether it holds, and with it the subject, a link for the human, the
// occurrence key and a sentence. A signal that waits for every merge request
// (signalspec.All: merged, approved, mergeable) has no subject; any other
// holds as soon as one merge request has it, and that one is the subject.
func (rs *State) outcomeHolds(slug string, obs []MRObservation, now time.Time) (outcomeHeld, bool) {
	sig := signalspec.Get(slug)
	if !sig.Observed() || len(obs) == 0 {
		return outcomeHeld{}, false
	}
	if sig.All {
		if held, _, _ := rs.WaitState(slug, "", obs, now); !held {
			return outcomeHeld{}, false
		}
		why := everyOne[slug]
		if s := rs.SignalOn(slug, 0, obs[0], now); len(obs) == 1 && s.Holds {
			why = s.Why
		}
		return outcomeHeld{link: obs[0].MR.Ref.URL, key: slug, why: why}, true
	}
	for _, o := range obs {
		if s := rs.SignalOn(slug, 0, o, now); s.Holds {
			return outcomeHeld{subject: o.MR.Repo, link: o.MR.Ref.URL, key: s.Key, why: s.Why}, true
		}
	}
	return outcomeHeld{}, false
}

// anyArmed reports whether some observed phase may still open on its own:
// its policy is automatic (auto or confirm), and — a
// watcher — it has runs left on an open merge request, or — an outcome
// that has not fired — a merge request is still open for its end to come;
// or a confirmation for it is still open.
func (rs *State) anyArmed(obs []MRObservation) bool {
	anyOpen := false
	for _, o := range obs {
		anyOpen = anyOpen || o.Status.State == repoport.MROpen
	}
	for i := range rs.Flow.Phases {
		ph := &rs.Flow.Phases[i]
		if !ph.Observed() || ph.On.Start == flow.StartManual {
			continue
		}
		for _, cp := range rs.Confirms {
			if cp.Phase == ph.ID && rs.AnsweredPings[cp.PingID] == nil {
				return true
			}
		}
		if ph.IsOutcome() {
			if anyOpen && !rs.PhaseEverRan(ph.ID) {
				return true
			}
			continue
		}
		for _, o := range obs {
			if o.Status.State != repoport.MROpen {
				continue
			}
			if ph.On.Max == flow.UnlimitedMax || rs.PhaseRuns[PhaseRunKey(ph.ID, o.MR.Repo)] < ph.On.Max {
				return true
			}
		}
	}
	return false
}

// ResumeKind is what settled a parked phase run's wait point.
type ResumeKind int

const (
	// ResumeNone: nothing settled; the phase stays parked.
	ResumeNone ResumeKind = iota
	// ResumeWaitHolds: the signal a wait stage is on holds.
	ResumeWaitHolds
	// ResumeWaitImpossible: it can no longer hold (Why says so).
	ResumeWaitImpossible
	// ResumeWaitTimedOut: the wait stage's timeout ran out (Why says so).
	ResumeWaitTimedOut
	// ResumeGateAnswered: a gate's ping has its answer on the record.
	ResumeGateAnswered
)

// ResumePlan is the parked chain's wait point, settled.
type ResumePlan struct {
	Kind  ResumeKind
	Stage *flow.Stage
	// Why is the failure a wait that will not hold reports.
	Why string
	// Answer is the recorded answer of ResumeGateAnswered.
	Answer *event.PingAnswered
}

// PlanResume checks the parked chain's wait point against the latest
// observation: a wait whose signal holds completes, one whose signal can no
// longer hold or that timed out fails (on_fail / on_timeout), a gate routes
// its answer.
func (rs *State) PlanResume(obs []MRObservation, now time.Time) ResumePlan {
	pr := rs.Parked
	if pr == nil || rs.Pos < 0 || rs.Pos >= len(rs.Flow.Stages) {
		return ResumePlan{}
	}
	st := &rs.Flow.Stages[rs.Pos]
	switch st.Type {
	case flow.StageWait:
		holds, impossible, why := rs.WaitState(st.WaitFor, rs.Subject, obs, now)
		switch {
		case holds:
			return ResumePlan{Kind: ResumeWaitHolds, Stage: st}
		case impossible:
			return ResumePlan{Kind: ResumeWaitImpossible, Stage: st,
				Why: fmt.Sprintf("%s can no longer happen: %s", st.WaitFor, why)}
		case st.Timeout > 0 && !now.Before(pr.Since.Add(st.Timeout)):
			return ResumePlan{Kind: ResumeWaitTimedOut, Stage: st,
				Why: fmt.Sprintf("%s did not hold within %s", st.WaitFor, st.Timeout)}
		}
	case flow.StageHumanGate:
		if ans := rs.AnsweredPings[pr.Ping]; ans != nil {
			return ResumePlan{Kind: ResumeGateAnswered, Stage: st, Answer: ans}
		}
	}
	return ResumePlan{}
}

// PlanOutcome is the outcome that takes the cursor from a run the idle tick
// does not reach: in a stage, or dormant with no loop. Nil when none does: a
// take-over keeps it, a chain that merged the merge request itself goes on to
// its end, a wait the signal settles goes first, and a live action stage
// finishes, because what it calls cannot be taken back halfway. An outcome
// that asks first (start: confirm) waits for the hub, where it can ask.
func (rs *State) PlanOutcome(obs []MRObservation, now time.Time, dormant bool) *IdleAction {
	if rs.Done || len(rs.Holds) > 0 || rs.ChainMerged || rs.PlanResume(obs, now).Kind != ResumeNone {
		return nil
	}
	if ph := rs.Flow.Phase(rs.Phase); ph != nil && ph.IsOutcome() {
		return nil // the run's end is under way
	}
	if !dormant && rs.Ph == PhIn && rs.Pos >= 0 && rs.Pos < len(rs.Flow.Stages) && rs.Flow.Stages[rs.Pos].Type == flow.StageAction {
		return nil
	}
	for i := range rs.Flow.Phases {
		ph := &rs.Flow.Phases[i]
		if !ph.IsOutcome() || ph.On.Start != flow.StartAuto {
			continue
		}
		held, ok := rs.outcomeHolds(ph.On.Signal, obs, now)
		if ok && !rs.TriggerSeen[TriggerKey(ph.ID, held.subject, held.key)] {
			return &IdleAction{Kind: IdleBegin, Phase: ph, Subject: held.subject, Key: held.key}
		}
	}
	return nil
}

// IdleActionKind is one thing an idle tick does.
type IdleActionKind int

const (
	// IdleResume resumes the parked chain (Resume).
	IdleResume IdleActionKind = iota
	// IdleDecline records a confirmation answered no.
	IdleDecline
	// IdleAsk raises a confirmation before starting a phase.
	IdleAsk
	// IdleBegin starts a phase.
	IdleBegin
	// IdleComplete ends the run: nothing parked, nothing armed.
	IdleComplete
)

// IdleAction is one step of a tick, in the order the plan lists them.
type IdleAction struct {
	Kind   IdleActionKind
	Resume ResumePlan
	// Phase, Subject and Key name the occurrence; Link and Why are what a
	// confirmation shows; Tip is the head a started phase begins on.
	Phase        *flow.Phase
	Subject, Key string
	Link, Why    string
	Tip          string
}

// IdlePlan is one idle tick. Holds is nil when the tick ended before it
// looked for firing phases: the last tick's holds stand.
type IdlePlan struct {
	Actions []IdleAction
	Holds   map[string][]TriggerHold
}

// idleTick is the state of a tick being planned: what its earlier actions
// will have done to the run by the time a later one is carried out.
type idleTick struct {
	rs   *State
	plan IdlePlan
	seen map[string]bool // declines this tick
}

func (t *idleTick) triggerSeen(phase, subject, key string) bool {
	k := TriggerKey(phase, subject, key)
	return t.seen[k] || t.rs.TriggerSeen[k]
}

// asked reports whether (phase, subject, key) has its confirmation: asked
// about that occurrence, or a question about the phase on that subject still
// open — this tick's included.
func (t *idleTick) asked(phase, subject, key string) bool {
	for _, cp := range t.rs.Confirms {
		if cp.Phase == phase && cp.Subject == subject && (cp.TriggerKey == key || t.rs.AnsweredPings[cp.PingID] == nil) {
			return true
		}
	}
	for _, a := range t.plan.Actions {
		if a.Kind == IdleAsk && a.Phase.ID == phase && a.Subject == subject {
			return true
		}
	}
	return false
}

// fire applies a firing phase's start policy to an occurrence not yet seen:
// manual is only ever surfaced, confirm asks, auto starts. Reports whether
// the tick is over — one start per tick.
func (t *idleTick) fire(ph *flow.Phase, subject, key, link, why, tip string) bool {
	switch ph.On.Start {
	case flow.StartManual:
		return false
	case flow.StartConfirm:
		if !t.asked(ph.ID, subject, key) {
			t.plan.Actions = append(t.plan.Actions, IdleAction{Kind: IdleAsk, Phase: ph, Subject: subject, Key: key, Link: link, Why: why})
		}
		return false
	}
	t.plan.Actions = append(t.plan.Actions, IdleAction{Kind: IdleBegin, Phase: ph, Subject: subject, Key: key, Tip: tip})
	return true
}

// PlanIdle is one idle tick over one observation. The run has one cursor: a
// parked chain holds it until its wait point settles, and only an outcome may
// take it from there, since the run is over either way. At the hub the tick
// acts on answered confirmations, opens the first firing phase whose signal
// holds under its start policy — subjects in checkout order, phases in
// document order, one start per tick — and decides whether the run is over.
// Every hold is recorded for the run page.
func (rs *State) PlanIdle(obs []MRObservation, now time.Time) IdlePlan {
	// A wait point that settled resumes its chain before anything else opens.
	if r := rs.PlanResume(obs, now); r.Kind != ResumeNone {
		return IdlePlan{Actions: []IdleAction{{Kind: IdleResume, Resume: r}}}
	}
	parked := rs.Parked != nil
	t := &idleTick{rs: rs, seen: map[string]bool{}}

	// A confirmation answered (here, or while another phase ran): approve
	// starts it now, reject records the decline (R5, R6).
	for _, cp := range rs.Confirms {
		ans := rs.AnsweredPings[cp.PingID]
		if ans == nil || t.triggerSeen(cp.Phase, cp.Subject, cp.TriggerKey) {
			continue
		}
		if parked && !rs.Flow.Phase(cp.Phase).IsOutcome() {
			continue
		}
		if ans.Answer != "approve" {
			t.seen[TriggerKey(cp.Phase, cp.Subject, cp.TriggerKey)] = true
			t.plan.Actions = append(t.plan.Actions, IdleAction{Kind: IdleDecline, Phase: rs.Flow.Phase(cp.Phase), Subject: cp.Subject, Key: cp.TriggerKey})
			continue
		}
		var tip string
		for _, o := range obs {
			if o.MR.Repo == cp.Subject {
				tip = o.Status.HeadSHA
			}
		}
		t.plan.Actions = append(t.plan.Actions, IdleAction{Kind: IdleBegin, Phase: rs.Flow.Phase(cp.Phase), Subject: cp.Subject, Key: cp.TriggerKey, Tip: tip})
		return t.plan
	}

	t.plan.Holds = map[string][]TriggerHold{}

	// Outcomes first: an end that holds beats any watcher. Each fires once
	// (FLOWLANG §1).
	for i := range rs.Flow.Phases {
		ph := &rs.Flow.Phases[i]
		if !ph.IsOutcome() || parked && rs.ChainMerged {
			continue
		}
		held, ok := rs.outcomeHolds(ph.On.Signal, obs, now)
		if !ok {
			continue
		}
		seen := t.triggerSeen(ph.ID, held.subject, held.key)
		t.plan.Holds[ph.ID] = append(t.plan.Holds[ph.ID], TriggerHold{Subject: held.subject, Key: held.key, Why: held.why, Seen: seen, At: now})
		if !seen && t.fire(ph, held.subject, held.key, held.link, held.why, "") {
			return t.plan
		}
	}

	// Watchers, per merge request: each new occurrence of the signal on one
	// is a round of the watcher on it.
	for _, o := range obs {
		for i := range rs.Flow.Phases {
			ph := &rs.Flow.Phases[i]
			if !ph.Fires() {
				continue
			}
			s := rs.SignalOn(ph.On.Signal, ph.On.QuietFor, o, now)
			if !s.Holds {
				continue
			}
			spent := ph.On.Max != flow.UnlimitedMax && rs.PhaseRuns[PhaseRunKey(ph.ID, o.MR.Repo)] >= ph.On.Max
			seen := spent || t.triggerSeen(ph.ID, o.MR.Repo, s.Key)
			t.plan.Holds[ph.ID] = append(t.plan.Holds[ph.ID], TriggerHold{Subject: o.MR.Repo, Key: s.Key, Why: s.Why, Seen: seen, At: now})
			if !seen && !parked && t.fire(ph, o.MR.Repo, s.Key, o.MR.Ref.URL, s.Why, o.Status.HeadSHA) {
				return t.plan
			}
		}
	}

	// The one rule for a run's end (FLOWLANG §1): an idle run with no chain
	// parked and no phase armed has nothing left to do, and completes.
	// Whatever happened to its merge requests is the flow's business — a
	// wait on mr_merged routes a close through on_fail; a flow that never
	// waits completes as soon as nothing reacts. A question asked this tick
	// is about an armed phase.
	for _, a := range t.plan.Actions {
		if a.Kind == IdleAsk {
			return t.plan
		}
	}
	if !parked && !rs.anyArmed(obs) {
		t.plan.Actions = append(t.plan.Actions, IdleAction{Kind: IdleComplete})
	}
	return t.plan
}
