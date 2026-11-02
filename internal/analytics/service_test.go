package analytics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeStore keeps dashboards and scorecards in maps and answers queries
// with canned rows, recording what it was asked.
type fakeStore struct {
	rows      []Row
	values    []string
	drill     []DrillRun
	err       error
	last      Compiled
	flows     map[string]string
	projects  map[string]string
	dash      map[string]Dashboard
	cards     map[string]Scorecard
	labelErr  error
	labelAsks [][]string
}

func newFake() *fakeStore {
	return &fakeStore{dash: map[string]Dashboard{}, cards: map[string]Scorecard{}}
}

func (f *fakeStore) RunQuery(_ context.Context, c Compiled) ([]Row, error) {
	f.last = c
	return f.rows, f.err
}

func (f *fakeStore) Drill(_ context.Context, c Compiled) ([]DrillRun, error) {
	f.last = c
	return f.drill, f.err
}

func (f *fakeStore) Values(_ context.Context, c Compiled) ([]string, error) {
	f.last = c
	return f.values, f.err
}

func (f *fakeStore) FlowVersionLabels(_ context.Context, _ string, hashes []string) (map[string]string, error) {
	f.labelAsks = append(f.labelAsks, hashes)
	return f.flows, f.labelErr
}

func (f *fakeStore) ProjectNames(_ context.Context, _ string, ids []string) (map[string]string, error) {
	f.labelAsks = append(f.labelAsks, ids)
	return f.projects, f.labelErr
}

func (f *fakeStore) ListDashboards(_ context.Context, userID string) ([]Dashboard, error) {
	var out []Dashboard
	for _, d := range f.dash {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, f.err
}

func (f *fakeStore) GetDashboard(_ context.Context, id string) (Dashboard, error) {
	d, ok := f.dash[id]
	if !ok {
		return d, fmt.Errorf("dashboard %s: %w", id, ErrNotFound)
	}
	return d, nil
}

func (f *fakeStore) PutDashboard(_ context.Context, d Dashboard) error {
	if old, ok := f.dash[d.ID]; ok {
		d.UserID, d.CreatedAt = old.UserID, old.CreatedAt
	}
	f.dash[d.ID] = d
	return f.err
}

func (f *fakeStore) DeleteDashboard(_ context.Context, id string) error {
	if _, ok := f.dash[id]; !ok {
		return fmt.Errorf("dashboard %s: %w", id, ErrNotFound)
	}
	delete(f.dash, id)
	return nil
}

func (f *fakeStore) ListScorecards(_ context.Context, userID string) ([]Scorecard, error) {
	var out []Scorecard
	for _, s := range f.cards {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, f.err
}

func (f *fakeStore) GetScorecard(_ context.Context, id string) (Scorecard, error) {
	s, ok := f.cards[id]
	if !ok {
		return s, fmt.Errorf("scorecard %s: %w", id, ErrNotFound)
	}
	return s, nil
}

func (f *fakeStore) PutScorecard(_ context.Context, s Scorecard) error {
	if old, ok := f.cards[s.ID]; ok {
		s.UserID, s.CreatedAt = old.UserID, old.CreatedAt
	}
	f.cards[s.ID] = s
	return f.err
}

func (f *fakeStore) DeleteScorecard(_ context.Context, id string) error {
	if _, ok := f.cards[id]; !ok {
		return fmt.Errorf("scorecard %s: %w", id, ErrNotFound)
	}
	delete(f.cards, id)
	return nil
}

var testNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newService(f *fakeStore) *Service {
	n := 0
	return &Service{Store: f, Clock: func() time.Time { return testNow },
		NewID: func() string { n++; return fmt.Sprintf("id%d", n) }}
}

func TestQueryTruncatesFlagsAndLabels(t *testing.T) {
	f := newFake()
	f.flows = map[string]string{"h1": "builder v2"}
	f.projects = map[string]string{"p1": "Shop"}
	for i := range 3 {
		f.rows = append(f.rows, Row{Dims: []string{"p1", []string{"h1", "abcdef0123", ""}[i]}, Values: []*float64{new(float64(i))}, N: int64(4 + i)})
	}
	s := newService(f)
	res, err := s.Query(context.Background(), "u1", Query{Grain: GrainRuns, GroupBy: []string{"project_id", "flow_hash"},
		Measures: []MeasureRef{{Field: "cost_usd", Agg: AggSum}}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if f.last.Args[0] != "u1" || !strings.HasSuffix(f.last.SQL, "LIMIT 3") {
		t.Errorf("compiled %v %s", f.last.Args, f.last.SQL)
	}
	if !res.Truncated || len(res.Rows) != 2 || !res.Rows[0].Small || res.Rows[1].Small {
		t.Errorf("rows %+v truncated %v", res.Rows, res.Truncated)
	}
	if len(res.Dims) != 2 || len(res.Measures) != 1 || res.Measures[0].Type != TypeUSD {
		t.Errorf("fields %+v %+v", res.Dims, res.Measures)
	}
	if res.Labels["project_id"]["p1"] != "Shop" || res.Labels["flow_hash"]["h1"] != "builder v2" ||
		res.Labels["flow_hash"]["abcdef0123"] != "built-in@abcdef0" {
		t.Errorf("labels %v", res.Labels)
	}

	// No rows is an empty list, not null; no label lookups for no values.
	f.rows, f.labelAsks = nil, nil
	res, err = s.Query(context.Background(), "u1", Query{Grain: GrainRuns, GroupBy: []string{"flow_hash"}})
	if err != nil || res.Rows == nil || len(res.Rows) != 0 || res.Truncated || len(f.labelAsks) != 0 {
		t.Errorf("empty = %+v, %v, asks %v", res, err, f.labelAsks)
	}

	f.labelErr = errors.New("boom")
	f.rows = []Row{{Dims: []string{"h"}, N: 9}}
	if _, err := s.Query(context.Background(), "u1", Query{Grain: GrainRuns, GroupBy: []string{"flow_hash"}}); err == nil {
		t.Error("label error swallowed")
	}
	f.labelErr, f.err = nil, ErrTimeout
	if _, err := s.Query(context.Background(), "u1", Query{Grain: GrainRuns}); !errors.Is(err, ErrTimeout) {
		t.Errorf("store error = %v", err)
	}
	if _, err := s.Query(context.Background(), "u1", Query{Grain: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad query = %v", err)
	}
}

func TestQueryScoresByAScorecardTheCallerMayUse(t *testing.T) {
	f := newFake()
	f.cards["mine"] = Scorecard{ID: "mine", UserID: "u1", Terms: []Term{{Measure: MeasureRef{Field: "cost_usd", Agg: AggSum}, Direction: DirLower, Weight: 1}}}
	f.cards["theirs"] = Scorecard{ID: "theirs", UserID: "u2", Terms: f.cards["mine"].Terms}
	f.rows = []Row{
		{Dims: []string{"a"}, Values: []*float64{new(4.0)}, N: 10},
		{Dims: []string{"b"}, Values: []*float64{new(2.0)}, N: 10},
	}
	s := newService(f)
	q := Query{Grain: GrainRuns, GroupBy: []string{"flow_name"}, Score: &ScoreSpec{Scorecard: "mine"}, Order: &OrderBy{By: "score", Desc: true}}
	res, err := s.Query(context.Background(), "u1", q)
	if err != nil {
		t.Fatal(err)
	}
	if f.last.Measures != 1 || res.Rows[0].Dims[0] != "b" || !near(res.Rows[0].Score, 100) || !near(res.Rows[1].Score, 0) {
		t.Errorf("scored %+v", res.Rows)
	}
	q.Score = &ScoreSpec{Scorecard: "builtin-cheapest", Baseline: []string{"zz"}}
	if _, err := s.Query(context.Background(), "u1", q); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing baseline = %v", err)
	}
	for _, id := range []string{"theirs", "nope", "builtin-nope"} {
		q.Score = &ScoreSpec{Scorecard: id}
		if _, err := s.Query(context.Background(), "u1", q); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
	}
}

func TestValuesAndDrill(t *testing.T) {
	f := newFake()
	f.values = []string{"", "h1", "h2"}
	f.flows = map[string]string{"h1": "f v1"}
	s := newService(f)
	vals, err := s.Values(context.Background(), "u1", GrainRuns, "flow_hash", TimeRange{Bucket: BucketWeek})
	if err != nil {
		t.Fatal(err)
	}
	want := []Value{{"", ""}, {"h1", "f v1"}, {"h2", "built-in@h2"}}
	if fmt.Sprint(vals) != fmt.Sprint(want) {
		t.Errorf("values %v", vals)
	}
	if strings.Contains(f.last.SQL, "date_trunc") || f.last.Args[0] != "u1" {
		t.Errorf("values SQL %s", f.last.SQL)
	}
	f.values = []string{"completed"}
	if vals, err := s.Values(context.Background(), "u1", GrainRuns, "state", TimeRange{}); err != nil || vals[0] != (Value{"completed", "completed"}) {
		t.Errorf("state values %v %v", vals, err)
	}
	if _, err := s.Values(context.Background(), "u1", GrainRuns, "nope", TimeRange{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad field = %v", err)
	}
	f.err = errors.New("down")
	if _, err := s.Values(context.Background(), "u1", GrainRuns, "state", TimeRange{}); err == nil {
		t.Error("store error swallowed")
	}
	f.err = nil

	runs, err := s.Drill(context.Background(), "u1", GrainPings, []Filter{{Field: "type", Op: OpEq, Values: []string{"gate"}}}, TimeRange{})
	if err != nil || runs == nil || len(runs) != 0 || !strings.Contains(f.last.SQL, "p.run_id") {
		t.Errorf("drill %v %v %s", runs, err, f.last.SQL)
	}
	if _, err := s.Drill(context.Background(), "u1", GrainTickets, nil, TimeRange{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("tickets drill = %v", err)
	}
}

func TestDashboardsCRUD(t *testing.T) {
	f := newFake()
	s := newService(f)
	ctx := context.Background()
	from := testNow.AddDate(0, -1, 0)
	panel := Panel{Title: "Spend", Viz: "bar", Width: 6, Query: Query{Grain: GrainRuns, GroupBy: []string{"flow_name"},
		Measures: []MeasureRef{{Field: "cost_usd", Agg: AggSum}}, Time: TimeRange{From: &from, To: &from, Bucket: BucketWeek}}}
	d, err := s.CreateDashboard(ctx, "u1", DashboardInput{Name: "  Mine  ", Panels: []Panel{panel, panel}})
	if err != nil {
		t.Fatal(err)
	}
	// Panel ids are drawn first, then the dashboard's.
	if d.ID != "id3" || d.UserID != "u1" || d.Name != "Mine" || d.Filters == nil || !d.CreatedAt.Equal(testNow) {
		t.Errorf("created %+v", d)
	}
	p := d.Panels[0]
	if p.ID == "" || p.ID == d.Panels[1].ID || p.Query.Time.From != nil || p.Query.Time.To != nil || p.Query.Time.Bucket != BucketWeek {
		t.Errorf("panels %+v", d.Panels)
	}

	list, err := s.ListDashboards(ctx, "u1")
	if err != nil || len(list) != len(builtinDashboards)+1 || !list[0].Builtin || list[0].ID != "builtin-overview" || list[len(list)-1].ID != d.ID {
		t.Errorf("list %d %v", len(list), err)
	}
	// A built-in handed out is a copy.
	list[0].Panels[0].Title = "changed"
	if b, _ := s.GetDashboard(ctx, "builtin-overview"); b.Panels[0].Title == "changed" {
		t.Error("built-in shared")
	}
	if other, _ := s.ListDashboards(ctx, "u2"); len(other) != len(builtinDashboards) {
		t.Errorf("u2 sees %d", len(other))
	}

	up, err := s.UpdateDashboard(ctx, d.ID, DashboardInput{Name: "Renamed", Filters: []string{"flow_name"}})
	if err != nil || up.UserID != "u1" || up.Name != "Renamed" || len(up.Panels) != 0 || up.Filters[0] != "flow_name" {
		t.Errorf("updated %+v %v", up, err)
	}
	if got, _ := s.GetDashboard(ctx, d.ID); got.Name != "Renamed" {
		t.Errorf("stored %+v", got)
	}

	for _, id := range []string{"builtin-overview", "builtin-nope"} {
		if _, err := s.UpdateDashboard(ctx, id, DashboardInput{Name: "x"}); !errors.Is(err, ErrReadOnly) {
			t.Errorf("update %s = %v", id, err)
		}
		if err := s.DeleteDashboard(ctx, id); !errors.Is(err, ErrReadOnly) {
			t.Errorf("delete %s = %v", id, err)
		}
	}
	if _, err := s.GetDashboard(ctx, "builtin-nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown built-in = %v", err)
	}
	if _, err := s.UpdateDashboard(ctx, "nope", DashboardInput{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v", err)
	}
	if err := s.DeleteDashboard(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDashboard(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted = %v", err)
	}
}

func TestDashboardValidation(t *testing.T) {
	f := newFake()
	f.cards["theirs"] = Scorecard{ID: "theirs", UserID: "u2", Terms: []Term{{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 1}}}
	s := newService(f)
	ok := Panel{Viz: "table", Width: 12, Query: Query{Grain: GrainRuns}}
	with := func(mod func(*Panel)) []Panel { p := ok; mod(&p); return []Panel{p} }
	many := make([]Panel, maxPanels+1)
	for i := range many {
		many[i] = ok
	}
	cases := []struct {
		name, token string
		in          DashboardInput
	}{
		{"empty name", "name", DashboardInput{Name: "  "}},
		{"long name", "name", DashboardInput{Name: strings.Repeat("é", maxNameLen+1)}},
		{"filter", "user_id", DashboardInput{Name: "d", Filters: []string{"user_id"}}},
		{"panels", "30", DashboardInput{Name: "d", Panels: many}},
		{"width", "width 5", DashboardInput{Name: "d", Panels: with(func(p *Panel) { p.Width = 5 })}},
		{"viz", "pie", DashboardInput{Name: "d", Panels: with(func(p *Panel) { p.Viz = "pie" })}},
		{"query", "nope", DashboardInput{Name: "d", Panels: with(func(p *Panel) { p.Query.Grain = "nope" })}},
		{"scorecard", "theirs", DashboardInput{Name: "d", Panels: with(func(p *Panel) { p.Query.Score = &ScoreSpec{Scorecard: "theirs"} })}},
		{"score terms", "success_rate", DashboardInput{Name: "d", Panels: with(func(p *Panel) {
			p.Query.Grain, p.Query.Score = GrainAttempts, &ScoreSpec{Scorecard: "builtin-cheapest"}
		})}},
	}
	for _, c := range cases {
		_, err := s.CreateDashboard(context.Background(), "u1", c.in)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.token) {
			t.Errorf("%s: %v, want ErrInvalid naming %q", c.name, err, c.token)
		}
	}
	if len(f.dash) != 0 {
		t.Errorf("an invalid dashboard was stored: %v", f.dash)
	}
	if _, err := s.CreateDashboard(context.Background(), "u1", DashboardInput{Name: strings.Repeat("é", maxNameLen),
		Panels: with(func(p *Panel) { p.Query.Score = &ScoreSpec{Scorecard: "builtin-balanced"} })}); err != nil {
		t.Errorf("valid dashboard: %v", err)
	}
	f.err = errors.New("down")
	if _, err := s.CreateDashboard(context.Background(), "u1", DashboardInput{Name: "d"}); err == nil {
		t.Error("store error swallowed")
	}
	if _, err := s.ListDashboards(context.Background(), "u1"); err == nil {
		t.Error("list error swallowed")
	}
}

func TestScorecardsCRUD(t *testing.T) {
	f := newFake()
	s := newService(f)
	ctx := context.Background()
	terms := []Term{{Measure: MeasureRef{Field: "response_ms", Agg: AggMedian}, Direction: DirLower, Weight: 2}}
	sc, err := s.CreateScorecard(ctx, "u1", ScorecardInput{Name: "Quick answers", Terms: terms})
	if err != nil || sc.UserID != "u1" || sc.ID != "id1" || sc.Builtin {
		t.Fatalf("created %+v %v", sc, err)
	}
	list, err := s.ListScorecards(ctx, "u1")
	if err != nil || len(list) != len(builtinScorecards)+1 || list[0].ID != "builtin-cheapest" || !list[0].Builtin {
		t.Errorf("list %+v %v", list, err)
	}
	list[0].Terms[0].Weight = 99
	if b, _ := s.GetScorecard(ctx, "builtin-cheapest"); b.Terms[0].Weight == 99 {
		t.Error("built-in shared")
	}
	up, err := s.UpdateScorecard(ctx, sc.ID, ScorecardInput{Name: "Renamed", Terms: terms})
	if err != nil || up.UserID != "u1" || up.Name != "Renamed" {
		t.Errorf("updated %+v %v", up, err)
	}
	if _, err := s.UpdateScorecard(ctx, sc.ID, ScorecardInput{Name: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no terms = %v", err)
	}
	if _, err := s.UpdateScorecard(ctx, "nope", ScorecardInput{Name: "x", Terms: terms}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v", err)
	}
	if _, err := s.UpdateScorecard(ctx, "builtin-fastest", ScorecardInput{Name: "x", Terms: terms}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update built-in = %v", err)
	}
	if err := s.DeleteScorecard(ctx, "builtin-fastest"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete built-in = %v", err)
	}
	if err := s.DeleteScorecard(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetScorecard(ctx, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted = %v", err)
	}

	bad := []struct {
		token string
		terms []Term
	}{
		{"terms", nil},
		{"direction", []Term{{Measure: terms[0].Measure, Direction: "up", Weight: 1}}},
		{"weight", []Term{{Measure: terms[0].Measure, Direction: DirLower}}},
		{"unknown measure", []Term{{Measure: MeasureRef{Field: "nope"}, Direction: DirLower, Weight: 1}}},
		{"needs an agg", []Term{{Measure: MeasureRef{Field: "cost_usd"}, Direction: DirLower, Weight: 1}}},
		{"derived", []Term{{Measure: MeasureRef{Field: "success_rate", Agg: AggSum}, Direction: DirLower, Weight: 1}}},
	}
	for _, b := range bad {
		if _, err := s.CreateScorecard(ctx, "u1", ScorecardInput{Name: "x", Terms: b.terms}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), b.token) {
			t.Errorf("%s: %v", b.token, err)
		}
	}
	if _, err := s.CreateScorecard(ctx, "u1", ScorecardInput{Name: "", Terms: terms}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no name = %v", err)
	}
	f.err = errors.New("down")
	if _, err := s.CreateScorecard(ctx, "u1", ScorecardInput{Name: "x", Terms: terms}); err == nil {
		t.Error("store error swallowed")
	}
	if _, err := s.ListScorecards(ctx, "u1"); err == nil {
		t.Error("list error swallowed")
	}
}

func TestServiceDefaults(t *testing.T) {
	s := &Service{}
	if a, b := s.newID(), s.newID(); a == "" || a == b {
		t.Errorf("ids %q %q", a, b)
	}
	if s.now().Location() != time.UTC {
		t.Error("clock not UTC")
	}
	if len(s.Catalog().Grains) != len(grains) {
		t.Error("catalog")
	}
}

// A label store with nothing to say leaves the raw values (a flow version
// with no row reads as built-in); one that fails fails the answer.
func TestLabelsWhenTheStoreHasNone(t *testing.T) {
	f := newFake()
	f.rows = []Row{{Dims: []string{"p1", "abcdef0123"}, N: 9}}
	s := newService(f)
	res, err := s.Query(context.Background(), "u1", Query{Grain: GrainRuns, GroupBy: []string{"project_id", "flow_hash"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Labels["project_id"]) != 0 || res.Labels["flow_hash"]["abcdef0123"] != "built-in@abcdef0" {
		t.Errorf("labels = %v", res.Labels)
	}
	f.values = []string{"p1"}
	vals, err := s.Values(context.Background(), "u1", GrainRuns, "project_id", TimeRange{})
	if err != nil || len(vals) != 1 || vals[0] != (Value{"p1", "p1"}) {
		t.Errorf("unlabelled values = %v, %v", vals, err)
	}
	f.labelErr = errors.New("down")
	if _, err := s.Values(context.Background(), "u1", GrainRuns, "project_id", TimeRange{}); err == nil {
		t.Error("label error swallowed by values")
	}
}

// An update the store fails to keep is an error, not the updated row.
func TestUpdatesReportStoreFailures(t *testing.T) {
	f := newFake()
	s := newService(f)
	ctx := context.Background()
	d, err := s.CreateDashboard(ctx, "u1", DashboardInput{Name: "d"})
	if err != nil {
		t.Fatal(err)
	}
	terms := []Term{{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 1}}
	sc, err := s.CreateScorecard(ctx, "u1", ScorecardInput{Name: "s", Terms: terms})
	if err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("down")
	if _, err := s.UpdateDashboard(ctx, d.ID, DashboardInput{Name: "renamed"}); err == nil {
		t.Error("dashboard update error swallowed")
	}
	if _, err := s.UpdateScorecard(ctx, sc.ID, ScorecardInput{Name: "renamed", Terms: terms}); err == nil {
		t.Error("scorecard update error swallowed")
	}
}

// An invalid update is refused before the store is touched; unscored rows
// sort last whichever way the scores run.
func TestInvalidUpdateAndUnscoredRows(t *testing.T) {
	f := newFake()
	s := newService(f)
	d, err := s.CreateDashboard(context.Background(), "u1", DashboardInput{Name: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateDashboard(context.Background(), d.ID, DashboardInput{Name: ""}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid update = %v", err)
	}
	if f.dash[d.ID].Name != "d" {
		t.Errorf("stored = %+v", f.dash[d.ID])
	}
	rows := []Row{{Dims: []string{"a"}}, {Dims: []string{"b"}, Score: new(10.0)}, {Dims: []string{"c"}}}
	for _, desc := range []bool{true, false} {
		sortByScore(rows, desc)
		if rows[0].Dims[0] != "b" || rows[1].Score != nil || rows[2].Score != nil {
			t.Errorf("desc %v: %v %v %v", desc, rows[0].Dims, rows[1].Dims, rows[2].Dims)
		}
	}
}
