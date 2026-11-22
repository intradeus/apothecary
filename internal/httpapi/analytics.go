package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	"apothecary/internal/analytics"
	"apothecary/internal/project/confstore"
)

// Tool argument docs for the dashboard and scorecard version tools.
const (
	argDashboard        = "The dashboard's id, from list_dashboards."
	argDashboardVersion = "The version's seq, from list_dashboard_versions."
	argScorecard        = "The scorecard's id, from list_scorecards."
	argScorecardVersion = "The version's seq, from list_scorecard_versions."
)

// Run analytics (TODO 23): queries over the runs of every workshop the caller
// may see (authz.KindRun visibility, bound into the SQL by the service), and
// the dashboards and scorecards they keep. The id'd routes are scoped by the
// guard (ScopeDashboard / ScopeScorecard); a built-in is everyone's.

// ValuesRequest asks for one dimension's distinct values.
type ValuesRequest struct {
	Grain analytics.Grain     `json:"grain"`
	Field string              `json:"field"`
	Time  analytics.TimeRange `json:"time"`
}

// DrillRequest asks for the runs behind a group.
type DrillRequest struct {
	Grain   analytics.Grain     `json:"grain"`
	Filters []analytics.Filter  `json:"filters"`
	Time    analytics.TimeRange `json:"time"`
}

func analyticsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, analytics.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, analytics.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, analytics.ErrReadOnly), errors.Is(err, analytics.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, analytics.ErrTimeout):
		writeErr(w, http.StatusUnprocessableEntity, "query took longer than 5s; narrow the time range")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// analyticsOwner is whose a dashboard or scorecard is: a built-in is the
// caller's (everyone reads it; the service refuses writes to it).
func (s *Server) analyticsOwner(ctx context.Context, sc Scope, id string) (string, error) {
	if s.Analytics == nil {
		return "", errors.New("no analytics")
	}
	if strings.HasPrefix(id, "builtin-") {
		return auth.UserID(ctx), nil
	}
	if sc == ScopeDashboard {
		d, err := s.Analytics.GetDashboard(ctx, id)
		return d.UserID, err
	}
	c, err := s.Analytics.GetScorecard(ctx, id)
	return c.UserID, err
}

func (s *Server) getAnalyticsCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Analytics.Catalog())
}

func (s *Server) queryAnalytics(w http.ResponseWriter, r *http.Request) {
	var q analytics.Query
	if !readJSON(w, r, &q) {
		return
	}
	v, err := s.visible(r.Context(), authz.KindRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, err := s.Analytics.Query(r.Context(), auth.UserID(r.Context()), v.ProjectIDs, q)
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) analyticsValues(w http.ResponseWriter, r *http.Request) {
	var body ValuesRequest
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.visible(r.Context(), authz.KindRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	vals, err := s.Analytics.Values(r.Context(), v.ProjectIDs, body.Grain, body.Field, body.Time)
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vals)
}

func (s *Server) drillAnalytics(w http.ResponseWriter, r *http.Request) {
	var body DrillRequest
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.visible(r.Context(), authz.KindRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs, err := s.Analytics.Drill(r.Context(), v.ProjectIDs, body.Grain, body.Filters, body.Time)
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) listDashboards(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Analytics.ListDashboards(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

// DashboardBody is a dashboard as written: assisted says an assistant drew
// it (its version is the assistant's, on the caller's behalf).
type DashboardBody struct {
	analytics.DashboardInput
	Assisted bool `json:"assisted,omitempty"`
}

// ScorecardBody is a scorecard as written; assisted as DashboardBody's.
type ScorecardBody struct {
	analytics.ScorecardInput
	Assisted bool `json:"assisted,omitempty"`
}

// analyticsChange is who a write is by: the caller, or an assistant on
// their behalf when the body or the request says so.
func analyticsChange(ctx context.Context, assisted bool) analytics.Change {
	kind := analytics.AuthorUser
	if assisted || confstore.AssistedChange(ctx) {
		kind = analytics.AuthorAssistant
	}
	return analytics.Change{Kind: kind, AuthorID: auth.UserID(ctx)}
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var in DashboardBody
	if !readJSON(w, r, &in) {
		return
	}
	d, err := s.Analytics.CreateDashboard(r.Context(), auth.UserID(r.Context()), in.DashboardInput, analyticsChange(r.Context(), in.Assisted))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.Analytics.GetDashboard(r.Context(), r.PathValue("id"))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request) {
	var in DashboardBody
	if !readJSON(w, r, &in) {
		return
	}
	d, err := s.Analytics.UpdateDashboard(r.Context(), r.PathValue("id"), in.DashboardInput, analyticsChange(r.Context(), in.Assisted))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	if err := s.Analytics.DeleteDashboard(r.Context(), r.PathValue("id")); err != nil {
		analyticsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listScorecards(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Analytics.ListScorecards(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) createScorecard(w http.ResponseWriter, r *http.Request) {
	var in ScorecardBody
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Analytics.CreateScorecard(r.Context(), auth.UserID(r.Context()), in.ScorecardInput, analyticsChange(r.Context(), in.Assisted))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) getScorecard(w http.ResponseWriter, r *http.Request) {
	c, err := s.Analytics.GetScorecard(r.Context(), r.PathValue("id"))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) updateScorecard(w http.ResponseWriter, r *http.Request) {
	var in ScorecardBody
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Analytics.UpdateScorecard(r.Context(), r.PathValue("id"), in.ScorecardInput, analyticsChange(r.Context(), in.Assisted))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteScorecard(w http.ResponseWriter, r *http.Request) {
	if err := s.Analytics.DeleteScorecard(r.Context(), r.PathValue("id")); err != nil {
		analyticsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DashboardVersionSummary is one kept version of a dashboard, as a history
// lists it: what changed against the version before it, not the content.
type DashboardVersionSummary struct {
	Seq          int                     `json:"seq"`
	Name         string                  `json:"name"`
	AuthorKind   analytics.AuthorKind    `json:"author_kind"`
	AuthorID     string                  `json:"author_id"`
	AuthorHandle string                  `json:"author_handle,omitempty"`
	RestoredFrom int                     `json:"restored_from,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
	Changed      []string                `json:"changed,omitempty"`
	PanelChanges *analytics.PanelChanges `json:"panel_changes,omitempty"`
	Oldest       bool                    `json:"oldest,omitempty"`
}

// DashboardVersionView is one kept version of a dashboard, its content
// whole.
type DashboardVersionView struct {
	analytics.DashboardVersion
	AuthorHandle string `json:"author_handle,omitempty"`
}

// ScorecardVersionSummary is one kept version of a scorecard, as a history
// lists it.
type ScorecardVersionSummary struct {
	Seq          int                  `json:"seq"`
	Name         string               `json:"name"`
	AuthorKind   analytics.AuthorKind `json:"author_kind"`
	AuthorID     string               `json:"author_id"`
	AuthorHandle string               `json:"author_handle,omitempty"`
	RestoredFrom int                  `json:"restored_from,omitempty"`
	CreatedAt    time.Time            `json:"created_at"`
	Changed      []string             `json:"changed,omitempty"`
	Oldest       bool                 `json:"oldest,omitempty"`
}

// ScorecardVersionView is one kept version of a scorecard, its terms whole.
type ScorecardVersionView struct {
	analytics.ScorecardVersion
	AuthorHandle string `json:"author_handle,omitempty"`
}

func (s *Server) listDashboardVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Analytics.DashboardVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	handles := s.authorHandles()
	out := make([]DashboardVersionSummary, 0, len(vs))
	for _, v := range vs {
		out = append(out, DashboardVersionSummary{Seq: v.Seq, Name: v.Name, AuthorKind: v.Kind, AuthorID: v.AuthorID,
			AuthorHandle: handles.of(r.Context(), string(v.Kind), v.AuthorID), RestoredFrom: v.RestoredFrom, CreatedAt: v.CreatedAt,
			Changed: v.Changed, PanelChanges: v.PanelChanges, Oldest: v.Oldest})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDashboardVersion(w http.ResponseWriter, r *http.Request) {
	seq, ok := versionSeq(w, r)
	if !ok {
		return
	}
	v, err := s.Analytics.DashboardVersion(r.Context(), r.PathValue("id"), seq)
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DashboardVersionView{DashboardVersion: v, AuthorHandle: s.authorHandles().of(r.Context(), string(v.Kind), v.AuthorID)})
}

func (s *Server) restoreDashboardVersion(w http.ResponseWriter, r *http.Request) {
	seq, ok := versionSeq(w, r)
	if !ok {
		return
	}
	d, err := s.Analytics.RestoreDashboardVersion(r.Context(), r.PathValue("id"), seq, analyticsChange(r.Context(), false))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) listScorecardVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Analytics.ScorecardVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	handles := s.authorHandles()
	out := make([]ScorecardVersionSummary, 0, len(vs))
	for _, v := range vs {
		out = append(out, ScorecardVersionSummary{Seq: v.Seq, Name: v.Name, AuthorKind: v.Kind, AuthorID: v.AuthorID,
			AuthorHandle: handles.of(r.Context(), string(v.Kind), v.AuthorID), RestoredFrom: v.RestoredFrom, CreatedAt: v.CreatedAt,
			Changed: v.Changed, Oldest: v.Oldest})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getScorecardVersion(w http.ResponseWriter, r *http.Request) {
	seq, ok := versionSeq(w, r)
	if !ok {
		return
	}
	v, err := s.Analytics.ScorecardVersion(r.Context(), r.PathValue("id"), seq)
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ScorecardVersionView{ScorecardVersion: v, AuthorHandle: s.authorHandles().of(r.Context(), string(v.Kind), v.AuthorID)})
}

func (s *Server) restoreScorecardVersion(w http.ResponseWriter, r *http.Request) {
	seq, ok := versionSeq(w, r)
	if !ok {
		return
	}
	c, err := s.Analytics.RestoreScorecardVersion(r.Context(), r.PathValue("id"), seq, analyticsChange(r.Context(), false))
	if err != nil {
		analyticsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) analyticsRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/analytics/catalog", ID: "getAnalyticsCatalog", Tag: tagAnalytics,
			Summary: "Every grain, dimension and measure a query may name",
			Handler: s.getAnalyticsCatalog, Returns: analytics.Catalog{}, Enabled: haveAnalytics, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "get_analytics_catalog", Doc: "Every grain, dimension and measure a dashboard panel or a scorecard term may name.", Args: map[string]string{}}},
		{Method: "POST", Path: "/api/analytics/query", ID: "queryAnalytics", Tag: tagAnalytics,
			Summary: "Run a query over one's own runs",
			Doc: "A whitelisted shape — grain, measures, group-by, filters, time range — checked against the catalog. " +
				"At most 8 measures, 3 group-by dimensions, 10 filters and 500 rows; 422 when it runs longer than 5s.",
			Handler: s.queryAnalytics, Body: analytics.Query{}, Returns: analytics.Result{}, Enabled: haveAnalytics, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "query_analytics", Doc: "Run an analytics query over your runs: a grain, measures, group-by dimensions, filters and a time range, named from get_analytics_catalog. Where a flow's runs fail, what they cost, how long they wait for a person. At most 500 rows.", Args: map[string]string{}}},
		{Method: "POST", Path: "/api/analytics/values", ID: "analyticsValues", Tag: tagAnalytics,
			Summary: "One dimension's distinct values (at most 200), labelled",
			Handler: s.analyticsValues, Body: ValuesRequest{}, Returns: []analytics.Value{}, Enabled: haveAnalytics, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "analytics_values", Doc: "One dimension's distinct values (at most 200), labelled: what a query's filter may name.", Args: map[string]string{}}},
		{Method: "POST", Path: "/api/analytics/drill", ID: "drillAnalytics", Tag: tagAnalytics,
			Summary: "The runs behind a group (at most 200, newest first)",
			Handler: s.drillAnalytics, Body: DrillRequest{}, Returns: []analytics.DrillRun{}, Enabled: haveAnalytics, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "drill_analytics", Doc: "The runs behind one group of a query (at most 200, newest first): which runs a number counts.", Args: map[string]string{}}},
		{Method: "GET", Path: "/api/analytics/dashboards", ID: "listDashboards", Tag: tagAnalytics,
			Summary: "The built-in dashboards, then one's own",
			Handler: s.listDashboards, Returns: []analytics.Dashboard{}, Enabled: haveAnalytics, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "list_dashboards", Doc: "The built-in dashboards, then one's own: their names and ids.", Args: map[string]string{}}},
		{Method: "POST", Path: "/api/analytics/dashboards", ID: "createDashboard", Tag: tagAnalytics,
			Summary: "Create a dashboard",
			Handler: s.createDashboard, Body: DashboardBody{}, Returns: analytics.Dashboard{}, Status: http.StatusCreated, Enabled: haveAnalytics, Action: authz.AnalyticsWrite,
			Tool: &Tool{Name: "create_dashboard", Doc: "Create a dashboard: its name, filters and panels, each a query naming the catalog's grains, dimensions and measures (get_analytics_catalog).", Args: map[string]string{}}},
		{Method: "GET", Path: "/api/analytics/dashboards/{id}", ID: "getDashboard", Tag: tagAnalytics,
			Summary: "One dashboard", Handler: s.getDashboard, Returns: analytics.Dashboard{}, Enabled: haveAnalytics, Scope: ScopeDashboard, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "get_dashboard", Doc: "One dashboard: its filters and panels, each a query over the analytics catalog.", Args: map[string]string{"id": "The dashboard's id, from list_dashboards."}}},
		{Method: "PUT", Path: "/api/analytics/dashboards/{id}", ID: "updateDashboard", Tag: tagAnalytics,
			Summary: "Replace a dashboard's name, filters and panels (409 on a built-in)",
			Handler: s.updateDashboard, Body: DashboardBody{}, Returns: analytics.Dashboard{}, Enabled: haveAnalytics, Scope: ScopeDashboard, Action: authz.AnalyticsWrite,
			Tool: &Tool{Name: "update_dashboard", Doc: "Replace one's dashboard's name, filters and panels. A built-in one is refused.", Args: map[string]string{"id": "The dashboard's id, from list_dashboards."}}},
		{Method: "DELETE", Path: "/api/analytics/dashboards/{id}", ID: "deleteDashboard", Tag: tagAnalytics,
			Summary: "Delete a dashboard (409 on a built-in)",
			Handler: s.deleteDashboard, Status: http.StatusNoContent, Enabled: haveAnalytics, Scope: ScopeDashboard, Action: authz.AnalyticsWrite},
		{Method: "GET", Path: "/api/analytics/dashboards/{id}/versions", ID: "listDashboardVersions", Tag: tagAnalytics,
			Summary: "A dashboard's kept versions, newest first (a built-in has none)",
			Doc:     "The newest 100 saves that changed its name, filters or panels, each with what changed against the one before it.",
			Handler: s.listDashboardVersions, Returns: []DashboardVersionSummary{}, Enabled: haveAnalytics, Scope: ScopeDashboard, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "list_dashboard_versions", Doc: "A dashboard's kept versions, newest first: who saved each, when, and what changed (name, filters, panels added, removed or changed). A built-in one has none.", Args: map[string]string{"id": argDashboard}}},
		{Method: "GET", Path: "/api/analytics/dashboards/{id}/versions/{seq}", ID: "getDashboardVersion", Tag: tagAnalytics,
			Summary: "One kept version of a dashboard",
			Handler: s.getDashboardVersion, Returns: DashboardVersionView{}, Enabled: haveAnalytics, Scope: ScopeDashboard, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "get_dashboard_version", Doc: "One kept version of a dashboard: its name, filters and panels as that save left them.", Args: map[string]string{"id": argDashboard, "seq": argDashboardVersion}}},
		{Method: "POST", Path: "/api/analytics/dashboards/{id}/versions/{seq}/restore", ID: "restoreDashboardVersion", Tag: tagAnalytics,
			Summary: "Restore a dashboard's kept version (409 on a built-in, 400 when the catalog no longer allows it)",
			Doc:     "Saves the version's name, filters and panels as the current ones: a new version; history is never rewritten.",
			Handler: s.restoreDashboardVersion, Returns: analytics.Dashboard{}, Enabled: haveAnalytics, Scope: ScopeDashboard, Action: authz.AnalyticsWrite,
			Tool: &Tool{Name: "restore_dashboard_version", Doc: "Restore one's dashboard to a kept version: its name, filters and panels become current again, as a new version. A built-in one is refused.", Args: map[string]string{"id": argDashboard, "seq": argDashboardVersion}}},
		{Method: "GET", Path: "/api/analytics/scorecards", ID: "listScorecards", Tag: tagAnalytics,
			Summary: "The built-in scorecards, then one's own",
			Handler: s.listScorecards, Returns: []analytics.Scorecard{}, Enabled: haveAnalytics, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "list_scorecards", Doc: "The built-in scorecards, then one's own: their names and ids.", Args: map[string]string{}}},
		{Method: "POST", Path: "/api/analytics/scorecards", ID: "createScorecard", Tag: tagAnalytics,
			Summary: "Create a scorecard",
			Handler: s.createScorecard, Body: ScorecardBody{}, Returns: analytics.Scorecard{}, Status: http.StatusCreated, Enabled: haveAnalytics, Action: authz.AnalyticsWrite,
			Tool: &Tool{Name: "create_scorecard", Doc: "Create a scorecard: its name and terms over the analytics catalog's measures (get_analytics_catalog).", Args: map[string]string{}}},
		{Method: "GET", Path: "/api/analytics/scorecards/{id}", ID: "getScorecard", Tag: tagAnalytics,
			Summary: "One scorecard", Handler: s.getScorecard, Returns: analytics.Scorecard{}, Enabled: haveAnalytics, Scope: ScopeScorecard, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "get_scorecard", Doc: "One scorecard: its terms, each a measure with its weight and target.", Args: map[string]string{"id": "The scorecard's id, from list_scorecards."}}},
		{Method: "PUT", Path: "/api/analytics/scorecards/{id}", ID: "updateScorecard", Tag: tagAnalytics,
			Summary: "Replace a scorecard's name and terms (409 on a built-in)",
			Handler: s.updateScorecard, Body: ScorecardBody{}, Returns: analytics.Scorecard{}, Enabled: haveAnalytics, Scope: ScopeScorecard, Action: authz.AnalyticsWrite,
			Tool: &Tool{Name: "update_scorecard", Doc: "Replace one's scorecard's name and terms. A built-in one is refused.", Args: map[string]string{"id": "The scorecard's id, from list_scorecards."}}},
		{Method: "DELETE", Path: "/api/analytics/scorecards/{id}", ID: "deleteScorecard", Tag: tagAnalytics,
			Summary: "Delete a scorecard (409 on a built-in)",
			Handler: s.deleteScorecard, Status: http.StatusNoContent, Enabled: haveAnalytics, Scope: ScopeScorecard, Action: authz.AnalyticsWrite},
		{Method: "GET", Path: "/api/analytics/scorecards/{id}/versions", ID: "listScorecardVersions", Tag: tagAnalytics,
			Summary: "A scorecard's kept versions, newest first (a built-in has none)",
			Doc:     "The newest 100 saves that changed its name or terms, each with what changed against the one before it.",
			Handler: s.listScorecardVersions, Returns: []ScorecardVersionSummary{}, Enabled: haveAnalytics, Scope: ScopeScorecard, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "list_scorecard_versions", Doc: "A scorecard's kept versions, newest first: who saved each, when, and what changed (name, terms). A built-in one has none.", Args: map[string]string{"id": argScorecard}}},
		{Method: "GET", Path: "/api/analytics/scorecards/{id}/versions/{seq}", ID: "getScorecardVersion", Tag: tagAnalytics,
			Summary: "One kept version of a scorecard",
			Handler: s.getScorecardVersion, Returns: ScorecardVersionView{}, Enabled: haveAnalytics, Scope: ScopeScorecard, Action: authz.AnalyticsRead,
			Tool: &Tool{Name: "get_scorecard_version", Doc: "One kept version of a scorecard: its name and terms as that save left them.", Args: map[string]string{"id": argScorecard, "seq": argScorecardVersion}}},
		{Method: "POST", Path: "/api/analytics/scorecards/{id}/versions/{seq}/restore", ID: "restoreScorecardVersion", Tag: tagAnalytics,
			Summary: "Restore a scorecard's kept version (409 on a built-in, 400 when the catalog no longer allows it)",
			Doc:     "Saves the version's name and terms as the current ones: a new version; history is never rewritten.",
			Handler: s.restoreScorecardVersion, Returns: analytics.Scorecard{}, Enabled: haveAnalytics, Scope: ScopeScorecard, Action: authz.AnalyticsWrite,
			Tool: &Tool{Name: "restore_scorecard_version", Doc: "Restore one's scorecard to a kept version: its name and terms become current again, as a new version. A built-in one is refused.", Args: map[string]string{"id": argScorecard, "seq": argScorecardVersion}}},
	}
}
