package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/analytics"
	"apothecary/internal/engine"
)

// memAnalytics is an analytics.Store in maps: queries answer canned rows (or
// err) and remember the user they were compiled for.
type memAnalytics struct {
	err error
	// listErr fails the dashboard and scorecard lists.
	listErr error
	asked   []any
	dash    map[string]analytics.Dashboard
	scores  map[string]analytics.Scorecard
}

func (m *memAnalytics) RunQuery(_ context.Context, c analytics.Compiled) ([]analytics.Row, error) {
	m.asked = append(m.asked, c.Args[0])
	return []analytics.Row{{Dims: make([]string, c.Dims), Values: make([]*float64, c.Measures), N: 7}}, m.err
}

func (m *memAnalytics) Drill(_ context.Context, c analytics.Compiled) ([]analytics.DrillRun, error) {
	m.asked = append(m.asked, c.Args[0])
	return []analytics.DrillRun{{RunID: "r1"}}, m.err
}

func (m *memAnalytics) Values(_ context.Context, c analytics.Compiled) ([]string, error) {
	m.asked = append(m.asked, c.Args[0])
	return []string{"completed"}, m.err
}

func (m *memAnalytics) FlowVersionLabels(context.Context, string, []string) (map[string]string, error) {
	return nil, nil
}

func (m *memAnalytics) ProjectNames(context.Context, string, []string) (map[string]string, error) {
	return nil, nil
}

func (m *memAnalytics) ListDashboards(_ context.Context, userID string) ([]analytics.Dashboard, error) {
	var out []analytics.Dashboard
	for _, d := range m.dash {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, m.listErr
}

func (m *memAnalytics) GetDashboard(_ context.Context, id string) (analytics.Dashboard, error) {
	d, ok := m.dash[id]
	if !ok {
		return d, fmt.Errorf("dashboard %s: %w", id, analytics.ErrNotFound)
	}
	return d, nil
}

func (m *memAnalytics) PutDashboard(_ context.Context, d analytics.Dashboard) error {
	m.dash[d.ID] = d
	return nil
}

func (m *memAnalytics) DeleteDashboard(_ context.Context, id string) error {
	delete(m.dash, id)
	return nil
}

func (m *memAnalytics) ListScorecards(_ context.Context, userID string) ([]analytics.Scorecard, error) {
	var out []analytics.Scorecard
	for _, s := range m.scores {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, m.listErr
}

func (m *memAnalytics) GetScorecard(_ context.Context, id string) (analytics.Scorecard, error) {
	s, ok := m.scores[id]
	if !ok {
		return s, fmt.Errorf("scorecard %s: %w", id, analytics.ErrNotFound)
	}
	return s, nil
}

func (m *memAnalytics) PutScorecard(_ context.Context, s analytics.Scorecard) error {
	m.scores[s.ID] = s
	return nil
}

func (m *memAnalytics) DeleteScorecard(_ context.Context, id string) error {
	delete(m.scores, id)
	return nil
}

func newAnalyticsEnv(t *testing.T) (*env, *memAnalytics) {
	t.Helper()
	x := newEnv(t, engine.Config{})
	m := &memAnalytics{dash: map[string]analytics.Dashboard{}, scores: map[string]analytics.Scorecard{}}
	x.srv.Analytics = &analytics.Service{Store: m}
	x.ts.Close()
	x.ts = httptest.NewServer(x.srv.Handler())
	t.Cleanup(x.ts.Close)
	return x, m
}

func TestAnalyticsQueriesAreTheCallers(t *testing.T) {
	x, m := newAnalyticsEnv(t)
	bob := x.addUser(t, "bob", false)

	resp, data := x.do(t, "GET", "/api/analytics/catalog", nil, nil)
	if resp.StatusCode != http.StatusOK || len(decode[analytics.Catalog](t, data).Grains) != 5 {
		t.Fatalf("catalog = %d %s", resp.StatusCode, data)
	}
	q := analytics.Query{Grain: analytics.GrainRuns, GroupBy: []string{"state"}, Measures: []analytics.MeasureRef{{Field: "cost_usd", Agg: analytics.AggSum}}}
	resp, data = x.do(t, "POST", "/api/analytics/query", q, nil)
	if res := decode[analytics.Result](t, data); resp.StatusCode != http.StatusOK || len(res.Rows) != 1 || res.Rows[0].N != 7 {
		t.Errorf("query = %d %s", resp.StatusCode, data)
	}
	x.do(t, "POST", "/api/analytics/query", q, bob)
	resp, data = x.do(t, "POST", "/api/analytics/values", ValuesRequest{Grain: analytics.GrainRuns, Field: "state"}, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"completed"`) {
		t.Errorf("values = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/analytics/drill", DrillRequest{Grain: analytics.GrainPings}, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"r1"`) {
		t.Errorf("drill = %d %s", resp.StatusCode, data)
	}
	if fmt.Sprint(m.asked) != fmt.Sprintf("[%s bob %s %s]", testUser, testUser, testUser) {
		t.Errorf("compiled for %v", m.asked)
	}

	for name, c := range map[string]struct {
		path string
		body any
	}{
		"query":  {"/api/analytics/query", analytics.Query{Grain: "nope"}},
		"values": {"/api/analytics/values", ValuesRequest{Grain: analytics.GrainRuns, Field: "user_id"}},
		"drill":  {"/api/analytics/drill", DrillRequest{Grain: analytics.GrainTickets}},
	} {
		if resp, data := x.do(t, "POST", c.path, c.body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s invalid = %d %s", name, resp.StatusCode, data)
		}
	}
	m.err = fmt.Errorf("canceling statement: %w", analytics.ErrTimeout)
	resp, data = x.do(t, "POST", "/api/analytics/query", q, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(data), "query took longer than 5s; narrow the time range") {
		t.Errorf("timeout = %d %s", resp.StatusCode, data)
	}
	m.err = fmt.Errorf("connection refused")
	for _, path := range []string{"/api/analytics/query", "/api/analytics/values", "/api/analytics/drill"} {
		body := any(q)
		if path == "/api/analytics/values" {
			body = ValuesRequest{Grain: analytics.GrainRuns, Field: "state"}
		} else if path == "/api/analytics/drill" {
			body = DrillRequest{Grain: analytics.GrainRuns}
		}
		if resp, _ := x.do(t, "POST", path, body, nil); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s store error = %d", path, resp.StatusCode)
		}
	}
	if resp, _ := x.do(t, "POST", "/api/analytics/query", "{", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON = %d", resp.StatusCode)
	}
}

// A dashboard or scorecard is its owner's alone; a built-in is everyone's to
// read and nobody's to change.
func TestAnalyticsDashboardsAndScorecardsAreScoped(t *testing.T) {
	x, _ := newAnalyticsEnv(t)
	bob := x.addUser(t, "bob", false)

	in := analytics.DashboardInput{Name: "Mine", Panels: []analytics.Panel{{Title: "n", Viz: "number", Width: 3, Query: analytics.Query{Grain: analytics.GrainRuns}}}}
	resp, data := x.do(t, "POST", "/api/analytics/dashboards", in, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, data)
	}
	d := decode[analytics.Dashboard](t, data)
	if d.UserID != testUser {
		t.Errorf("owner %q", d.UserID)
	}
	path := "/api/analytics/dashboards/" + d.ID
	if resp, data := x.do(t, "GET", path, nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("get = %d %s", resp.StatusCode, data)
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if resp, data := x.do(t, method, path, in, bob); resp.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "dashboard "+d.ID+" not found") {
			t.Errorf("bob %s = %d %s", method, resp.StatusCode, data)
		}
	}
	resp, data = x.do(t, "GET", "/api/analytics/dashboards", nil, bob)
	if list := decode[[]analytics.Dashboard](t, data); resp.StatusCode != http.StatusOK || len(list) != 6 || !list[0].Builtin {
		t.Errorf("bob's list = %d %s", resp.StatusCode, data)
	}
	if resp, data := x.do(t, "GET", "/api/analytics/dashboards/builtin-overview", nil, bob); resp.StatusCode != http.StatusOK {
		t.Errorf("built-in = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "GET", "/api/analytics/dashboards/builtin-nope", nil, bob); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown built-in = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "PUT", "/api/analytics/dashboards/builtin-overview", in, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("update built-in = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", "/api/analytics/dashboards/builtin-overview", nil, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("delete built-in = %d", resp.StatusCode)
	}
	in.Name = "Renamed"
	if resp, data := x.do(t, "PUT", path, in, nil); resp.StatusCode != http.StatusOK || decode[analytics.Dashboard](t, data).Name != "Renamed" {
		t.Errorf("update = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "PUT", path, analytics.DashboardInput{}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid update = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "POST", "/api/analytics/dashboards", analytics.DashboardInput{}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid create = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", path, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "GET", path, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleted = %d", resp.StatusCode)
	}

	sc := analytics.ScorecardInput{Name: "Cheap", Terms: []analytics.Term{{Measure: analytics.MeasureRef{Field: "cost_usd", Agg: analytics.AggSum}, Direction: "lower", Weight: 1}}}
	resp, data = x.do(t, "POST", "/api/analytics/scorecards", sc, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create scorecard = %d %s", resp.StatusCode, data)
	}
	c := decode[analytics.Scorecard](t, data)
	spath := "/api/analytics/scorecards/" + c.ID
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if resp, _ := x.do(t, method, spath, sc, bob); resp.StatusCode != http.StatusNotFound {
			t.Errorf("bob %s scorecard = %d", method, resp.StatusCode)
		}
	}
	// Bob cannot rank by it either: it reads as not there.
	q := analytics.Query{Grain: analytics.GrainRuns, Score: &analytics.ScoreSpec{Scorecard: c.ID}}
	if resp, _ := x.do(t, "POST", "/api/analytics/query", q, bob); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob scores by it = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "POST", "/api/analytics/query", q, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("owner scores by it = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "GET", "/api/analytics/scorecards", nil, nil)
	if list := decode[[]analytics.Scorecard](t, data); resp.StatusCode != http.StatusOK || len(list) != 5 {
		t.Errorf("list = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "GET", "/api/analytics/scorecards/builtin-balanced", nil, bob); resp.StatusCode != http.StatusOK {
		t.Errorf("built-in scorecard = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "PUT", "/api/analytics/scorecards/builtin-balanced", sc, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("update built-in scorecard = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", "/api/analytics/scorecards/builtin-balanced", nil, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("delete built-in scorecard = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "PUT", spath, analytics.ScorecardInput{Name: "x"}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid scorecard = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "POST", "/api/analytics/scorecards", analytics.ScorecardInput{}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid create scorecard = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "PUT", spath, sc, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("update scorecard = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", spath, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete scorecard = %d", resp.StatusCode)
	}
}

// Without the service the routes are not served — and the guard's owner
// lookup says so rather than panicking.
func TestAnalyticsOffWithoutTheService(t *testing.T) {
	x := newEnv(t, engine.Config{})
	if resp, _ := x.do(t, "GET", "/api/analytics/catalog", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("catalog = %d", resp.StatusCode)
	}
	if _, err := x.srv.analyticsOwner(context.Background(), ScopeDashboard, "d1"); err == nil {
		t.Error("owner without a service")
	}
}

// A body that is not the request's shape is a 400 on every write; a store
// that cannot list is a 500, never an empty list; a built-in id that names
// nothing is a 404.
func TestAnalyticsBadBodiesAndFailingLists(t *testing.T) {
	x, m := newAnalyticsEnv(t)
	m.dash["d1"] = analytics.Dashboard{ID: "d1", UserID: testUser, Name: "d"}
	m.scores["s1"] = analytics.Scorecard{ID: "s1", UserID: testUser, Name: "s"}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/analytics/values"},
		{"POST", "/api/analytics/drill"},
		{"POST", "/api/analytics/dashboards"},
		{"PUT", "/api/analytics/dashboards/d1"},
		{"POST", "/api/analytics/scorecards"},
		{"PUT", "/api/analytics/scorecards/s1"},
	} {
		if resp, data := x.do(t, c.method, c.path, "not an object", nil); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid JSON body") {
			t.Errorf("%s %s = %d %s", c.method, c.path, resp.StatusCode, data)
		}
	}
	if m.dash["d1"].Name != "d" || m.scores["s1"].Name != "s" {
		t.Error("a bad body changed a row")
	}
	m.listErr = fmt.Errorf("connection refused")
	for _, path := range []string{"/api/analytics/dashboards", "/api/analytics/scorecards"} {
		if resp, data := x.do(t, "GET", path, nil, nil); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s = %d %s", path, resp.StatusCode, data)
		}
	}
	if resp, data := x.do(t, "GET", "/api/analytics/scorecards/builtin-nope", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown built-in scorecard = %d %s", resp.StatusCode, data)
	}
}
