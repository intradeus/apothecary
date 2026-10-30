package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"apothecary/internal/resolution"
	"apothecary/internal/services"
	"apothecary/internal/wsinfo"
)

// The run page's Services pane: the project's services with their live state
// in the run's workspace, started and stopped by hand. Live state is read
// from the workspace on each GET (poll it); what happens to a service — a
// start, ready, a stop, a failure — also lands on the run's event stream.

func (s *Server) getRunServices(w http.ResponseWriter, r *http.Request) {
	out, err := s.Services.RunServices(r.Context(), r.PathValue("id"))
	if err != nil {
		serviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) postStartService(w http.ResponseWriter, r *http.Request) {
	s.serviceAction(w, r, func() (services.ServiceInfo, error) {
		return s.Services.StartService(r.Context(), r.PathValue("id"), r.PathValue("name"), false)
	})
}

func (s *Server) postRestartService(w http.ResponseWriter, r *http.Request) {
	s.serviceAction(w, r, func() (services.ServiceInfo, error) {
		return s.Services.StartService(r.Context(), r.PathValue("id"), r.PathValue("name"), true)
	})
}

func (s *Server) postStopService(w http.ResponseWriter, r *http.Request) {
	s.serviceAction(w, r, func() (services.ServiceInfo, error) {
		return s.Services.StopService(r.Context(), r.PathValue("id"), r.PathValue("name"))
	})
}

func (s *Server) serviceAction(w http.ResponseWriter, _ *http.Request, act func() (services.ServiceInfo, error)) {
	info, err := act()
	if err != nil {
		serviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, info)
}

func (s *Server) getServiceLogs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if n <= 0 || n > 5000 {
		n = 200
	}
	out, err := s.Services.ServiceLog(r.Context(), r.PathValue("id"), r.PathValue("name"), n)
	if err != nil {
		serviceErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(out))
}

func serviceErr(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway // the workspace did not answer as it should
	switch {
	case errors.Is(err, services.ErrNoSuchService), strings.Contains(err.Error(), "not found"):
		status = http.StatusNotFound
	case errors.Is(err, wsinfo.ErrNoWorkspace):
		status = http.StatusConflict
	case errors.Is(err, resolution.ErrUnresolvable):
		status = http.StatusConflict
	}
	writeErr(w, status, err.Error())
}

// serviceRoutes is the run page's project services.
func (s *Server) serviceRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/runs/{id}/services", ID: "getRunServices", Tag: tagWorkspace,
			Summary: "The project's services and how each stands in the run's workspace",
			Doc:     "Live state read from the workspace's supervisor on each call — poll it while the pane is open; service_started, service_ready, service_stopped and service_failed also arrive on the run's event stream. A healthy service's ports carry their preview links. workspace is false when the run has none; error says its services could not be read just now.",
			Handler: s.getRunServices, Returns: services.RunServices{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/services/{name}/start", ID: "startRunService", Tag: tagWorkspace,
			Summary: "Start a service by hand",
			Doc:     "Returns once the workspace runs it (already up: left alone); whether it comes up is its state on the services list, and service_ready or service_failed on the event stream. A service started by hand stays up until someone stops it or the workspace goes. 409 when the run has no workspace.",
			Handler: s.postStartService, Returns: services.ServiceInfo{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/services/{name}/restart", ID: "restartRunService", Tag: tagWorkspace,
			Summary: "Stop a service and start it again",
			Handler: s.postRestartService, Returns: services.ServiceInfo{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/services/{name}/stop", ID: "stopRunService", Tag: tagWorkspace,
			Summary: "Stop a service, whoever started it",
			Handler: s.postStopService, Returns: services.ServiceInfo{}, Status: http.StatusAccepted, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/services/{name}/logs", ID: "getRunServiceLogs", Tag: tagWorkspace,
			Summary: "The last lines of a service's output",
			Query:   []Param{{Name: "tail", Doc: "lines (default 200, at most 5000)", Type: "integer"}},
			Handler: s.getServiceLogs, Produces: "text/plain", Scope: ScopeRun},
	}
}
