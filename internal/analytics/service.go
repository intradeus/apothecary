package analytics

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

// smallN is the group size below which a row is flagged as noisy.
const smallN = 5

const (
	maxNameLen = 120
	maxPanels  = 30
	maxTerms   = 8
)

// Store is what the service reads and keeps. RunQuery, Drill and Values run
// SQL from Compile only, under a 5s statement timeout (ErrTimeout past it).
type Store interface {
	RunQuery(ctx context.Context, c Compiled) ([]Row, error)
	Drill(ctx context.Context, c Compiled) ([]DrillRun, error)
	Values(ctx context.Context, c Compiled) ([]string, error)
	// FlowVersionLabels names the user's flow versions "<name> v<n>", n
	// counting a flow's versions in its project from 1; a hash with no
	// version row (a built-in flow) is absent.
	FlowVersionLabels(ctx context.Context, userID string, hashes []string) (map[string]string, error)
	ProjectNames(ctx context.Context, userID string, ids []string) (map[string]string, error)
	ListDashboards(ctx context.Context, userID string) ([]Dashboard, error)
	GetDashboard(ctx context.Context, id string) (Dashboard, error)
	// PutDashboard creates or replaces a dashboard; replacing keeps its
	// owner and creation time.
	PutDashboard(ctx context.Context, d Dashboard) error
	DeleteDashboard(ctx context.Context, id string) error
	ListScorecards(ctx context.Context, userID string) ([]Scorecard, error)
	GetScorecard(ctx context.Context, id string) (Scorecard, error)
	PutScorecard(ctx context.Context, s Scorecard) error
	DeleteScorecard(ctx context.Context, id string) error
}

// Service answers a person's analytics: queries over their own runs, and
// the dashboards and scorecards they keep. The caller's id scopes every
// query; whose dashboard or scorecard an id is, is the HTTP guard's check.
type Service struct {
	Store Store
	// NewID is nil for a ULID; Clock nil for time.Now.
	NewID func() string
	Clock func() time.Time
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return ulid.Make().String()
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

// Catalog is every grain and field a query may name.
func (s *Service) Catalog() Catalog { return catalog() }

// Query answers q over userID's runs: grouped rows, labelled, scored when
// asked.
func (s *Service) Query(ctx context.Context, userID string, q Query) (Result, error) {
	var sc Scorecard
	var terms []int
	if q.Score != nil {
		var err error
		if sc, err = s.ownScorecard(ctx, userID, q.Score.Scorecard); err != nil {
			return Result{}, err
		}
		q, terms = withTerms(q, sc)
	}
	c, err := Compile(q, userID)
	if err != nil {
		return Result{}, err
	}
	rows, err := s.Store.RunQuery(ctx, c)
	if err != nil {
		return Result{}, err
	}
	res := Result{Dims: c.Fields[:c.Dims], Measures: c.Fields[c.Dims:], Rows: rows}
	if len(res.Rows) > c.Limit {
		res.Rows, res.Truncated = res.Rows[:c.Limit], true
	}
	if res.Rows == nil {
		res.Rows = []Row{}
	}
	for i := range res.Rows {
		res.Rows[i].Small = res.Rows[i].N < smallN
	}
	if res.Labels, err = s.labels(ctx, userID, res.Dims, res.Rows); err != nil {
		return Result{}, err
	}
	if q.Score != nil {
		if err := score(res.Rows, sc, terms, q.Score.Baseline); err != nil {
			return Result{}, err
		}
		if q.Order != nil && q.Order.By == "score" {
			sortByScore(res.Rows, q.Order.Desc)
		}
	}
	return res, nil
}

// labels names the project and flow-version values among the rows' dims.
func (s *Service) labels(ctx context.Context, userID string, dims []Field, rows []Row) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for i, d := range dims {
		if d.ID != "project_id" && d.ID != "flow_hash" {
			continue
		}
		vals := make([]string, 0, len(rows))
		for _, r := range rows {
			if v := r.Dims[i]; v != "" && !slices.Contains(vals, v) {
				vals = append(vals, v)
			}
		}
		m, err := s.labelValues(ctx, userID, d.ID, vals)
		if err != nil {
			return nil, err
		}
		out[d.ID] = m
	}
	return out, nil
}

func (s *Service) labelValues(ctx context.Context, userID, dimID string, vals []string) (map[string]string, error) {
	if len(vals) == 0 {
		return map[string]string{}, nil
	}
	if dimID == "project_id" {
		m, err := s.Store.ProjectNames(ctx, userID, vals)
		if m == nil {
			m = map[string]string{}
		}
		return m, err
	}
	m, err := s.Store.FlowVersionLabels(ctx, userID, vals)
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]string{}
	}
	for _, h := range vals {
		if _, ok := m[h]; !ok {
			m[h] = "built-in@" + h[:min(7, len(h))]
		}
	}
	return m, nil
}

// Values is a dimension's distinct values (at most 200) over the range,
// labelled — a filter bar's choices.
func (s *Service) Values(ctx context.Context, userID string, g Grain, fieldID string, t TimeRange) ([]Value, error) {
	c, err := compileValues(g, fieldID, t, userID)
	if err != nil {
		return nil, err
	}
	vals, err := s.Store.Values(ctx, c)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	if fieldID == "project_id" || fieldID == "flow_hash" {
		var nonEmpty []string
		for _, v := range vals {
			if v != "" {
				nonEmpty = append(nonEmpty, v)
			}
		}
		if labels, err = s.labelValues(ctx, userID, fieldID, nonEmpty); err != nil {
			return nil, err
		}
	}
	out := make([]Value, len(vals))
	for i, v := range vals {
		l, ok := labels[v]
		if !ok {
			l = v
		}
		out[i] = Value{Value: v, Label: l}
	}
	return out, nil
}

// Drill lists the runs (at most 200, newest first) behind the rows the
// filters select — any grain whose rows are one run's.
func (s *Service) Drill(ctx context.Context, userID string, g Grain, filters []Filter, t TimeRange) ([]DrillRun, error) {
	c, err := compileDrill(g, filters, t, userID)
	if err != nil {
		return nil, err
	}
	runs, err := s.Store.Drill(ctx, c)
	if runs == nil && err == nil {
		runs = []DrillRun{}
	}
	return runs, err
}

// --- dashboards ---------------------------------------------------------------

// ListDashboards is the built-ins, then the user's own.
func (s *Service) ListDashboards(ctx context.Context, userID string) ([]Dashboard, error) {
	own, err := s.Store.ListDashboards(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Dashboard, 0, len(builtinDashboards)+len(own))
	for _, d := range builtinDashboards {
		out = append(out, cloneDashboard(d))
	}
	return append(out, own...), nil
}

// GetDashboard is a built-in or a stored dashboard, whoever's.
func (s *Service) GetDashboard(ctx context.Context, id string) (Dashboard, error) {
	if isBuiltin(id) {
		if d, ok := builtinDashboard(id); ok {
			return d, nil
		}
		return Dashboard{}, fmt.Errorf("dashboard %s: %w", id, ErrNotFound)
	}
	return s.Store.GetDashboard(ctx, id)
}

func (s *Service) CreateDashboard(ctx context.Context, userID string, in DashboardInput) (Dashboard, error) {
	if err := s.checkDashboard(ctx, userID, &in); err != nil {
		return Dashboard{}, err
	}
	now := s.now()
	d := Dashboard{ID: s.newID(), UserID: userID, Name: in.Name, Filters: in.Filters, Panels: in.Panels, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.PutDashboard(ctx, d); err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

// UpdateDashboard replaces a dashboard's name, filters and panels; its
// owner never changes.
func (s *Service) UpdateDashboard(ctx context.Context, id string, in DashboardInput) (Dashboard, error) {
	if isBuiltin(id) {
		return Dashboard{}, fmt.Errorf("dashboard %s: %w", id, ErrReadOnly)
	}
	d, err := s.Store.GetDashboard(ctx, id)
	if err != nil {
		return Dashboard{}, err
	}
	if err := s.checkDashboard(ctx, d.UserID, &in); err != nil {
		return Dashboard{}, err
	}
	d.Name, d.Filters, d.Panels, d.UpdatedAt = in.Name, in.Filters, in.Panels, s.now()
	if err := s.Store.PutDashboard(ctx, d); err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

func (s *Service) DeleteDashboard(ctx context.Context, id string) error {
	if isBuiltin(id) {
		return fmt.Errorf("dashboard %s: %w", id, ErrReadOnly)
	}
	return s.Store.DeleteDashboard(ctx, id)
}

// checkDashboard validates and normalises a dashboard as saved: a name,
// at most 30 panels each of which compiles (with its scorecard's measures),
// a known viz and width; panel ids filled in; no saved time range (the
// dashboard supplies one when viewed).
func (s *Service) checkDashboard(ctx context.Context, userID string, in *DashboardInput) error {
	name, err := checkName(in.Name)
	if err != nil {
		return err
	}
	in.Name = name
	if in.Filters == nil {
		in.Filters = []string{}
	}
	for _, f := range in.Filters {
		if !anyGrainHasDim(f) {
			return invalidf("unknown filter dimension %q", f)
		}
	}
	if len(in.Panels) > maxPanels {
		return invalidf("at most %d panels, got %d", maxPanels, len(in.Panels))
	}
	if in.Panels == nil {
		in.Panels = []Panel{}
	}
	seen := map[string]bool{}
	for i := range in.Panels {
		p := &in.Panels[i]
		if p.ID == "" || seen[p.ID] {
			p.ID = s.newID()
		}
		seen[p.ID] = true
		if !slices.Contains(widths, p.Width) {
			return invalidf("panel %d: width %d is not one of 3, 4, 6, 8, 12", i+1, p.Width)
		}
		if !slices.Contains(vizzes, p.Viz) {
			return invalidf("panel %d: unknown viz %q", i+1, p.Viz)
		}
		p.Query.Time.From, p.Query.Time.To = nil, nil
		q := p.Query
		if q.Score != nil {
			sc, err := s.ownScorecard(ctx, userID, q.Score.Scorecard)
			if err != nil {
				return invalidf("panel %d: %v", i+1, err)
			}
			q, _ = withTerms(q, sc)
		}
		if _, err := Compile(q, userID); err != nil {
			return invalidf("panel %d: %v", i+1, err)
		}
	}
	return nil
}

func anyGrainHasDim(id string) bool {
	return slices.ContainsFunc(grains, func(g *grain) bool { _, ok := g.dim(id); return ok })
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > maxNameLen {
		return "", invalidf("a name is 1 to %d characters", maxNameLen)
	}
	return name, nil
}

// --- scorecards ---------------------------------------------------------------

// ListScorecards is the built-ins, then the user's own.
func (s *Service) ListScorecards(ctx context.Context, userID string) ([]Scorecard, error) {
	own, err := s.Store.ListScorecards(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Scorecard, 0, len(builtinScorecards)+len(own))
	for _, sc := range builtinScorecards {
		b, _ := builtinScorecard(sc.ID)
		out = append(out, b)
	}
	return append(out, own...), nil
}

// GetScorecard is a built-in or a stored scorecard, whoever's.
func (s *Service) GetScorecard(ctx context.Context, id string) (Scorecard, error) {
	if isBuiltin(id) {
		if sc, ok := builtinScorecard(id); ok {
			return sc, nil
		}
		return Scorecard{}, fmt.Errorf("scorecard %s: %w", id, ErrNotFound)
	}
	return s.Store.GetScorecard(ctx, id)
}

// ownScorecard is a scorecard userID may rank by: a built-in or their own.
// Another person's reads as not there.
func (s *Service) ownScorecard(ctx context.Context, userID, id string) (Scorecard, error) {
	sc, err := s.GetScorecard(ctx, id)
	if err != nil {
		return Scorecard{}, err
	}
	if !sc.Builtin && sc.UserID != userID {
		return Scorecard{}, fmt.Errorf("scorecard %s: %w", id, ErrNotFound)
	}
	return sc, nil
}

func (s *Service) CreateScorecard(ctx context.Context, userID string, in ScorecardInput) (Scorecard, error) {
	if err := checkScorecard(&in); err != nil {
		return Scorecard{}, err
	}
	now := s.now()
	sc := Scorecard{ID: s.newID(), UserID: userID, Name: in.Name, Terms: in.Terms, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.PutScorecard(ctx, sc); err != nil {
		return Scorecard{}, err
	}
	return sc, nil
}

// UpdateScorecard replaces a scorecard's name and terms; its owner never
// changes.
func (s *Service) UpdateScorecard(ctx context.Context, id string, in ScorecardInput) (Scorecard, error) {
	if isBuiltin(id) {
		return Scorecard{}, fmt.Errorf("scorecard %s: %w", id, ErrReadOnly)
	}
	sc, err := s.Store.GetScorecard(ctx, id)
	if err != nil {
		return Scorecard{}, err
	}
	if err := checkScorecard(&in); err != nil {
		return Scorecard{}, err
	}
	sc.Name, sc.Terms, sc.UpdatedAt = in.Name, in.Terms, s.now()
	if err := s.Store.PutScorecard(ctx, sc); err != nil {
		return Scorecard{}, err
	}
	return sc, nil
}

func (s *Service) DeleteScorecard(ctx context.Context, id string) error {
	if isBuiltin(id) {
		return fmt.Errorf("scorecard %s: %w", id, ErrReadOnly)
	}
	return s.Store.DeleteScorecard(ctx, id)
}

// checkScorecard validates a scorecard as saved: a name, 1–8 terms, each a
// measure some grain has (with its agg rules), a direction and a positive
// weight. Whether a grain has all of them is the query's check.
func checkScorecard(in *ScorecardInput) error {
	name, err := checkName(in.Name)
	if err != nil {
		return err
	}
	in.Name = name
	if len(in.Terms) == 0 || len(in.Terms) > maxTerms {
		return invalidf("a scorecard has 1 to %d terms, got %d", maxTerms, len(in.Terms))
	}
	for i, t := range in.Terms {
		if t.Direction != DirLower && t.Direction != DirHigher {
			return invalidf("term %d: direction %q is not lower or higher", i+1, t.Direction)
		}
		if !(t.Weight > 0) {
			return invalidf("term %d: weight must be positive", i+1)
		}
		if err := termMeasure(t.Measure); err != nil {
			return invalidf("term %d: %v", i+1, err)
		}
	}
	return nil
}

// termMeasure checks a measure some grain has: the error of the first grain
// naming the field (a wrong agg), else unknown.
func termMeasure(m MeasureRef) error {
	var named error
	for _, g := range grains {
		_, _, err := measureSQL(g, m)
		if err == nil {
			return nil
		}
		_, plain := find(g.measures, m.Field)
		_, der := find(g.derived, m.Field)
		if (plain || der) && named == nil {
			named = err
		}
	}
	if named != nil {
		return named
	}
	return invalidf("unknown measure %q", m.Field)
}
