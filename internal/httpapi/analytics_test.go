package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
	// dashVers and scoreVers keep every write, oldest first.
	dashVers  map[string][]analytics.DashboardVersion
	scoreVers map[string][]analytics.ScorecardVersion
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

func (m *memAnalytics) FlowVersionLabels(context.Context, []string, []string) (map[string]string, error) {
	return nil, nil
}

func (m *memAnalytics) ProjectNames(context.Context, []string, []string) (map[string]string, error) {
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

func (m *memAnalytics) PutDashboard(_ context.Context, d analytics.Dashboard, c analytics.Change) error {
	m.dash[d.ID] = d
	vs := m.dashVers[d.ID]
	m.dashVers[d.ID] = append(vs, analytics.DashboardVersion{DashboardID: d.ID, Seq: len(vs) + 1, Name: d.Name, Filters: d.Filters, Panels: d.Panels, Change: c, CreatedAt: d.UpdatedAt})
	return nil
}

func (m *memAnalytics) ListDashboardVersions(_ context.Context, id string) ([]analytics.DashboardVersion, error) {
	if _, ok := m.dash[id]; !ok {
		return nil, fmt.Errorf("dashboard %s: %w", id, analytics.ErrNotFound)
	}
	vs := slices.Clone(m.dashVers[id])
	slices.Reverse(vs)
	return vs, nil
}

func (m *memAnalytics) GetDashboardVersion(_ context.Context, id string, seq int) (analytics.DashboardVersion, error) {
	if vs := m.dashVers[id]; seq >= 1 && seq <= len(vs) {
		return vs[seq-1], nil
	}
	return analytics.DashboardVersion{}, fmt.Errorf("dashboard %s version %d: %w", id, seq, analytics.ErrNotFound)
}

func (m *memAnalytics) DeleteDashboard(_ context.Context, id string) error {
	delete(m.dash, id)
	delete(m.dashVers, id)
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

func (m *memAnalytics) PutScorecard(_ context.Context, s analytics.Scorecard, c analytics.Change) error {
	m.scores[s.ID] = s
	vs := m.scoreVers[s.ID]
	m.scoreVers[s.ID] = append(vs, analytics.ScorecardVersion{ScorecardID: s.ID, Seq: len(vs) + 1, Name: s.Name, Terms: s.Terms, Change: c, CreatedAt: s.UpdatedAt})
	return nil
}

func (m *memAnalytics) ListScorecardVersions(_ context.Context, id string) ([]analytics.ScorecardVersion, error) {
	if _, ok := m.scores[id]; !ok {
		return nil, fmt.Errorf("scorecard %s: %w", id, analytics.ErrNotFound)
	}
	vs := slices.Clone(m.scoreVers[id])
	slices.Reverse(vs)
	return vs, nil
}

func (m *memAnalytics) GetScorecardVersion(_ context.Context, id string, seq int) (analytics.ScorecardVersion, error) {
	if vs := m.scoreVers[id]; seq >= 1 && seq <= len(vs) {
		return vs[seq-1], nil
	}
	return analytics.ScorecardVersion{}, fmt.Errorf("scorecard %s version %d: %w", id, seq, analytics.ErrNotFound)
}

func (m *memAnalytics) DeleteScorecard(_ context.Context, id string) error {
	delete(m.scores, id)
	delete(m.scoreVers, id)
	return nil
}

func newAnalyticsEnv(t *testing.T) (*env, *memAnalytics) {
	t.Helper()
	x := newEnv(t, engine.Config{})
	m := &memAnalytics{dash: map[string]analytics.Dashboard{}, scores: map[string]analytics.Scorecard{},
		dashVers: map[string][]analytics.DashboardVersion{}, scoreVers: map[string][]analytics.ScorecardVersion{}}
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
	// Each query is over the workshops its caller sees: bob sees none.
	if fmt.Sprint(m.asked) != fmt.Sprintf("[[%s] [] [%s] [%s]]", testProject, testProject, testProject) {
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

// A dashboard's and a scorecard's history: listed newest first with what
// changed and who (by handle), one version read whole, one restored as a
// new version; another person's history is not there; a built-in has
// none and is not restored.
func TestAnalyticsVersions(t *testing.T) {
	x, m := newAnalyticsEnv(t)
	bob := x.addUser(t, "bob", false)
	panel := analytics.Panel{ID: "p1", Title: "n", Viz: "number", Width: 3, Query: analytics.Query{Grain: analytics.GrainRuns}}
	resp, data := x.do(t, "POST", "/api/analytics/dashboards", map[string]any{"name": "Mine", "panels": []analytics.Panel{panel}}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, data)
	}
	d := decode[analytics.Dashboard](t, data)
	path := "/api/analytics/dashboards/" + d.ID
	panel2 := panel
	panel2.ID, panel2.Title = "p2", "two"
	if resp, data := x.do(t, "PUT", path, map[string]any{"name": "Renamed", "panels": []analytics.Panel{panel, panel2}, "assisted": true}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", path+"/versions", nil, nil)
	list := decode[[]DashboardVersionSummary](t, data)
	if resp.StatusCode != http.StatusOK || len(list) != 2 || list[0].Seq != 2 || list[0].AuthorKind != analytics.AuthorAssistant ||
		list[0].AuthorHandle != "tester" || !slices.Equal(list[0].Changed, []string{"name", "panels"}) ||
		list[0].PanelChanges == nil || list[0].PanelChanges.Added != 1 || list[1].AuthorKind != analytics.AuthorUser || !list[1].Oldest {
		t.Fatalf("versions = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", path+"/versions/1", nil, nil)
	if v := decode[DashboardVersionView](t, data); resp.StatusCode != http.StatusOK || v.Name != "Mine" || len(v.Panels) != 1 || v.AuthorHandle != "tester" {
		t.Errorf("version 1 = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "GET", path+"/versions/x", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad seq = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "GET", path+"/versions/9", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing seq = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "POST", path+"/versions/1/restore", nil, nil)
	if got := decode[analytics.Dashboard](t, data); resp.StatusCode != http.StatusOK || got.Name != "Mine" || len(got.Panels) != 1 {
		t.Errorf("restore = %d %s", resp.StatusCode, data)
	}
	if vs := m.dashVers[d.ID]; len(vs) != 3 || vs[2].RestoredFrom != 1 || vs[2].Kind != analytics.AuthorUser || vs[2].AuthorID != testUser {
		t.Errorf("restored version = %+v", vs)
	}
	// Content the catalog no longer allows is a 400.
	m.dashVers[d.ID] = append(m.dashVers[d.ID], analytics.DashboardVersion{DashboardID: d.ID, Seq: 4, Name: "Old",
		Panels: []analytics.Panel{{ID: "x", Title: "x", Viz: "pie", Width: 3, Query: analytics.Query{Grain: analytics.GrainRuns}}}})
	if resp, _ := x.do(t, "POST", path+"/versions/4/restore", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid restore = %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path string }{{"GET", path + "/versions"}, {"GET", path + "/versions/1"}, {"POST", path + "/versions/1/restore"}} {
		if resp, _ := x.do(t, c.method, c.path, nil, bob); resp.StatusCode != http.StatusNotFound {
			t.Errorf("bob %s %s = %d", c.method, c.path, resp.StatusCode)
		}
	}
	if resp, data := x.do(t, "GET", "/api/analytics/dashboards/builtin-overview/versions", nil, bob); resp.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("built-in versions = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "POST", "/api/analytics/dashboards/builtin-overview/versions/1/restore", nil, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("built-in restore = %d", resp.StatusCode)
	}

	terms := func(w float64) []analytics.Term {
		return []analytics.Term{{Measure: analytics.MeasureRef{Field: "cost_usd", Agg: analytics.AggSum}, Direction: "lower", Weight: w}}
	}
	resp, data = x.do(t, "POST", "/api/analytics/scorecards", map[string]any{"name": "Cheap", "terms": terms(1), "assisted": true}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create scorecard = %d %s", resp.StatusCode, data)
	}
	sc := decode[analytics.Scorecard](t, data)
	spath := "/api/analytics/scorecards/" + sc.ID
	if resp, data := x.do(t, "PUT", spath, map[string]any{"name": "Cheap", "terms": terms(2)}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("update scorecard = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", spath+"/versions", nil, nil)
	slist := decode[[]ScorecardVersionSummary](t, data)
	if resp.StatusCode != http.StatusOK || len(slist) != 2 || !slices.Equal(slist[0].Changed, []string{"terms"}) || slist[0].AuthorKind != analytics.AuthorUser ||
		slist[1].AuthorKind != analytics.AuthorAssistant || slist[1].AuthorHandle != "tester" || !slist[1].Oldest {
		t.Fatalf("scorecard versions = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", spath+"/versions/1", nil, nil)
	if v := decode[ScorecardVersionView](t, data); resp.StatusCode != http.StatusOK || v.Terms[0].Weight != 1 {
		t.Errorf("scorecard version 1 = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "GET", spath+"/versions/0", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("seq 0 = %d", resp.StatusCode)
	}
	resp, data = x.do(t, "POST", spath+"/versions/1/restore", nil, nil)
	if got := decode[analytics.Scorecard](t, data); resp.StatusCode != http.StatusOK || got.Terms[0].Weight != 1 {
		t.Errorf("restore scorecard = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "POST", spath+"/versions/9/restore", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("restore missing seq = %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path string }{{"GET", spath + "/versions"}, {"GET", spath + "/versions/1"}, {"POST", spath + "/versions/1/restore"}} {
		if resp, _ := x.do(t, c.method, c.path, nil, bob); resp.StatusCode != http.StatusNotFound {
			t.Errorf("bob %s %s = %d", c.method, c.path, resp.StatusCode)
		}
	}
	if resp, data := x.do(t, "GET", "/api/analytics/scorecards/builtin-balanced/versions", nil, nil); resp.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("built-in scorecard versions = %d %s", resp.StatusCode, data)
	}
	if resp, _ := x.do(t, "GET", "/api/analytics/scorecards/builtin-balanced/versions/1", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("built-in scorecard version = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "POST", "/api/analytics/scorecards/builtin-balanced/versions/1/restore", nil, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("built-in scorecard restore = %d", resp.StatusCode)
	}
}

// A conflicting save (another writer took the version) is a 409.
func TestAnalyticsConflictIs409(t *testing.T) {
	rec := httptest.NewRecorder()
	analyticsErr(rec, fmt.Errorf("d1 changed meanwhile: %w", analytics.ErrConflict))
	if rec.Code != http.StatusConflict {
		t.Errorf("conflict = %d", rec.Code)
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
