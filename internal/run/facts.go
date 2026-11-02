package run

import (
	"maps"
	"slices"
	"time"

	"apothecary/internal/run/event"
)

// AgentPingType: a question an agent raised through ask_human — the kind a
// well-briefed agent should not need, hence counted as unwanted. Everything
// else (a stage's gate, a phase confirmation, a selection) is the engine
// asking on purpose.
func AgentPingType(t string) bool {
	return t == event.PingClarification || t == event.PingBlocker || t == event.PingPermission
}

// Facts is the analytics accumulator Apply folds alongside the view (run_facts / stage_attempts).
//
// It lives on the View, not beside it, so it rides every path the view
// already takes: the append transaction's projection (runs.view), crash
// recovery's replay and the engine's live state — one fold, so the budget
// the engine enforces and the spend analytics report can never disagree.
// The six time buckets partition the run's wall clock by the state the run
// was in, which is why FactBearing must cover every event that moves State,
// Dormant or Hold.
type Facts struct {
	LastTS          time.Time  `json:"last_ts"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	EndReason       string     `json:"end_reason,omitempty"`   // RunAbandoned.Reason | RunFailed.Reason
	StalledAt       string     `json:"stalled_at,omitempty"`   // RunAbandoned.StalledAt
	TrackerKind     string     `json:"tracker_kind,omitempty"` // RunCreated.TicketSourceKind (View's copy is json:"-")
	QueuedMS        int64      `json:"queued_ms"`
	ProvisioningMS  int64      `json:"provisioning_ms"`
	ActiveMS        int64      `json:"active_ms"`
	WaitingHumanMS  int64      `json:"waiting_human_ms"`
	HeldMS          int64      `json:"held_ms"`
	PausedMS        int64      `json:"paused_ms"`
	CostUSD         float64    `json:"cost_usd"`
	ReworkCostUSD   float64    `json:"rework_cost_usd"`
	InputTokens     int        `json:"input_tokens"`
	OutputTokens    int        `json:"output_tokens"`
	Turns           int        `json:"turns"`
	Attempts        int        `json:"attempts"`
	FailedAttempts  int        `json:"failed_attempts"`
	Retries         int        `json:"retries"`
	TryAgains       int        `json:"try_agains"`
	Pings           int        `json:"pings"`
	PingsUnwanted   int        `json:"pings_unwanted"`
	PingsAnswered   int        `json:"pings_answered"`
	GatesRejected   int        `json:"gates_rejected"`
	HumanResponseMS int64      `json:"human_response_ms"`
	Holds           int        `json:"holds"`
	MRsCreated      int        `json:"mrs_created"`
	// Stages counts the attempts started per stage id; its length is the
	// number of distinct stages the run entered.
	Stages map[string]int `json:"stages,omitempty"`
	// Open is the attempt in progress per stage id.
	Open map[string]*AttemptFacts `json:"open,omitempty"`
	// PingClock is each open ping's type and raise time, for the response
	// time its answer closes.
	PingClock map[string]PingClock `json:"ping_clock,omitempty"`
	// Ended are the attempts closed since the projection last drained it:
	// the store writes their final rows, then clears it. Never persisted.
	Ended []AttemptFacts `json:"-"`
}

// AttemptFacts is one stage attempt, from its stage_started to whatever
// closed it.
type AttemptFacts struct {
	Seq           int64      `json:"seq"` // the StageStarted event's seq: the attempt's key
	StageID       string     `json:"stage_id"`
	Iteration     int        `json:"iteration"`
	Harness       string     `json:"harness,omitempty"`
	Model         string     `json:"model,omitempty"`
	Agent         string     `json:"agent,omitempty"`
	AgentHash     string     `json:"agent_hash,omitempty"`
	Outcome       string     `json:"outcome"` // running|paused|completed|skipped|failed|abandoned
	FailureReason string     `json:"failure_reason,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	ActiveMS      int64      `json:"active_ms"`
	WaitingMS     int64      `json:"waiting_ms"`
	CostUSD       float64    `json:"cost_usd"`
	InputTokens   int        `json:"input_tokens"`
	OutputTokens  int        `json:"output_tokens"`
	Turns         int        `json:"turns"`
	Pings         int        `json:"pings"`
}

// PingClock is an open ping's type and when it was raised.
type PingClock struct {
	Type     string    `json:"type"`
	RaisedAt time.Time `json:"raised_at"`
}

// Attempt outcomes.
const (
	OutcomeRunning   = "running"
	OutcomePaused    = "paused"
	OutcomeCompleted = "completed"
	OutcomeSkipped   = "skipped"
	OutcomeFailed    = "failed"
	OutcomeAbandoned = "abandoned"
)

// WallMS is the run's wall clock so far: the sum of the six buckets.
func (f Facts) WallMS() int64 {
	return f.QueuedMS + f.ProvisioningMS + f.ActiveMS + f.WaitingHumanMS + f.HeldMS + f.PausedMS
}

// factBearing are the event types that change Facts — directly, or through
// the State, Dormant or Hold the time buckets are classified by.
var factBearing = map[event.Type]bool{
	event.TypeRunCreated: true, event.TypeEnvRequested: true, event.TypeEnvReady: true, event.TypeEnvFailed: true,
	event.TypeStageStarted: true, event.TypeStageCompleted: true, event.TypeStageFailed: true,
	event.TypeStagePaused: true, event.TypeStageResumed: true, event.TypeEdgeTaken: true,
	event.TypePingRaised: true, event.TypePingAnswered: true, event.TypeMRCreated: true,
	event.TypePhaseStarted: true, event.TypePhaseCompleted: true, event.TypePhaseDeclined: true,
	event.TypePhaseParked: true, event.TypePhaseResumed: true,
	event.TypeRunCompleted: true, event.TypeRunAbandoned: true, event.TypeRunFailed: true,
	event.TypeRunArchived: true, event.TypeRunRetried: true, event.TypeRunPaused: true, event.TypeRunUnpaused: true,
	event.TypeHoldTaken: true, event.TypeHoldReleased: true,
}

// FactBearing reports whether an event of type t can change a run's facts:
// the store rewrites run_facts only after a batch holding one.
func FactBearing(t event.Type) bool { return factBearing[t] }

// stamp is an event's time as every store reads it back: UTC, to the
// microsecond (timestamptz). The live fold sees the events an append
// returned, a replay the rows it reads; folding stamps keeps both on the
// same millisecond arithmetic, in the same zone.
func stamp(e *event.Event) time.Time { return e.TS.UTC().Truncate(time.Microsecond) }

// accrue charges the time since the last fact-bearing event to the bucket of
// the state the run was in (the pre-event view), and to the attempt in
// flight. Other events cannot move the bucket, and skipping them keeps the
// intervals — each truncated to the millisecond — the same however many
// side events (a service's health, a transcript line) a store interleaved.
func (f *Facts) accrue(v *View, e *event.Event) {
	if e.Payload == nil || !factBearing[e.Payload.EventType()] {
		return
	}
	ts := stamp(e)
	if !f.LastTS.IsZero() {
		if dt := ts.Sub(f.LastTS).Milliseconds(); dt > 0 {
			f.charge(v, dt)
		}
	}
	f.LastTS = ts
}

func (f *Facts) charge(v *View, dt int64) {
	a := f.Open[v.CurrentStage]
	switch {
	case v.State.Terminal():
	case v.Hold != nil:
		f.HeldMS += dt
		if a != nil {
			a.WaitingMS += dt
		}
	case v.State == Paused:
		f.PausedMS += dt
	case v.State == Pending:
		f.QueuedMS += dt
	case v.State == Provisioning:
		f.ProvisioningMS += dt
	case v.State == WaitingOnHuman || v.State == InReview:
		f.WaitingHumanMS += dt
		if a != nil {
			a.WaitingMS += dt
		}
	case v.State == Running:
		f.ActiveMS += dt
		if a != nil {
			a.ActiveMS += dt
		}
	}
}

// observe folds the event's own facts, after Apply moved the view.
func (f *Facts) observe(v *View, e *event.Event) {
	ts := stamp(e)
	switch p := e.Payload.(type) {
	case *event.RunCreated:
		f.TrackerKind = p.TicketSourceKind
	case *event.StageStarted:
		if f.Open[p.StageID] != nil {
			f.close(p.StageID, OutcomeAbandoned, "", ts)
		}
		f.Attempts++
		if f.Stages == nil {
			f.Stages = map[string]int{}
		}
		f.Stages[p.StageID]++
		if f.Open == nil {
			f.Open = map[string]*AttemptFacts{}
		}
		f.Open[p.StageID] = &AttemptFacts{Seq: e.Seq, StageID: p.StageID, Iteration: p.Iteration,
			Harness: p.Harness, Model: p.Model, Agent: p.Agent, AgentHash: p.AgentHash,
			Outcome: OutcomeRunning, StartedAt: ts}
	case *event.StageCompleted:
		f.bank(p.StageID, p.TurnsUsed, p.Usage)
		out := OutcomeCompleted
		if p.Skipped {
			out = OutcomeSkipped
		}
		f.close(p.StageID, out, "", ts)
	case *event.StageFailed:
		f.FailedAttempts++
		f.bank(p.StageID, p.TurnsUsed, p.Usage)
		f.close(p.StageID, OutcomeFailed, p.Reason, ts)
	case *event.StagePaused:
		f.bank(p.StageID, p.TurnsUsed, p.Usage)
		if a := f.Open[p.StageID]; a != nil {
			a.Outcome = OutcomePaused
		}
	case *event.StageResumed:
		if a := f.Open[p.StageID]; a != nil {
			a.Outcome = OutcomeRunning
		}
	case *event.EdgeTaken:
		f.Retries++
	case *event.PingRaised:
		f.Pings++
		if AgentPingType(p.Type) {
			f.PingsUnwanted++
		}
		if f.PingClock == nil {
			f.PingClock = map[string]PingClock{}
		}
		f.PingClock[p.PingID] = PingClock{Type: p.Type, RaisedAt: ts}
		if a := f.Open[p.StageID]; a != nil {
			a.Pings++
		}
	case *event.PingAnswered:
		if c, ok := f.PingClock[p.PingID]; ok {
			f.PingsAnswered++
			f.HumanResponseMS += ts.Sub(c.RaisedAt).Milliseconds()
			if c.Type == event.PingGate && p.Answer == "reject" {
				f.GatesRejected++
			}
			delete(f.PingClock, p.PingID)
			if len(f.PingClock) == 0 {
				f.PingClock = nil // as a JSON round-trip leaves it (omitempty)
			}
		}
	case *event.HoldTaken:
		f.Holds++
	case *event.MRCreated:
		f.MRsCreated++
	case *event.RunRetried:
		f.TryAgains++
		f.EndedAt, f.EndReason, f.StalledAt = nil, "", ""
	case *event.RunCompleted:
		f.end(ts, "", "")
	case *event.RunAbandoned:
		f.end(ts, p.Reason, p.StalledAt)
	case *event.RunFailed:
		f.end(ts, p.Reason, "")
	case *event.RunArchived:
		if f.EndedAt == nil {
			f.EndedAt = &ts
		}
		f.closeAll(ts)
	}
}

// bank adds an invocation's spend to the run and to the stage's attempt in
// flight; spend on a later iteration is rework.
func (f *Facts) bank(stageID string, turns int, u *event.Usage) {
	var in, out int
	var cost float64
	if u != nil {
		in, out, cost = u.InputTokens, u.OutputTokens, u.CostUSD
	}
	f.Turns += turns
	f.InputTokens += in
	f.OutputTokens += out
	f.CostUSD += cost
	a := f.Open[stageID]
	if a == nil {
		return
	}
	a.Turns += turns
	a.InputTokens += in
	a.OutputTokens += out
	a.CostUSD += cost
	if a.Iteration > 1 {
		f.ReworkCostUSD += cost
	}
}

// close ends the stage's attempt in flight, if any, queueing its final row.
func (f *Facts) close(stageID, outcome, reason string, ts time.Time) {
	a := f.Open[stageID]
	if a == nil {
		return
	}
	a.Outcome, a.FailureReason = outcome, reason
	a.EndedAt = &ts
	f.Ended = append(f.Ended, *a)
	delete(f.Open, stageID)
	if len(f.Open) == 0 {
		f.Open = nil // as a JSON round-trip leaves it (omitempty)
	}
}

// closeAll abandons every attempt in flight, in stage order so a replay
// queues the same rows as the live fold.
func (f *Facts) closeAll(ts time.Time) {
	for _, id := range slices.Sorted(maps.Keys(f.Open)) {
		f.close(id, OutcomeAbandoned, "", ts)
	}
}

func (f *Facts) end(ts time.Time, reason, stalledAt string) {
	f.EndedAt, f.EndReason, f.StalledAt = &ts, reason, stalledAt
	f.closeAll(ts)
	f.PingClock = nil
}
