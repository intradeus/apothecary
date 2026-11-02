// Package analyticstest is the analytics.Store contract. Postgres is the only
// implementation — the fact tables are its projections — so the contract
// seeds runs the way production does, by appending their events, and reads
// them back through the analytics.Service (and so through Compile): what it
// proves is that the SQL the compiler writes means what the catalog says,
// and above all that one person's runs never answer another's query.
package analyticstest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"apothecary/internal/analytics"
	"apothecary/internal/run/event"
)

// Fixture is a fresh, empty store: the analytics side and the event log
// whose projections it reads.
type Fixture struct {
	Store  analytics.Store
	Events event.Store
}

// RunContract exercises one implementation. open must return a fresh, empty
// store per call.
func RunContract(t *testing.T, open func(t *testing.T) Fixture) {
	t.Helper()
	t.Run("every grain answers only its owner", func(t *testing.T) { runIsolation(t, open(t)) })
	t.Run("aggregates mean what the catalog says", func(t *testing.T) { runAggregates(t, open(t)) })
	t.Run("drill and values", func(t *testing.T) { runDrill(t, open(t)) })
	t.Run("dashboards", func(t *testing.T) { runDashboards(t, open(t)) })
	t.Run("scorecards", func(t *testing.T) { runScorecards(t, open(t)) })
}

// The seeded people: alice has three runs on one ticket (an abandoned
// attempt, the one that shipped, one after it), bob one run of his own with
// values alice's never have.
const (
	alice = "u-alice"
	bob   = "u-bob"
)

type seedRun struct {
	id, user, project, ticket, title, flow, hash string
	skillset                                     event.SkillsetPin
	cost                                         float64
	ping                                         string // type; "" for none
	answer                                       string
	completed                                    bool
}

var seedRuns = []seedRun{
	{id: "an-a1", user: alice, project: "p-a", ticket: "t-a", title: "Alice one", flow: "fa", hash: "ha1",
		skillset: event.SkillsetPin{ID: "ss-a", Name: "lint", Seq: 1, Hash: "x1"}, cost: 1, ping: "gate", answer: "reject"},
	{id: "an-a2", user: alice, project: "p-a", ticket: "t-a", title: "Alice one", flow: "fa", hash: "ha1",
		skillset: event.SkillsetPin{ID: "ss-a", Name: "lint", Seq: 1, Hash: "x1"}, cost: 2, ping: "gate", answer: "approve", completed: true},
	{id: "an-a3", user: alice, project: "p-a", ticket: "t-a", title: "Alice one", flow: "fa", hash: "ha2",
		skillset: event.SkillsetPin{ID: "ss-a", Name: "lint", Seq: 2, Hash: "x2"}, cost: 4, completed: true},
	{id: "an-b1", user: bob, project: "p-b", ticket: "t-b", title: "Bob one", flow: "fb", hash: "hb1",
		skillset: event.SkillsetPin{ID: "ss-b", Name: "secret", Seq: 1, Hash: "y1"}, cost: 100, ping: "blocker", completed: true},
}

func seed(t *testing.T, f Fixture) {
	t.Helper()
	ctx := context.Background()
	for _, r := range seedRuns {
		evs := []event.Payload{
			&event.RunCreated{UserID: r.user, ProjectID: r.project, TicketID: r.ticket, TicketTitle: r.title,
				TicketChangeType: "feat", FlowName: r.flow, FlowHash: r.hash, Skillsets: []event.SkillsetPin{r.skillset}},
			&event.StageStarted{StageID: "build", Iteration: 1, Harness: "fake", Model: "m-" + r.user},
		}
		if r.ping != "" {
			evs = append(evs, &event.PingRaised{PingID: r.id + "-p", Type: r.ping, Question: "ok?", StageID: "build"})
			if r.answer != "" {
				evs = append(evs, &event.PingAnswered{PingID: r.id + "-p", Answer: r.answer})
			}
		}
		evs = append(evs, &event.StageCompleted{StageID: "build", TurnsUsed: 1, Usage: &event.Usage{CostUSD: r.cost}})
		if r.completed {
			evs = append(evs, &event.RunCompleted{})
		} else {
			evs = append(evs, &event.RunAbandoned{StalledAt: "build", Reason: "gave up"})
		}
		// One append per event, as the engine writes them: each lands in its
		// own transaction with its projection.
		for _, e := range evs {
			if _, err := f.Events.Append(ctx, r.id, e); err != nil {
				t.Fatalf("seed %s %T: %v", r.id, e, err)
			}
		}
	}
}

func service(f Fixture) *analytics.Service { return &analytics.Service{Store: f.Store} }

func query(t *testing.T, s *analytics.Service, user string, q analytics.Query) analytics.Result {
	t.Helper()
	res, err := s.Query(context.Background(), user, q)
	if err != nil {
		t.Fatalf("%s %+v: %v", user, q, err)
	}
	return res
}

// groupDim is a dimension of each grain whose values differ between alice
// and bob, and the values each may see.
var groupDims = []struct {
	grain        analytics.Grain
	dim          string
	alice, bob   []string
	nAlice, nBob int64
}{
	{analytics.GrainRuns, "flow_hash", []string{"ha1", "ha2"}, []string{"hb1"}, 3, 1},
	{analytics.GrainAttempts, "model", []string{"m-" + alice}, []string{"m-" + bob}, 3, 1},
	{analytics.GrainSkillsets, "skillset_version", []string{"lint v1", "lint v2"}, []string{"secret v1"}, 3, 1},
	{analytics.GrainPings, "type", []string{"gate"}, []string{"blocker"}, 2, 1},
	{analytics.GrainTickets, "project_id", []string{"p-a"}, []string{"p-b"}, 1, 1},
}

func runIsolation(t *testing.T, f Fixture) {
	seed(t, f)
	s := service(f)
	for _, g := range groupDims {
		for _, who := range []struct {
			user string
			want []string
			n    int64
		}{{alice, g.alice, g.nAlice}, {bob, g.bob, g.nBob}, {"u-nobody", nil, 0}} {
			res := query(t, s, who.user, analytics.Query{Grain: g.grain, GroupBy: []string{g.dim}})
			var got []string
			var n int64
			for _, r := range res.Rows {
				got = append(got, r.Dims[0])
				n += r.N
			}
			slices.Sort(got)
			if !slices.Equal(got, who.want) || n != who.n {
				t.Errorf("%s by %s for %s = %v (n %d), want %v (n %d)", g.grain, g.dim, who.user, got, n, who.want, who.n)
			}
			// Ungrouped: one row, the user's count.
			all := query(t, s, who.user, analytics.Query{Grain: g.grain})
			if len(all.Rows) != 1 || all.Rows[0].N != who.n {
				t.Errorf("%s total for %s = %+v, want n %d", g.grain, who.user, all.Rows, who.n)
			}
			// A filter naming the other person's value finds nothing.
			other := g.bob
			if who.user == bob {
				other = g.alice
			}
			res = query(t, s, who.user, analytics.Query{Grain: g.grain,
				Filters: []analytics.Filter{{Field: g.dim, Op: analytics.OpIn, Values: other}}})
			if len(res.Rows) != 1 || res.Rows[0].N != 0 {
				t.Errorf("%s filtered to the other's values for %s = %+v", g.grain, who.user, res.Rows)
			}
		}
	}
}

func value(t *testing.T, res analytics.Result, row, measure int) float64 {
	t.Helper()
	if row >= len(res.Rows) || res.Rows[row].Values[measure] == nil {
		t.Fatalf("no value at row %d measure %d: %+v", row, measure, res.Rows)
	}
	return *res.Rows[row].Values[measure]
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func runAggregates(t *testing.T, f Fixture) {
	seed(t, f)
	s := service(f)
	m := func(field string, agg analytics.Agg) analytics.MeasureRef {
		return analytics.MeasureRef{Field: field, Agg: agg}
	}
	cases := []struct {
		name  string
		q     analytics.Query
		wants []float64
	}{
		{"runs: spend, success, attempts", analytics.Query{Grain: analytics.GrainRuns, Measures: []analytics.MeasureRef{
			m("cost_usd", analytics.AggSum), m("success_rate", ""), m("attempts", analytics.AggMax), m("cost_usd", analytics.AggMedian)}},
			[]float64{7, 2.0 / 3, 1, 2}},
		{"attempts: spend, failure rate", analytics.Query{Grain: analytics.GrainAttempts, Measures: []analytics.MeasureRef{
			m("cost_usd", analytics.AggSum), m("failure_rate", "")}}, []float64{7, 0}},
		{"skillsets: spend", analytics.Query{Grain: analytics.GrainSkillsets, Measures: []analytics.MeasureRef{
			m("cost_usd", analytics.AggSum)}}, []float64{7}},
		{"pings: reject rate", analytics.Query{Grain: analytics.GrainPings, Measures: []analytics.MeasureRef{
			m("reject_rate", "")}}, []float64{0.5}},
		{"tickets: what it took to ship", analytics.Query{Grain: analytics.GrainTickets, Measures: []analytics.MeasureRef{
			m("runs", analytics.AggSum), m("runs_to_ship", analytics.AggSum), m("cost_to_ship", analytics.AggSum),
			m("total_cost_usd", analytics.AggSum), m("ship_rate", "")}}, []float64{3, 2, 3, 7, 1}},
	}
	for _, c := range cases {
		res := query(t, s, alice, c.q)
		for i, want := range c.wants {
			if got := value(t, res, 0, i); !near(got, want) {
				t.Errorf("%s: measure %d = %v, want %v", c.name, i, got, want)
			}
		}
	}
	// Grouped and bucketed: per flow version, then per day.
	res := query(t, s, alice, analytics.Query{Grain: analytics.GrainRuns, GroupBy: []string{"flow_hash"},
		Measures: []analytics.MeasureRef{m("cost_usd", analytics.AggSum)}, Order: &analytics.OrderBy{By: "flow_hash"}})
	if len(res.Rows) != 2 || res.Rows[0].Dims[0] != "ha1" || !near(value(t, res, 0, 0), 3) || !near(value(t, res, 1, 0), 4) {
		t.Errorf("by flow_hash = %+v", res.Rows)
	}
	res = query(t, s, alice, analytics.Query{Grain: analytics.GrainRuns, Time: analytics.TimeRange{Bucket: analytics.BucketDay}})
	if len(res.Rows) == 0 || res.Rows[0].Dims[0] == "" {
		t.Errorf("bucketed = %+v", res.Rows)
	}
	// The time range applies to the grain's own time field.
	future := time.Now().Add(time.Hour)
	for _, g := range groupDims {
		res = query(t, s, alice, analytics.Query{Grain: g.grain, Time: analytics.TimeRange{From: &future}})
		if res.Rows[0].N != 0 {
			t.Errorf("%s after now = %+v", g.grain, res.Rows)
		}
		res = query(t, s, alice, analytics.Query{Grain: g.grain, Time: analytics.TimeRange{To: &future}})
		if res.Rows[0].N != g.nAlice {
			t.Errorf("%s before now = %+v", g.grain, res.Rows)
		}
	}
	// No rows is no value, not zero.
	res = query(t, s, "u-nobody", analytics.Query{Grain: analytics.GrainRuns, Measures: []analytics.MeasureRef{m("cost_usd", analytics.AggSum)}})
	if len(res.Rows) != 1 || res.Rows[0].Values[0] != nil {
		t.Errorf("nobody's spend = %+v", res.Rows)
	}
	// Labels: nothing to name for a hash with no version row, and no error.
	labels, err := f.Store.FlowVersionLabels(context.Background(), alice, []string{"ha1"})
	if err != nil || len(labels) != 0 {
		t.Errorf("labels = %v, %v", labels, err)
	}
	names, err := f.Store.ProjectNames(context.Background(), alice, []string{"p-b"})
	if err != nil || len(names) != 0 {
		t.Errorf("names = %v, %v", names, err)
	}
}

func runDrill(t *testing.T, f Fixture) {
	seed(t, f)
	s := service(f)
	ctx := context.Background()
	ids := func(runs []analytics.DrillRun) []string {
		out := make([]string, len(runs))
		for i, r := range runs {
			out[i] = r.RunID
		}
		return out
	}
	runs, err := s.Drill(ctx, alice, analytics.GrainRuns,
		[]analytics.Filter{{Field: "flow_hash", Op: analytics.OpEq, Values: []string{"ha1"}}}, analytics.TimeRange{})
	if err != nil || fmt.Sprint(ids(runs)) != "[an-a2 an-a1]" {
		t.Fatalf("drill ha1 = %v, %v", ids(runs), err)
	}
	r := runs[0]
	if r.Title != "Alice one" || r.FlowName != "fa" || r.FlowHash != "ha1" || r.State != "completed" || !near(r.CostUSD, 2) || r.CreatedAt.IsZero() {
		t.Errorf("drill row = %+v", r)
	}
	for _, g := range []struct {
		grain   analytics.Grain
		filters []analytics.Filter
		want    string
	}{
		{analytics.GrainAttempts, nil, "[an-a3 an-a2 an-a1]"},
		{analytics.GrainSkillsets, []analytics.Filter{{Field: "skillset_version", Op: analytics.OpEq, Values: []string{"lint v2"}}}, "[an-a3]"},
		{analytics.GrainPings, []analytics.Filter{{Field: "answer", Op: analytics.OpEq, Values: []string{"reject"}}}, "[an-a1]"},
	} {
		runs, err := s.Drill(ctx, alice, g.grain, g.filters, analytics.TimeRange{})
		if err != nil || fmt.Sprint(ids(runs)) != g.want {
			t.Errorf("drill %s = %v, %v, want %s", g.grain, ids(runs), err, g.want)
		}
	}
	// Bob drilling into alice's values finds nothing.
	runs, err = s.Drill(ctx, bob, analytics.GrainRuns,
		[]analytics.Filter{{Field: "flow_hash", Op: analytics.OpEq, Values: []string{"ha1"}}}, analytics.TimeRange{})
	if err != nil || len(runs) != 0 {
		t.Errorf("bob's drill = %v, %v", ids(runs), err)
	}

	vals, err := s.Values(ctx, alice, analytics.GrainSkillsets, "skillset", analytics.TimeRange{})
	if err != nil || len(vals) != 1 || vals[0].Value != "lint" {
		t.Errorf("alice's skillsets = %v, %v", vals, err)
	}
	vals, err = s.Values(ctx, bob, analytics.GrainRuns, "flow_name", analytics.TimeRange{})
	if err != nil || len(vals) != 1 || vals[0].Value != "fb" {
		t.Errorf("bob's flows = %v, %v", vals, err)
	}
}

// at is a time the store keeps exactly (microseconds, UTC).
func at(m int) time.Time {
	return time.Date(2026, 10, 1, 9, m, 0, 0, time.UTC)
}

// sameDashboard / sameScorecard compare a row as read back: instants by
// value (the driver may hand them back in another zone), the rest by content.
func sameDashboard(a, b analytics.Dashboard) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.CreatedAt, a.UpdatedAt, b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

func sameScorecard(a, b analytics.Scorecard) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.CreatedAt, a.UpdatedAt, b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

func runDashboards(t *testing.T, f Fixture) {
	ctx := context.Background()
	st := f.Store
	if _, err := st.GetDashboard(ctx, "nope"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if err := st.DeleteDashboard(ctx, "nope"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
	d := analytics.Dashboard{ID: "d1", UserID: alice, Name: "Mine", Filters: []string{"flow_name"},
		Panels: []analytics.Panel{{ID: "p1", Title: "Spend", Viz: "bar", Width: 6, Query: analytics.Query{
			Grain: analytics.GrainRuns, GroupBy: []string{"flow_name"},
			Measures: []analytics.MeasureRef{{Field: "cost_usd", Agg: analytics.AggSum}},
			Time:     analytics.TimeRange{Bucket: analytics.BucketWeek}}}},
		CreatedAt: at(0), UpdatedAt: at(0)}
	if err := st.PutDashboard(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDashboard(ctx, analytics.Dashboard{ID: "d2", UserID: bob, Name: "His", CreatedAt: at(1), UpdatedAt: at(1)}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDashboard(ctx, "d1")
	if err != nil || !sameDashboard(got, d) {
		t.Errorf("round trip = %+v, %v\nwant %+v", got, err, d)
	}
	list, err := st.ListDashboards(ctx, alice)
	if err != nil || len(list) != 1 || list[0].ID != "d1" {
		t.Errorf("alice's = %+v, %v", list, err)
	}
	if list, _ := st.ListDashboards(ctx, "u-nobody"); len(list) != 0 {
		t.Errorf("nobody's = %+v", list)
	}
	// A replace keeps the owner and the creation time, whatever it is handed.
	if err := st.PutDashboard(ctx, analytics.Dashboard{ID: "d1", UserID: bob, Name: "Renamed", CreatedAt: at(5), UpdatedAt: at(6)}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetDashboard(ctx, "d1")
	if got.UserID != alice || got.Name != "Renamed" || !got.CreatedAt.Equal(at(0)) || !got.UpdatedAt.Equal(at(6)) || got.Panels == nil || len(got.Panels) != 0 {
		t.Errorf("replaced = %+v", got)
	}
	if list, _ := st.ListDashboards(ctx, bob); len(list) != 1 || list[0].ID != "d2" {
		t.Errorf("bob's = %+v", list)
	}
	// Through the service: built-ins first, then one's own.
	all, err := service(f).ListDashboards(ctx, alice)
	if err != nil || len(all) < 2 || !all[0].Builtin || all[len(all)-1].ID != "d1" {
		t.Errorf("service list = %d, %v", len(all), err)
	}
	if err := st.DeleteDashboard(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDashboard(ctx, "d1"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("deleted = %v", err)
	}
}

func runScorecards(t *testing.T, f Fixture) {
	ctx := context.Background()
	st := f.Store
	if _, err := st.GetScorecard(ctx, "nope"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if err := st.DeleteScorecard(ctx, "nope"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
	sc := analytics.Scorecard{ID: "s1", UserID: alice, Name: "Cheap", Terms: []analytics.Term{
		{Measure: analytics.MeasureRef{Field: "cost_usd", Agg: analytics.AggMedian}, Direction: analytics.DirLower, Weight: 0.7},
		{Measure: analytics.MeasureRef{Field: "success_rate"}, Direction: analytics.DirHigher, Weight: 0.3},
	}, CreatedAt: at(0), UpdatedAt: at(0)}
	if err := st.PutScorecard(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetScorecard(ctx, "s1")
	if err != nil || !sameScorecard(got, sc) {
		t.Errorf("round trip = %+v, %v", got, err)
	}
	if list, err := st.ListScorecards(ctx, alice); err != nil || len(list) != 1 {
		t.Errorf("alice's = %+v, %v", list, err)
	}
	if list, _ := st.ListScorecards(ctx, bob); len(list) != 0 {
		t.Errorf("bob's = %+v", list)
	}
	if err := st.PutScorecard(ctx, analytics.Scorecard{ID: "s1", UserID: bob, Name: "Renamed", CreatedAt: at(5), UpdatedAt: at(6)}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetScorecard(ctx, "s1")
	if got.UserID != alice || got.Name != "Renamed" || !got.CreatedAt.Equal(at(0)) || !got.UpdatedAt.Equal(at(6)) || got.Terms == nil {
		t.Errorf("replaced = %+v", got)
	}
	if err := st.DeleteScorecard(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetScorecard(ctx, "s1"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("deleted = %v", err)
	}
}
