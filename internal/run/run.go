// Package run holds the run lifecycle (DESIGN.md §2) and the runs read-model
// projection (§6): a pure reduction over a run's events, rebuildable at any
// time (apothecaryd rebuild-projections) and reused by crash recovery.
package run

import (
	"context"
	"time"

	"apothecary/internal/event"
)

type State string

const (
	Pending        State = "pending"
	Provisioning   State = "provisioning"
	Running        State = "running"
	WaitingOnHuman State = "waiting_on_human"
	// InReview: a phased run between phases, its merge requests open — the
	// reviewers have it, and reaction phases start when their triggers
	// hold (PLAN6). Non-terminal; the run ends from its merge requests.
	InReview State = "in_review"
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
	// TicketAttachmentIDs are the parent ticket's attachments, snapshotted at
	// run start; carried for prompt assembly + delivery (not a column).
	TicketAttachmentIDs []string `json:"-"`
	// TicketKey/TicketURL name the tracker issue the ticket was imported from
	// ("PROJ-123" and its page), for the run page; empty on a ticket written
	// by hand. TicketSourceID/Kind are the integration a tracker action
	// writes through — engine-side only, like TicketGoal.
	TicketKey        string `json:"ticket_key,omitempty"`
	TicketURL        string `json:"ticket_url,omitempty"`
	TicketSourceID   string `json:"-"`
	TicketSourceKind string `json:"-"`
	FlowName         string `json:"flow_name"`
	FlowHash         string `json:"flow_hash"`
	CurrentStage     string `json:"current_stage,omitempty"`
	Branch           string `json:"branch,omitempty"`        // the run's actual git branch (from RunCreated)
	BranchBase       string `json:"branch_base,omitempty"`   // branch stem before the per-run suffix
	SourceBranch     string `json:"source_branch,omitempty"` // branch this run forks off from (origin/<SourceBranch>)
	// BeginPhase is the entrypoint the run began at, Params the parameter
	// values it runs with and StartedBy who created it (all from run_created).
	BeginPhase string            `json:"begin_phase,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	StartedBy  string            `json:"started_by,omitempty"`
	MRURL      string            `json:"mr_url,omitempty"`
	// MRs are every merge request the run opened (a multi-repo run has one
	// per changed repo), in creation order;
	// MRURL is the first. Checkouts are the run's repositories. Neither is a
	// read-model column: they are reduced from events on the detail path.
	MRs       []MRView         `json:"mrs,omitempty"`
	Checkouts []event.Checkout `json:"checkouts,omitempty"`
	// Phase is the phase in progress — or the last one, while Idle — and
	// PhaseSubject the checkout it acts on ("" for the entry phase). Not
	// read-model columns.
	Phase        string `json:"phase,omitempty"`
	PhaseSubject string `json:"phase_subject,omitempty"`
	Idle         bool   `json:"idle,omitempty"`
	EnvID        string `json:"env_id,omitempty"`
	// EnvStatus is the reconciled LIVE container status (running/starting/
	// stopped/absent) — NOT a projected column and never set by Apply. The
	// read path fills it from the engine's reconciler so the workspace badge
	// can tell a live env from one stopped/removed out-of-band; empty means
	// "not reconciled" and the UI falls back to the env_id liveness guess.
	EnvStatus string `json:"env_status,omitempty"`
	// EnvDirty: the workspace's devcontainer configuration changed since its
	// container was built (a rebuild would pick it up). Like EnvStatus, live
	// state from the reconciler, never a projected column.
	EnvDirty bool `json:"env_dirty,omitempty"`
	// Dormant: the run has no loop goroutine and none is owed to it until
	// something wakes it (play, or the answer it is waiting for). NOT a
	// projected column — recovery, pause/play and the answer path all derive
	// it by folding events, and nothing outside the engine needs it.
	//
	// Orthogonal to State on purpose: a run parked on a human reads
	// waiting_on_human AND is dormant, which is what lets the engine free its
	// concurrency slot without the UI claiming someone paused it.
	Dormant bool `json:"-"`
	// Rebuilding: an env_rebuild_started with no completion or failure after
	// it. Only ever true between the two halves of one RebuildEnv call — or
	// when the process died in between, which is what recovery closes.
	Rebuilding bool      `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MRView is one merge request as the run page shows it.
type MRView struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Target string `json:"target,omitempty"`
	URL    string `json:"url"`
	IID    int    `json:"iid"`
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
		v.TicketAttachmentIDs = p.TicketAttachmentIDs
		v.TicketSourceID = p.TicketSourceID
		v.TicketSourceKind = p.TicketSourceKind
		v.TicketKey = p.TicketSourceKey
		v.TicketURL = p.TicketSourceURL
		v.FlowName = p.FlowName
		v.FlowHash = p.FlowHash
		v.Branch = p.Branch
		v.BranchBase = p.BranchBase
		v.SourceBranch = p.SourceBranch
		v.BeginPhase = p.Phase
		v.Params = p.Params
		v.StartedBy = p.By
		if len(p.Checkouts) > 0 {
			v.Checkouts = append([]event.Checkout(nil), p.Checkouts...)
		} else {
			v.Checkouts = []event.Checkout{{Name: event.PrimaryCheckoutName, Dir: event.PrimaryCheckoutDir,
				URL: p.RepoURL, Branch: p.Branch, Source: p.SourceBranch, Primary: true}}
		}
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
		// A stage that runs before any workspace exists (a remote tracker
		// action first in the flow) is still the run working: pending means
		// "queued, never started", and recovery would re-queue it as such.
		if v.State == Pending {
			v.State = Running
		}
	case *event.PingRaised:
		v.State = WaitingOnHuman
	case *event.PingAnswered:
		// An answer puts a run back to work — unless it has no loop to put
		// back: a parked run keeps waiting until something wakes it, and a
		// run a human paused keeps its pause. Claiming "running" for either
		// would be the projection inventing activity that is not happening.
		switch {
		case v.Dormant:
		case v.Idle:
			// A phase confirmation answered between phases: back to review
			// (an approval's phase_started moves it on).
			v.State = InReview
		default:
			v.State = Running
		}
	case *event.MRCreated:
		repo := p.Repo
		if repo == "" {
			repo = event.PrimaryCheckoutName
		}
		replaced := false
		for i := range v.MRs {
			if v.MRs[i].Repo == repo && v.MRs[i].Branch == p.Branch {
				v.MRs[i] = MRView{Repo: repo, Branch: p.Branch, Target: p.Target, URL: p.MRURL, IID: p.MRIID}
				replaced = true
			}
		}
		if !replaced {
			v.MRs = append(v.MRs, MRView{Repo: repo, Branch: p.Branch, Target: p.Target, URL: p.MRURL, IID: p.MRIID})
		}
		v.MRURL = v.MRs[0].URL
	case *event.PhaseStarted:
		v.Phase, v.PhaseSubject, v.Idle = p.Phase, p.Subject, false
		if v.State == InReview || v.State == WaitingOnHuman {
			v.State = Running
		}
	case *event.PhaseCompleted:
		v.Idle = true
		if len(v.MRs) > 0 {
			v.State = InReview
		}
	case *event.PhaseParked:
		// Parked at a wait point: idle for every other phase, and in review
		// when a merge request is what it waits on.
		v.Idle = true
		if len(v.MRs) > 0 && v.State == Running {
			v.State = InReview
		}
	case *event.PhaseResumed:
		v.Phase, v.PhaseSubject, v.Idle = p.Phase, p.Subject, false
		if v.State == InReview || v.State == WaitingOnHuman {
			v.State = Running
		}
	case *event.PhaseDeclined:
		if v.Idle && v.State == WaitingOnHuman {
			v.State = InReview
		}
	case *event.RunCompleted:
		v.State = Completed
	case *event.RunAbandoned:
		v.State = Abandoned
	case *event.RunFailed:
		v.State = Failed
	case *event.RunArchived:
		v.State = Archived
	case *event.RunRetried:
		// Back to work: re-entering StageID in the same workspace. The env
		// persisted (abandoned/failed runs are never reaped).
		v.State = Running
		v.CurrentStage = p.StageID
		// "Try again" re-pins the run to the flow version resume saw, so an
		// edit made to fix the failure actually takes effect (PLAN5 M19).
		if p.FlowHash != "" {
			v.FlowHash = p.FlowHash
		}
	case *event.StagePaused, *event.StageResumed:
		// An attempt parking on an open question and picking up again. State
		// is driven by the ping events that bracket it (ping_raised →
		// waiting_on_human, ping_answered → running), so touching it here
		// would only fight them: a pause recorded after its answer already
		// landed would drag a running run back to waiting.
	case *event.RunPaused:
		// Both flavours park the loop; only the user's reads as paused. The
		// engine's own parking leaves the state the ping events set
		// (waiting_on_human) — see event.RunPaused.
		v.Dormant = true
		if p.Reason != event.PauseWaiting {
			v.State = Paused
		}
		if p.StageID != "" {
			v.CurrentStage = p.StageID
		}
	case *event.RunUnpaused:
		// Play: the loop restarts and re-enters where the stage events left
		// off (crash-recovery path). A re-armed gate raises no new ping, so
		// the event itself says whether the run is back to waiting.
		v.Dormant = false
		if p.Waiting {
			v.State = WaitingOnHuman
		} else {
			v.State = Running
		}
	case *event.EnvDestroyed:
		v.EnvID = ""
	case *event.EnvRebuildStarted:
		// Rebuild recreates the container in place — orthogonal to lifecycle,
		// so State and EnvID are untouched (a completed run stays completed).
		// The UpdatedAt bump above IS wanted: it resets the reaper's
		// destroy-completed window so a just-rebuilt env isn't reaped at once.
		v.Rebuilding = true
	case *event.EnvRebuildCompleted, *event.EnvRebuildFailed:
		v.Rebuilding = false
	}
}

// ViewSource is an OPTIONAL event.Store capability: the runs read-model as
// rows, every run in one query. The engine's pollers, boot recovery and the
// list endpoint prefer it to reducing every run's log on each pass; a store
// without it (the in-memory one) is reduced instead, which is also what the
// projection is checked against.
type ViewSource interface {
	RunViews(ctx context.Context) ([]View, error)
}
