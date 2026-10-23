package confstore

// Automations: the ways a run begins without a person pressing Start (flow
// v4). An automation belongs to a project and names a flow, one of its
// run_start phases and parameter overrides, plus how it fires:
//
//   - tracker_query polls a ticketsource integration with a query ("assigned
//     to the factory"), imports every issue it finds as a ticket of the
//     project — once — and starts a run for each;
//   - cron fires on a schedule (five-field cron, UTC) for one ticket, a new
//     run each time.
//
// The flow is untouched by any of this: an automation is a rule outside the
// run that creates one at an entrypoint, the way a person would.

import (
	"context"
	"maps"
	"strings"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/schedule"
)

const (
	AutomationTrackerQuery = "tracker_query"
	AutomationCron         = "cron"
)

// AutomationKinds lists the kinds, for messages and the API.
var AutomationKinds = []string{AutomationTrackerQuery, AutomationCron}

type Automation struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	// tracker_query: the ticketsource integration polled and the query it is
	// asked (empty = the adapter's default search).
	SourceID string `json:"source_id,omitempty"`
	Query    string `json:"query,omitempty"`
	// cron: the five-field schedule (UTC) and the ticket each firing runs.
	Schedule string `json:"schedule,omitempty"`
	TicketID string `json:"ticket_id,omitempty"`
	// What a firing creates: a run of Flow beginning at Phase ("" = the
	// flow's first entrypoint) with Params laid over the project's binding.
	Flow    string            `json:"flow"`
	Phase   string            `json:"phase,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Enabled bool              `json:"enabled"`
	// What the watcher last did with it.
	LastPolledAt *time.Time `json:"last_polled_at,omitempty"`
	LastFiredAt  *time.Time `json:"last_fired_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// AutomationMark is what a watcher pass records: a zero time leaves that
// stamp as it was; Err is always applied ("" clears the last error).
type AutomationMark struct {
	PolledAt time.Time
	FiredAt  time.Time
	Err      string
}

func (s *Service) CreateAutomation(ctx context.Context, a Automation) (Automation, error) {
	if err := s.validateAutomation(ctx, &a); err != nil {
		return Automation{}, err
	}
	a.ID = s.newID()
	a.CreatedAt = s.now().UTC()
	a.UpdatedAt = a.CreatedAt
	a.LastPolledAt, a.LastFiredAt, a.LastError = nil, nil, ""
	if err := s.db.UpsertAutomation(ctx, a); err != nil {
		return Automation{}, err
	}
	return a, nil
}

// UpdateAutomation replaces the rule and keeps the watcher's marks. A
// changed schedule counts from now: the last firing stays, so a slot that
// already passed is not fired again.
func (s *Service) UpdateAutomation(ctx context.Context, id string, upd Automation) (Automation, error) {
	cur, err := s.db.GetAutomation(ctx, id)
	if err != nil {
		return Automation{}, err
	}
	upd.ID, upd.ProjectID, upd.CreatedAt = cur.ID, cur.ProjectID, cur.CreatedAt
	upd.LastPolledAt, upd.LastFiredAt, upd.LastError = cur.LastPolledAt, cur.LastFiredAt, cur.LastError
	if err := s.validateAutomation(ctx, &upd); err != nil {
		return Automation{}, err
	}
	upd.UpdatedAt = s.now().UTC()
	if err := s.db.UpsertAutomation(ctx, upd); err != nil {
		return Automation{}, err
	}
	return upd, nil
}

func (s *Service) GetAutomation(ctx context.Context, id string) (Automation, error) {
	return s.db.GetAutomation(ctx, id)
}

// ListAutomations lists a project's automations, or every project's when
// projectID is empty.
func (s *Service) ListAutomations(ctx context.Context, projectID string) ([]Automation, error) {
	return s.db.ListAutomations(ctx, projectID)
}

// EnabledAutomations is what a watcher pass walks: every enabled automation
// of every project.
func (s *Service) EnabledAutomations(ctx context.Context) ([]Automation, error) {
	all, err := s.db.ListAutomations(ctx, "")
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, a := range all {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Service) DeleteAutomation(ctx context.Context, id string) error {
	return s.db.DeleteAutomation(ctx, id)
}

// MarkAutomation records a watcher pass without touching the rule.
func (s *Service) MarkAutomation(ctx context.Context, id string, m AutomationMark) error {
	a, err := s.db.GetAutomation(ctx, id)
	if err != nil {
		return err
	}
	if !m.PolledAt.IsZero() {
		t := m.PolledAt.UTC()
		a.LastPolledAt = &t
	}
	if !m.FiredAt.IsZero() {
		t := m.FiredAt.UTC()
		a.LastFiredAt = &t
	}
	a.LastError = m.Err
	return s.db.UpsertAutomation(ctx, a)
}

// validateAutomation: the kind and what it needs, the project, the flow and
// an entrypoint of it, and params the flow accepts — checked the way a run
// would check them, so a firing is never the first to find out.
func (s *Service) validateAutomation(ctx context.Context, a *Automation) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return invalidf("automation needs a name")
	}
	if _, err := s.db.GetProject(ctx, a.ProjectID); err != nil {
		return err
	}
	a.SourceID, a.Query = strings.TrimSpace(a.SourceID), strings.TrimSpace(a.Query)
	a.Schedule, a.TicketID = strings.TrimSpace(a.Schedule), strings.TrimSpace(a.TicketID)
	switch a.Kind {
	case AutomationTrackerQuery:
		if a.SourceID == "" {
			return invalidf("a tracker_query automation needs source_id: the ticketsource integration to poll")
		}
		in, err := s.db.GetIntegration(ctx, a.SourceID)
		if err != nil {
			return err
		}
		if in.Port != PortTicketSource {
			return invalidf("integration %s is a %s, not a ticket source", a.SourceID, in.Port)
		}
		if a.Schedule != "" || a.TicketID != "" {
			return invalidf("a tracker_query automation takes no schedule or ticket_id")
		}
	case AutomationCron:
		if _, err := schedule.Parse(a.Schedule); err != nil {
			return invalidf("%v", err)
		}
		if a.TicketID == "" {
			return invalidf("a cron automation needs ticket_id: the ticket each firing runs")
		}
		t, err := s.db.GetTicket(ctx, a.TicketID)
		if err != nil {
			return err
		}
		if t.ProjectID != a.ProjectID {
			return invalidf("ticket %s belongs to another project", a.TicketID)
		}
		if a.SourceID != "" || a.Query != "" {
			return invalidf("a cron automation takes no source_id or query")
		}
	default:
		return invalidf("kind %q is not one of %s", a.Kind, strings.Join(AutomationKinds, ", "))
	}
	a.Flow = strings.TrimSpace(a.Flow)
	if a.Flow == "" {
		return invalidf("automation needs a flow")
	}
	src, err := s.flowText(ctx, a.ProjectID, a.Flow)
	if err != nil {
		return err
	}
	f, err := flow.Parse(a.Flow, []byte(src))
	if err != nil {
		return invalidf("flow %q does not parse: %v", a.Flow, err)
	}
	a.Phase = strings.TrimSpace(a.Phase)
	if a.Phase != "" {
		ok := false
		for _, ph := range f.Entrypoints() {
			ok = ok || ph.ID == a.Phase
		}
		if !ok {
			var names []string
			for _, ph := range f.Entrypoints() {
				names = append(names, ph.ID)
			}
			return invalidf("phase %q is not an entrypoint of flow %q (%s)", a.Phase, a.Flow, strings.Join(names, ", "))
		}
	}
	values := map[string]string{}
	for k, v := range a.Params {
		if v = strings.TrimSpace(v); v != "" {
			values[strings.TrimSpace(k)] = v
		}
	}
	if _, err := flow.ParseBound(a.Flow, []byte(src), values); err != nil {
		return invalidf("%v", err)
	}
	if len(values) == 0 {
		values = nil
	}
	a.Params = maps.Clone(values)
	return nil
}
