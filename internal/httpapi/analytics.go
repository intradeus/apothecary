package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/account/auth"
	"apothecary/internal/analytics"
)

// Run analytics (TODO 23): queries over the caller's own runs, and the
// dashboards and scorecards they keep. Every query is the caller's — the
// service binds their id into the SQL — and the id'd routes are scoped by
// the guard (ScopeDashboard / ScopeScorecard); a built-in is everyone's.

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
	case errors.Is(err, analytics.ErrReadOnly):
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
	res, err := s.Analytics.Query(r.Context(), auth.UserID(r.Context()), q)
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
	vals, err := s.Analytics.Values(r.Context(), auth.UserID(r.Context()), body.Grain, body.Field, body.Time)
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
	runs, err := s.Analytics.Drill(r.Context(), auth.UserID(r.Context()), body.Grain, body.Filters, body.Time)
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

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var in analytics.DashboardInput
	if !readJSON(w, r, &in) {
		return
	}
	d, err := s.Analytics.CreateDashboard(r.Context(), auth.UserID(r.Context()), in)
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
	var in analytics.DashboardInput
	if !readJSON(w, r, &in) {
		return
	}
	d, err := s.Analytics.UpdateDashboard(r.Context(), r.PathValue("id"), in)
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
	var in analytics.ScorecardInput
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Analytics.CreateScorecard(r.Context(), auth.UserID(r.Context()), in)
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
	var in analytics.ScorecardInput
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Analytics.UpdateScorecard(r.Context(), r.PathValue("id"), in)
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

func (s *Server) analyticsRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/analytics/catalog", ID: "getAnalyticsCatalog", Tag: tagAnalytics,
			Summary: "Every grain, dimension and measure a query may name",
			Handler: s.getAnalyticsCatalog, Returns: analytics.Catalog{}, Enabled: haveAnalytics},
		{Method: "POST", Path: "/api/analytics/query", ID: "queryAnalytics", Tag: tagAnalytics,
			Summary: "Run a query over one's own runs",
			Doc: "A whitelisted shape — grain, measures, group-by, filters, time range — checked against the catalog. " +
				"At most 8 measures, 3 group-by dimensions, 10 filters and 500 rows; 422 when it runs longer than 5s.",
			Handler: s.queryAnalytics, Body: analytics.Query{}, Returns: analytics.Result{}, Enabled: haveAnalytics},
		{Method: "POST", Path: "/api/analytics/values", ID: "analyticsValues", Tag: tagAnalytics,
			Summary: "One dimension's distinct values (at most 200), labelled",
			Handler: s.analyticsValues, Body: ValuesRequest{}, Returns: []analytics.Value{}, Enabled: haveAnalytics},
		{Method: "POST", Path: "/api/analytics/drill", ID: "drillAnalytics", Tag: tagAnalytics,
			Summary: "The runs behind a group (at most 200, newest first)",
			Handler: s.drillAnalytics, Body: DrillRequest{}, Returns: []analytics.DrillRun{}, Enabled: haveAnalytics},
		{Method: "GET", Path: "/api/analytics/dashboards", ID: "listDashboards", Tag: tagAnalytics,
			Summary: "The built-in dashboards, then one's own",
			Handler: s.listDashboards, Returns: []analytics.Dashboard{}, Enabled: haveAnalytics},
		{Method: "POST", Path: "/api/analytics/dashboards", ID: "createDashboard", Tag: tagAnalytics,
			Summary: "Create a dashboard",
			Handler: s.createDashboard, Body: analytics.DashboardInput{}, Returns: analytics.Dashboard{}, Status: http.StatusCreated, Enabled: haveAnalytics},
		{Method: "GET", Path: "/api/analytics/dashboards/{id}", ID: "getDashboard", Tag: tagAnalytics,
			Summary: "One dashboard", Handler: s.getDashboard, Returns: analytics.Dashboard{}, Enabled: haveAnalytics, Scope: ScopeDashboard},
		{Method: "PUT", Path: "/api/analytics/dashboards/{id}", ID: "updateDashboard", Tag: tagAnalytics,
			Summary: "Replace a dashboard's name, filters and panels (409 on a built-in)",
			Handler: s.updateDashboard, Body: analytics.DashboardInput{}, Returns: analytics.Dashboard{}, Enabled: haveAnalytics, Scope: ScopeDashboard},
		{Method: "DELETE", Path: "/api/analytics/dashboards/{id}", ID: "deleteDashboard", Tag: tagAnalytics,
			Summary: "Delete a dashboard (409 on a built-in)",
			Handler: s.deleteDashboard, Status: http.StatusNoContent, Enabled: haveAnalytics, Scope: ScopeDashboard},
		{Method: "GET", Path: "/api/analytics/scorecards", ID: "listScorecards", Tag: tagAnalytics,
			Summary: "The built-in scorecards, then one's own",
			Handler: s.listScorecards, Returns: []analytics.Scorecard{}, Enabled: haveAnalytics},
		{Method: "POST", Path: "/api/analytics/scorecards", ID: "createScorecard", Tag: tagAnalytics,
			Summary: "Create a scorecard",
			Handler: s.createScorecard, Body: analytics.ScorecardInput{}, Returns: analytics.Scorecard{}, Status: http.StatusCreated, Enabled: haveAnalytics},
		{Method: "GET", Path: "/api/analytics/scorecards/{id}", ID: "getScorecard", Tag: tagAnalytics,
			Summary: "One scorecard", Handler: s.getScorecard, Returns: analytics.Scorecard{}, Enabled: haveAnalytics, Scope: ScopeScorecard},
		{Method: "PUT", Path: "/api/analytics/scorecards/{id}", ID: "updateScorecard", Tag: tagAnalytics,
			Summary: "Replace a scorecard's name and terms (409 on a built-in)",
			Handler: s.updateScorecard, Body: analytics.ScorecardInput{}, Returns: analytics.Scorecard{}, Enabled: haveAnalytics, Scope: ScopeScorecard},
		{Method: "DELETE", Path: "/api/analytics/scorecards/{id}", ID: "deleteScorecard", Tag: tagAnalytics,
			Summary: "Delete a scorecard (409 on a built-in)",
			Handler: s.deleteScorecard, Status: http.StatusNoContent, Enabled: haveAnalytics, Scope: ScopeScorecard},
	}
}
