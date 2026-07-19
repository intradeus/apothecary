// Package run holds the run lifecycle (DESIGN.md §2) and the runs read-model
// projection (§6): a pure reduction over a run's events, rebuildable at any
// time (factoryd rebuild-projections) and reused by crash recovery.
package run

import (
	"time"

	"factory/internal/event"
)

type State string

const (
	Pending        State = "pending"
	Provisioning   State = "provisioning"
	Running        State = "running"
	WaitingOnHuman State = "waiting_on_human"
	Completed      State = "completed"
	Abandoned      State = "abandoned"
	Failed         State = "failed"
	Archived       State = "archived"
)

// Terminal reports whether the state can only move to Archived.
func (s State) Terminal() bool {
	return s == Completed || s == Abandoned || s == Failed || s == Archived
}

// View is the runs read-model row (§6): a projection, updated in the same
// transaction as each append in the Postgres store.
type View struct {
	RunID        string    `json:"run_id"`
	State        State     `json:"state"`
	TicketTitle  string    `json:"ticket_title"`
	TicketGoal   string    `json:"-"` // not a read-model column; carried for prompt assembly on recovery
	FlowName     string    `json:"flow_name"`
	FlowHash     string    `json:"flow_hash"`
	CurrentStage string    `json:"current_stage,omitempty"`
	MRURL        string    `json:"mr_url,omitempty"`
	EnvID        string    `json:"env_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Reduce projects a run's ordered event slice into its read-model row.
func Reduce(events []event.Event) View {
	var v View
	for i := range events {
		Apply(&v, &events[i])
	}
	return v
}

// Apply folds one event into the view (exported so the Postgres store can
// update the projection incrementally in the append transaction).
func Apply(v *View, e *event.Event) {
	if v.RunID == "" {
		v.RunID = e.RunID
	}
	v.UpdatedAt = e.TS
	switch p := e.Payload.(type) {
	case *event.RunCreated:
		v.State = Pending
		v.TicketTitle = p.TicketTitle
		v.TicketGoal = p.TicketGoal
		v.FlowName = p.FlowName
		v.FlowHash = p.FlowHash
		v.CreatedAt = e.TS
	case *event.EnvRequested:
		v.State = Provisioning
	case *event.EnvReady:
		v.State = Running
		v.EnvID = p.EnvID
	case *event.EnvFailed:
		v.State = Failed
	case *event.StageStarted:
		v.CurrentStage = p.StageID
	case *event.PingRaised:
		v.State = WaitingOnHuman
	case *event.PingAnswered:
		v.State = Running
	case *event.MRCreated:
		v.MRURL = p.MRURL
	case *event.RunCompleted:
		v.State = Completed
	case *event.RunAbandoned:
		v.State = Abandoned
	case *event.RunFailed:
		v.State = Failed
	case *event.RunArchived:
		v.State = Archived
	case *event.EnvDestroyed:
		v.EnvID = ""
	}
}
