package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"apothecary/internal/engine"
	"apothecary/internal/resolution"
	"apothecary/internal/wsinfo"
)

// The run page's Services pane: the project's services with their live state
// in the run's workspace, started and stopped by hand. Live state is read
// from the workspace on each GET (poll it); what happens to a service — a
// start, ready, a stop, a failure — also lands on the run's event stream.

func (s *Server) getRunServices(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.RunServices(r.Context(), r.PathValue("id"))
	if err != nil {
		serviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) postStartService(w http.ResponseWriter, r *http.Request) {
	s.serviceAction(w, r, func() (engine.ServiceInfo, error) {
		return s.Engine.StartService(r.Context(), r.PathValue("id"), r.PathValue("name"), false)
	})
}

func (s *Server) postRestartService(w http.ResponseWriter, r *http.Request) {
	s.serviceAction(w, r, func() (engine.ServiceInfo, error) {
		return s.Engine.StartService(r.Context(), r.PathValue("id"), r.PathValue("name"), true)
	})
}

func (s *Server) postStopService(w http.ResponseWriter, r *http.Request) {
	s.serviceAction(w, r, func() (engine.ServiceInfo, error) {
		return s.Engine.StopService(r.Context(), r.PathValue("id"), r.PathValue("name"))
	})
}

func (s *Server) serviceAction(w http.ResponseWriter, _ *http.Request, act func() (engine.ServiceInfo, error)) {
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
	out, err := s.Engine.ServiceLog(r.Context(), r.PathValue("id"), r.PathValue("name"), n)
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
	case errors.Is(err, engine.ErrNoSuchService), strings.Contains(err.Error(), "not found"):
		status = http.StatusNotFound
	case errors.Is(err, wsinfo.ErrNoWorkspace):
		status = http.StatusConflict
	case errors.Is(err, resolution.ErrUnresolvable):
		status = http.StatusConflict
	}
	writeErr(w, status, err.Error())
}
