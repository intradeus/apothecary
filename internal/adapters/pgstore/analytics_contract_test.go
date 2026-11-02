package pgstore

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"apothecary/internal/analytics"
	"apothecary/internal/analytics/analyticstest"
)

func TestAnalyticsContract(t *testing.T) {
	analyticstest.RunContract(t, func(t *testing.T) analyticstest.Fixture {
		s := open(t)
		return analyticstest.Fixture{Store: s, Events: s}
	})
}

// Flow versions are named "<flow> v<n>" by their order within the flow and
// project, and project names come from the caller's own projects only.
func TestAnalyticsLabels(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedIntegration(t, s, "i2", "u2")
	seedProject(t, s, mkProject("p1", "Shop", "u1", "i1"))
	seedProject(t, s, mkProject("p2", "Theirs", "u2", "i2"))
	for i, v := range []struct{ project, name, hash string }{
		{"p1", "builder", "h1"}, {"p1", "builder", "h2"}, {"p1", "fixer", "h3"}, {"p2", "builder", "h9"},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO flow_versions (project_id, hash, name, yaml, created_at) VALUES ($1,$2,$3,'',$4)`,
			v.project, v.hash, v.name, testNow.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	labels, err := s.FlowVersionLabels(ctx, "u1", []string{"h1", "h2", "h3", "h9", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"h1": "builder v1", "h2": "builder v2", "h3": "fixer v1"}
	if len(labels) != len(want) {
		t.Errorf("labels = %v", labels)
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
	names, err := s.ProjectNames(ctx, "u1", []string{"p1", "p2"})
	if err != nil || len(names) != 1 || names["p1"] != "Shop" {
		t.Errorf("names = %v, %v", names, err)
	}
}

// A query past the statement timeout is ErrTimeout, which the API turns into
// "narrow the time range" rather than a 500.
func TestAnalyticsQueryTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 5s statement timeout")
	}
	s := open(t)
	_, err := s.RunQuery(context.Background(), analytics.Compiled{SQL: `SELECT pg_sleep(6)::text, count(*) FROM (SELECT 1) x GROUP BY 1`, Dims: 1})
	if !errors.Is(err, analytics.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	// And the store refuses to write through it: the transaction is read-only.
	_, err = s.RunQuery(context.Background(), analytics.Compiled{SQL: `INSERT INTO settings (key, value) VALUES ('x', '1') RETURNING 1::bigint`})
	if err == nil {
		t.Error("a write ran in an analytics query")
	}
}

// A query the database rejects, or whose columns do not fit its compiled
// shape, is an error — never an empty chart.
func TestAnalyticsQueryErrorsAreErrors(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	bad := analytics.Compiled{SQL: `SELECT FROM nowhere`}
	if rows, err := s.RunQuery(ctx, bad); err == nil {
		t.Errorf("rejected query = %v", rows)
	}
	if runs, err := s.Drill(ctx, bad); err == nil {
		t.Errorf("rejected drill = %v", runs)
	}
	if vals, err := s.Values(ctx, bad); err == nil {
		t.Errorf("rejected values = %v", vals)
	}
	// One column where the shape wants a dimension and a count.
	if rows, err := s.RunQuery(ctx, analytics.Compiled{SQL: `SELECT 'x'`, Dims: 1}); err == nil {
		t.Errorf("misshapen query = %v", rows)
	}
	if runs, err := s.Drill(ctx, analytics.Compiled{SQL: `SELECT 'r1'`}); err == nil {
		t.Errorf("misshapen drill = %v", runs)
	}
	// A shape that fits reads its NULLs as empty dims and absent values.
	rows, err := s.RunQuery(ctx, analytics.Compiled{SQL: `SELECT NULL::text, NULL::float8, 2.5::float8, 3::bigint`, Dims: 1, Measures: 2})
	if err != nil || len(rows) != 1 || rows[0].Dims[0] != "" || rows[0].Values[0] != nil || *rows[0].Values[1] != 2.5 || rows[0].N != 3 {
		t.Errorf("rows = %+v, %v", rows, err)
	}
}

// A scorecard JSON cannot hold (a NaN weight) is refused, and nothing is
// stored.
func TestAnalyticsUnencodableScorecardIsRefused(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	sc := analytics.Scorecard{ID: "s1", UserID: "u1", Name: "n", CreatedAt: testNow, UpdatedAt: testNow,
		Terms: []analytics.Term{{Measure: analytics.MeasureRef{Field: "success_rate"}, Direction: analytics.DirHigher, Weight: math.NaN()}}}
	if err := s.PutScorecard(ctx, sc); err == nil {
		t.Fatal("a NaN weight was stored")
	}
	if _, err := s.GetScorecard(ctx, "s1"); !errors.Is(err, analytics.ErrNotFound) {
		t.Errorf("after refusal = %v", err)
	}
}
