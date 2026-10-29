package confstore

import (
	"context"
	"strings"
	"time"

	"apothecary/internal/ports"
)

// Ticket is a first-class unit of work that owns many runs (each run is one
// attempt). It is the DB-backed successor of the transient run-creation input:
// runs now reference a ticket by id and derive their own branch from the
// ticket's BaseBranch. Flow is deliberately NOT here — it's a per-run choice
// made when a run is started.
type Ticket struct {
	ID string `json:"id"`
	// UserID is the owner: the project's, copied onto the row so the
	// per-user list needs no join. Set on every write from the project.
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	BaseBranch string `json:"base_branch"` // "" = generated per run from the title
	// ChangeType is what the ticket is (ports.ChangeType: feat, fix, …; ""
	// on a write means feat). The base branch is named after it at
	// creation; changing it later renames nothing.
	ChangeType ports.ChangeType `json:"change_type"`
	// Source* record where an imported ticket came from (empty for one typed
	// in by hand). They are provenance, not a live link: the copy is taken
	// once and edits here never travel back to the tracker, so a ticket keeps
	// working after the integration is deleted — which is why SourceKind is
	// stored next to the id rather than looked up through it.
	SourceID   string `json:"source_id,omitempty"`   // ticketsource integration
	SourceKind string `json:"source_kind,omitempty"` // "jira"
	SourceKey  string `json:"source_key,omitempty"`  // "PROJ-123"
	SourceURL  string `json:"source_url,omitempty"`
	// SourceType is the tracker's own word for the issue's type ("Bug"), as
	// it was when copied — what ChangeType was read from.
	SourceType string `json:"source_type,omitempty"`
	// AutomationID names the automation that imported the ticket, when one
	// did: provenance that lets a later poll tell its own import still
	// waiting for a run from a person's ticket.
	AutomationID string    `json:"automation_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreateTicket validates and inserts a ticket. Branch-format validation is the
// caller's job (the httpapi layer runs engine.CheckBranch, which confstore
// cannot import) — this only enforces the required fields and that the project
// exists.
func (s *Service) CreateTicket(ctx context.Context, t Ticket) (Ticket, error) {
	if err := s.validateTicket(ctx, &t); err != nil {
		return Ticket{}, err
	}
	t.ID = s.newID()
	t.CreatedAt = s.now().UTC()
	t.UpdatedAt = t.CreatedAt
	if err := s.db.UpsertTicket(ctx, t); err != nil {
		return Ticket{}, err
	}
	return t, nil
}

// UpdateTicket preserves id/created_at and re-validates.
func (s *Service) UpdateTicket(ctx context.Context, id string, upd Ticket) (Ticket, error) {
	cur, err := s.db.GetTicket(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	upd.ID = cur.ID
	upd.CreatedAt = cur.CreatedAt
	// Provenance is immutable: an edit form that doesn't know about the source
	// fields must not silently erase where the ticket came from.
	upd.SourceID, upd.SourceKind = cur.SourceID, cur.SourceKind
	upd.SourceKey, upd.SourceURL = cur.SourceKey, cur.SourceURL
	upd.SourceType = cur.SourceType
	if err := s.validateTicket(ctx, &upd); err != nil {
		return Ticket{}, err
	}
	upd.UpdatedAt = s.now().UTC()
	if err := s.db.UpsertTicket(ctx, upd); err != nil {
		return Ticket{}, err
	}
	return upd, nil
}

func (s *Service) GetTicket(ctx context.Context, id string) (Ticket, error) {
	return s.db.GetTicket(ctx, id)
}

// ListTickets is one user's tickets ("" = everyone's, for the daemon's own
// passes).
func (s *Service) ListTickets(ctx context.Context, userID string) ([]Ticket, error) {
	return s.db.ListTickets(ctx, userID)
}

// DeleteTicket removes the ticket. Runs that referenced it keep their own
// run_created snapshot (self-contained), so this never orphans history.
func (s *Service) DeleteTicket(ctx context.Context, id string) error {
	return s.db.DeleteTicket(ctx, id)
}

func (s *Service) validateTicket(ctx context.Context, t *Ticket) error {
	if strings.TrimSpace(t.Title) == "" {
		return invalidf("ticket: title is required")
	}
	if t.ProjectID == "" {
		return invalidf("ticket: project_id is required")
	}
	p, err := s.db.GetProject(ctx, t.ProjectID)
	if err != nil {
		return invalidf("ticket: project %s not found", t.ProjectID)
	}
	// A ticket belongs to whoever owns its project — whatever the caller
	// put on the row.
	t.UserID = p.UserID
	t.Title = strings.TrimSpace(t.Title)
	t.BaseBranch = strings.TrimSpace(t.BaseBranch)
	if t.ChangeType == "" {
		t.ChangeType = ports.ChangeFeat
	} else if ct, ok := ports.ParseChangeType(string(t.ChangeType)); ok {
		t.ChangeType = ct
	} else {
		return invalidf("ticket: change type %q is not one of feat, fix, hotfix, perf, docs, chore", t.ChangeType)
	}
	return nil
}
