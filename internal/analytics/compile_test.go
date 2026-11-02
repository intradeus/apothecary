package analytics

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// Every grain binds the caller as $1 in its source: the tenancy rule lives
// in the compiler, so it is checked on every grain and every shape.
func TestEveryGrainIsScopedToTheUser(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, g := range grains {
		q := Query{Grain: g.id, GroupBy: []string{g.dims[0].ID},
			Filters: []Filter{{Field: g.dims[0].ID, Op: OpIn, Values: []string{"x"}}},
			Time:    TimeRange{From: &from, Bucket: BucketWeek}}
		for _, m := range g.measures {
			q.Measures = append(q.Measures, MeasureRef{Field: m.ID, Agg: AggMedian})
			break
		}
		for _, d := range g.derived {
			q.Measures = append(q.Measures, MeasureRef{Field: d.ID})
			break
		}
		c, err := Compile(q, "u1")
		if err != nil {
			t.Fatalf("%s: %v", g.id, err)
		}
		if !strings.Contains(c.SQL, "user_id = $1") {
			t.Errorf("%s: SQL not scoped: %s", g.id, c.SQL)
		}
		if c.Args[0] != "u1" {
			t.Errorf("%s: $1 = %v", g.id, c.Args[0])
		}
		if c.Dims != 2 || c.Measures != len(q.Measures) || len(c.Fields) != c.Dims+c.Measures {
			t.Errorf("%s: shape %d/%d/%d", g.id, c.Dims, c.Measures, len(c.Fields))
		}
		v, err := compileValues(g.id, g.dims[0].ID, TimeRange{From: &from}, "u1")
		if err != nil || !strings.Contains(v.SQL, "user_id = $1") || v.Args[0] != "u1" {
			t.Errorf("%s values: %v %s", g.id, err, v.SQL)
		}
		if g.runID == "" {
			continue
		}
		d, err := compileDrill(g.id, q.Filters, TimeRange{}, "u1")
		if err != nil || !strings.Contains(d.SQL, "f.user_id = $1") || !strings.Contains(d.SQL, g.where) {
			t.Errorf("%s drill: %v %s", g.id, err, d.SQL)
		}
	}
	if _, err := compileDrill(GrainTickets, nil, TimeRange{}, "u1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("tickets drill = %v", err)
	}
}

func TestCompileRefusesWhatTheCatalogDoesNot(t *testing.T) {
	m := func(f string, a Agg) []MeasureRef { return []MeasureRef{{Field: f, Agg: a}} }
	many := func(n int) []MeasureRef {
		out := make([]MeasureRef, n)
		for i := range out {
			out[i] = MeasureRef{Field: "cost_usd", Agg: AggSum}
		}
		return out
	}
	filters := make([]Filter, 11)
	for i := range filters {
		filters[i] = Filter{Field: "state", Op: OpEq, Values: []string{"completed"}}
	}
	cases := []struct {
		name, token string
		q           Query
	}{
		{"grain", "nope", Query{Grain: "nope"}},
		{"measure", "secret", Query{Grain: GrainRuns, Measures: m("secret", AggSum)}},
		{"agg", "mode", Query{Grain: GrainRuns, Measures: m("cost_usd", "mode")}},
		{"no agg", "cost_usd", Query{Grain: GrainRuns, Measures: m("cost_usd", "")}},
		{"agg on derived", "success_rate", Query{Grain: GrainRuns, Measures: m("success_rate", AggSum)}},
		{"measure of another grain", "response_ms", Query{Grain: GrainRuns, Measures: m("response_ms", AggSum)}},
		{"dimension", "user_id", Query{Grain: GrainRuns, GroupBy: []string{"user_id"}}},
		{"grouped twice", "state", Query{Grain: GrainRuns, GroupBy: []string{"state", "state"}}},
		{"filter field", "user_id", Query{Grain: GrainRuns, Filters: []Filter{{Field: "user_id", Op: OpEq, Values: []string{"u2"}}}}},
		{"filter op", "like", Query{Grain: GrainRuns, Filters: []Filter{{Field: "state", Op: "like", Values: []string{"%"}}}}},
		{"eq arity", "state", Query{Grain: GrainRuns, Filters: []Filter{{Field: "state", Op: OpEq, Values: []string{"a", "b"}}}}},
		{"in arity", "state", Query{Grain: GrainRuns, Filters: []Filter{{Field: "state", Op: OpIn}}}},
		{"bucket", "hour", Query{Grain: GrainRuns, Time: TimeRange{Bucket: "hour"}}},
		{"measures", "8", Query{Grain: GrainRuns, Measures: many(9)}},
		{"group-by", "3", Query{Grain: GrainRuns, GroupBy: []string{"state", "flow_name", "flow_hash", "project_id"}}},
		{"filters", "10", Query{Grain: GrainRuns, Filters: filters}},
		{"order", "zz", Query{Grain: GrainRuns, Order: &OrderBy{By: "zz"}}},
		{"order measure", "m1", Query{Grain: GrainRuns, Measures: m("cost_usd", AggSum), Order: &OrderBy{By: "m1"}}},
		{"order score", "score", Query{Grain: GrainRuns, Order: &OrderBy{By: "score"}}},
		{"limit", "-1", Query{Grain: GrainRuns, Limit: -1}},
	}
	for _, c := range cases {
		_, err := Compile(c.q, "u1")
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.token) {
			t.Errorf("%s: %v, want ErrInvalid naming %q", c.name, err, c.token)
		}
	}
	if _, err := compileValues("nope", "state", TimeRange{}, "u"); !errors.Is(err, ErrInvalid) {
		t.Errorf("values grain = %v", err)
	}
	if _, err := compileValues(GrainRuns, "nope", TimeRange{}, "u"); !errors.Is(err, ErrInvalid) {
		t.Errorf("values field = %v", err)
	}
	if _, err := compileDrill("nope", nil, TimeRange{}, "u"); !errors.Is(err, ErrInvalid) {
		t.Errorf("drill grain = %v", err)
	}
	if _, err := compileDrill(GrainRuns, filters, TimeRange{}, "u"); !errors.Is(err, ErrInvalid) {
		t.Errorf("drill filters = %v", err)
	}
}

func TestCompileSQLShape(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	c, err := Compile(Query{
		Grain:    GrainRuns,
		Measures: []MeasureRef{{Field: "cost_usd", Agg: AggP90}, {Field: "wall_ms", Agg: AggAvg}, {Field: "success_rate"}},
		GroupBy:  []string{"flow_name"},
		Filters: []Filter{
			{Field: "state", Op: OpEq, Values: []string{"completed"}},
			{Field: "project_id", Op: OpNeq, Values: []string{"p0"}},
			{Field: "flow_hash", Op: OpIn, Values: []string{"h1", "h2"}},
			{Field: "end_reason", Op: OpNotIn, Values: []string{"x"}},
		},
		Time:  TimeRange{From: &from, To: &to, Bucket: BucketMonth},
		Limit: 10,
	}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SELECT date_trunc('month', f.created_at)::text, (f.flow_name)::text, " +
			"(percentile_cont(0.9) WITHIN GROUP (ORDER BY f.cost_usd))::float8, (avg(f.wall_ms))::float8, (avg(f.success::int))::float8, count(*) FROM ",
		"WHERE f.user_id = $1 AND (f.state)::text = $2 AND (f.project_id)::text <> $3 AND (f.flow_hash)::text = ANY($4::text[]) AND (f.end_reason)::text <> ALL($5::text[]) AND f.created_at >= $6 AND f.created_at < $7",
		" GROUP BY 1, 2 ORDER BY 1 LIMIT 11",
	} {
		if !strings.Contains(c.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, c.SQL)
		}
	}
	if len(c.Args) != 7 || c.Limit != 10 || c.Fields[0].ID != "bucket" || c.Fields[2].Label != "Cost (p90)" {
		t.Errorf("args %v limit %d fields %+v", c.Args, c.Limit, c.Fields)
	}

	order := func(q Query) string {
		t.Helper()
		c, err := Compile(q, "u")
		if err != nil {
			t.Fatal(err)
		}
		_, after, _ := strings.Cut(c.SQL, " ORDER BY ")
		before, _, _ := strings.Cut(after, " LIMIT")
		if !strings.Contains(c.SQL, " ORDER BY ") {
			return ""
		}
		return before
	}
	two := []MeasureRef{{Field: "cost_usd", Agg: AggSum}, {Field: "turns", Agg: AggMax}}
	for _, c := range []struct {
		q    Query
		want string
	}{
		{Query{Grain: GrainRuns}, ""},
		{Query{Grain: GrainRuns, GroupBy: []string{"state"}, Measures: two}, "4 DESC"},
		{Query{Grain: GrainRuns, GroupBy: []string{"state"}, Measures: two, Order: &OrderBy{By: "n"}}, "4 ASC NULLS LAST"},
		{Query{Grain: GrainRuns, GroupBy: []string{"state"}, Measures: two, Order: &OrderBy{By: "m1", Desc: true}}, "3 DESC NULLS LAST"},
		{Query{Grain: GrainRuns, GroupBy: []string{"state"}, Measures: two, Order: &OrderBy{By: "state"}}, "1 ASC NULLS LAST"},
		{Query{Grain: GrainRuns, GroupBy: []string{"state"}, Time: TimeRange{Bucket: BucketDay}, Order: &OrderBy{By: "bucket", Desc: true}}, "1 DESC NULLS LAST"},
		{Query{Grain: GrainRuns, GroupBy: []string{"state"}, Score: &ScoreSpec{}, Order: &OrderBy{By: "score"}}, "2 DESC"},
	} {
		if got := order(c.q); got != c.want {
			t.Errorf("%+v: order %q, want %q", c.q, got, c.want)
		}
	}
	if c, _ := Compile(Query{Grain: GrainRuns, Limit: 9999}, "u"); !strings.HasSuffix(c.SQL, "LIMIT 501") || c.Limit != 500 {
		t.Errorf("limit cap: %s", c.SQL)
	}
}

// Every built-in dashboard's panels compile, with their scorecards' terms.
func TestBuiltinsCompile(t *testing.T) {
	if len(builtinDashboards) != len(builtinOrder) {
		t.Fatalf("%d built-ins", len(builtinDashboards))
	}
	for _, d := range builtinDashboards {
		if !d.Builtin || d.UserID != "" || len(d.Panels) == 0 {
			t.Errorf("%s: %+v", d.ID, d)
		}
		for _, f := range d.Filters {
			if !anyGrainHasDim(f) {
				t.Errorf("%s: filter %q", d.ID, f)
			}
		}
		for _, p := range d.Panels {
			q := p.Query
			if q.Score != nil {
				sc, ok := builtinScorecard(q.Score.Scorecard)
				if !ok {
					t.Fatalf("%s/%s: scorecard %s", d.ID, p.ID, q.Score.Scorecard)
				}
				q, _ = withTerms(q, sc)
			}
			if _, err := Compile(q, "u"); err != nil {
				t.Errorf("%s/%s: %v", d.ID, p.ID, err)
			}
		}
	}
	for _, sc := range builtinScorecards {
		in := ScorecardInput{Name: sc.Name, Terms: sc.Terms}
		if err := checkScorecard(&in); err != nil {
			t.Errorf("%s: %v", sc.ID, err)
		}
		q, _ := withTerms(Query{Grain: GrainRuns}, sc)
		if _, err := Compile(q, "u"); err != nil {
			t.Errorf("%s on runs: %v", sc.ID, err)
		}
	}
}

func near(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 1e-9 }

func TestScoreMinMax(t *testing.T) {
	sc := Scorecard{Terms: []Term{
		{Measure: MeasureRef{Field: "cost_usd", Agg: AggMedian}, Direction: DirLower, Weight: 3},
		{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 1},
	}}
	q, idx := withTerms(Query{Measures: []MeasureRef{{Field: "success_rate"}}}, sc)
	if len(q.Measures) != 2 || idx[0] != 1 || idx[1] != 0 {
		t.Fatalf("withTerms = %+v %v", q.Measures, idx)
	}
	rows := []Row{
		{Dims: []string{"a"}, Values: []*float64{new(1.0), new(1.0)}}, // best success, priciest
		{Dims: []string{"b"}, Values: []*float64{new(0.5), new(0.0)}}, // cheapest, mid success
		{Dims: []string{"c"}, Values: []*float64{new(0.0), nil}},      // no cost: success only
		{Dims: []string{"d"}, Values: []*float64{nil, nil}},           // nothing: unscored
	}
	if err := score(rows, sc, idx, nil); err != nil {
		t.Fatal(err)
	}
	// a: cost norm 0, success 1 → 100·(0·3+1·1)/4 = 25
	// b: cost norm 1, success 0.5 → 100·(3+0.5)/4 = 87.5
	// c: success 0 → 0
	if !near(rows[0].Score, 25) || !near(rows[1].Score, 87.5) || !near(rows[2].Score, 0) || rows[3].Score != nil {
		t.Errorf("scores %v %v %v %v", rows[0].Score, rows[1].Score, rows[2].Score, rows[3].Score)
	}
	sortByScore(rows, true)
	if rows[0].Dims[0] != "b" || rows[1].Dims[0] != "a" || rows[2].Dims[0] != "c" || rows[3].Dims[0] != "d" {
		t.Errorf("order %v %v %v %v", rows[0].Dims, rows[1].Dims, rows[2].Dims, rows[3].Dims)
	}
	sortByScore(rows, false)
	if rows[0].Dims[0] != "c" || rows[3].Dims[0] != "d" {
		t.Errorf("asc order %v .. %v", rows[0].Dims, rows[3].Dims)
	}

	// All equal: every row is the best.
	same := []Row{{Values: []*float64{new(2.0), new(2.0)}}, {Values: []*float64{new(2.0), new(2.0)}}}
	if err := score(same, sc, idx, nil); err != nil || !near(same[0].Score, 100) || !near(same[1].Score, 100) {
		t.Errorf("equal rows: %v %v %v", err, same[0].Score, same[1].Score)
	}
}

func TestScoreBaseline(t *testing.T) {
	sc := Scorecard{Terms: []Term{
		{Measure: MeasureRef{Field: "cost_usd", Agg: AggMedian}, Direction: DirLower, Weight: 1},
		{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 1},
	}}
	idx := []int{0, 1}
	rows := []Row{
		{Dims: []string{"f", "base"}, Values: []*float64{new(2.0), new(0.5)}},
		{Dims: []string{"f", "cheap"}, Values: []*float64{new(1.0), new(0.5)}},  // cost 2/1=2, success 1 → 150
		{Dims: []string{"f", "pricey"}, Values: []*float64{new(8.0), new(1.0)}}, // cost 2/8, success 2 → 112.5
		{Dims: []string{"f", "free"}, Values: []*float64{new(0.0), new(0.0)}},   // cost clamps to 2, success 0 → 100
		{Dims: []string{"f", "unknown"}, Values: []*float64{nil, new(0.25)}},    // success only: 0.5 → 50
	}
	if err := score(rows, sc, idx, []string{"f", "base"}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{100, 150, 112.5, 100, 50} {
		if !near(rows[i].Score, want) {
			t.Errorf("row %d score %v, want %v", i, rows[i].Score, want)
		}
	}
	if err := score(rows, sc, idx, []string{"nope"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "baseline row not in result") {
		t.Errorf("missing baseline = %v", err)
	}
	// A baseline with no value for a term skips that term everywhere.
	rows = []Row{{Dims: []string{"b"}, Values: []*float64{nil, new(1.0)}}, {Dims: []string{"x"}, Values: []*float64{new(3.0), new(0.5)}}}
	if err := score(rows, sc, idx, []string{"b"}); err != nil || !near(rows[0].Score, 100) || !near(rows[1].Score, 50) {
		t.Errorf("nil baseline term: %v %v %v", err, rows[0].Score, rows[1].Score)
	}
}

func TestCatalogMirrorsTheGrains(t *testing.T) {
	c := catalog()
	if len(c.Grains) != 5 {
		t.Fatalf("%d grains", len(c.Grains))
	}
	for i, g := range c.Grains {
		if g.ID != grains[i].id || g.TimeField == "" || len(g.Dimensions) == 0 || len(g.Measures) == 0 || len(g.Derived) == 0 {
			t.Errorf("grain %+v", g)
		}
		for _, m := range g.Measures {
			if len(m.Aggs) != len(numericAggs) {
				t.Errorf("%s/%s aggs %v", g.ID, m.ID, m.Aggs)
			}
		}
		for _, d := range g.Derived {
			if len(d.Aggs) != 0 {
				t.Errorf("%s/%s derived with aggs", g.ID, d.ID)
			}
		}
	}
}
