package confstore

import (
	"context"
	"strings"
	"time"
)

// Ticket is a first-class unit of work that owns many runs (each run is one
// attempt). It is the DB-backed successor of the transient run-creation input:
// runs now reference a ticket by id and derive their own branch from the
// ticket's BaseBranch. Flow is deliberately NOT here — it's a per-run choice
// made when a run is started.
type Ticket struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Title       string    `json:"title"`
	Goal        string    `json:"goal"`
	BaseBranch  string    `json:"base_branch"`  // "" = generated per run from the title
	ArtifactIDs []string  `json:"artifact_ids"` // goal attachments, delivered into each run's workspace
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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

func (s *Service) ListTickets(ctx context.Context) ([]Ticket, error) {
	return s.db.ListTickets(ctx)
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
	if _, err := s.db.GetProject(ctx, t.ProjectID); err != nil {
		return invalidf("ticket: project %s not found", t.ProjectID)
	}
	t.Title = strings.TrimSpace(t.Title)
	t.BaseBranch = strings.TrimSpace(t.BaseBranch)
	if t.ArtifactIDs == nil {
		t.ArtifactIDs = []string{}
	}
	return nil
}
