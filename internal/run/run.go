// Package run holds the run lifecycle (DESIGN.md §2) and the runs read-model
// projection (§6): a pure reduction over a run's events, rebuildable at any
// time (apothecaryd rebuild-projections) and reused by crash recovery.
package run

import (
	"time"

	"apothecary/internal/event"
)

type State string

const (
	Pending        State = "pending"
	Provisioning   State = "provisioning"
	Running        State = "running"
	WaitingOnHuman State = "waiting_on_human"
	// Paused is non-terminal but NOT auto-recovered on boot: the user
	// pressed pause; only run_unpaused (play) or an abort moves it on.
	Paused    State = "paused"
	Completed State = "completed"
	Abandoned State = "abandoned"
	Failed    State = "failed"
	Archived  State = "archived"
)

// Terminal reports whether the state can only move to Archived.
func (s State) Terminal() bool {
	return s == Completed || s == Abandoned || s == Failed || s == Archived
}

// View is the runs read-model row (§6): a projection, updated in the same
// transaction as each append in the Postgres store.
type View struct {
	RunID       string `json:"run_id"`
	ProjectID   string `json:"project_id,omitempty"` // DESIGN2 §6.3; "" on pre-v2 runs
	TicketID    string `json:"ticket_id,omitempty"`  // parent ticket; "" on pre-ticket/direct runs
	State       State  `json:"state"`
	TicketTitle string `json:"ticket_title"`
	TicketGoal  string `json:"-"` // not a read-model column; carried for prompt assembly on recovery
	// RunGoal is this attempt's extra direction (additive to TicketGoal).
	RunGoal string `json:"run_goal,omitempty"`
	// TicketArtifactIDs are the parent ticket's attachments, snapshotted at
	// run start; carried for prompt assembly + delivery (not a column).
	TicketArtifactIDs []string `json:"-"`
	FlowName          string   `json:"flow_name"`
	FlowHash          string   `json:"flow_hash"`
	CurrentStage      string   `json:"current_stage,omitempty"`
	Branch            string   `json:"branch,omitempty"`      // the run's actual git branch (from RunCreated)
	BranchBase        string   `json:"branch_base,omitempty"` // branch stem before the per-run suffix
	MRURL             string   `json:"mr_url,omitempty"`
	EnvID             string   `json:"env_id,omitempty"`
	// EnvStatus is the reconciled LIVE container status (running/starting/
	// stopped/absent) — NOT a projected column and never set by Apply. The
	// read path fills it from the engine's reconciler so the workspace badge
	// can tell a live env from one stopped/removed out-of-band; empty means
	// "not reconciled" and the UI falls back to the env_id liveness guess.
	EnvStatus string    `json:"env_status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
		v.ProjectID = p.ProjectID
		v.TicketID = p.TicketID
		v.TicketTitle = p.TicketTitle
		v.TicketGoal = p.TicketGoal
		v.RunGoal = p.RunGoal
		v.TicketArtifactIDs = p.TicketArtifactIDs
		v.FlowName = p.FlowName
		v.FlowHash = p.FlowHash
		v.Branch = p.Branch
		v.BranchBase = p.BranchBase
		v.CreatedAt = e.TS
	case *event.EnvRequested:
		v.State = Provisioning
	case *event.EnvReady:
		v.State = Running
		v.EnvID = p.EnvID
	case *event.EnvFailed:
		v.State = Failed
		if p.EnvID != "" {
			v.EnvID = p.EnvID
		}
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
	case *event.RunResumed:
		// Back to work: re-entering StageID in the same workspace. The env
		// persisted (abandoned/failed runs are never reaped).
		v.State = Running
		v.CurrentStage = p.StageID
	case *event.RunPaused:
		v.State = Paused
		if p.StageID != "" {
			v.CurrentStage = p.StageID
		}
	case *event.RunUnpaused:
		// Play: the loop restarts and re-enters where the stage events left
		// off (crash-recovery path). A re-armed gate raises no new ping, so
		// the event itself says whether the run is back to waiting.
		if p.Waiting {
			v.State = WaitingOnHuman
		} else {
			v.State = Running
		}
	case *event.EnvDestroyed:
		v.EnvID = ""
	case *event.EnvRebuildStarted, *event.EnvRebuildCompleted, *event.EnvRebuildFailed:
		// Rebuild recreates the container in place — orthogonal to lifecycle,
		// so State and EnvID are untouched (a completed run stays completed).
		// The UpdatedAt bump above IS wanted: it resets the reaper's
		// destroy-completed window so a just-rebuilt env isn't reaped at once.
	}
}
