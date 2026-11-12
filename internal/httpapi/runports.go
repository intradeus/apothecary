package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/portrelay"
	"apothecary/internal/workspace/runports"
)

// A run's workspace ports: exposing one, the in-browser editor, the ports
// the workspace reports listening, and its resource samples.

// postExposePort publishes a workspace port and returns its browser URL
// (the run-page "open the app" path). Idempotent per (run, port).
func (s *Server) postExposePort(w http.ResponseWriter, r *http.Request) {
	var body ExposePortRequest
	if !readJSON(w, r, &body) {
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}
	url, err := s.Ports.Expose(r.Context(), r.PathValue("id"), body.Port)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, runports.ErrPortsOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ExposedPort{Port: body.Port, URL: s.reach(r, url)})
}

// reach is a relay URL as the requesting browser opens it (portrelay
// ForHost): an engine bound to every interface hands out the host the UI
// was reached at, not "localhost".
func (s *Server) reach(r *http.Request, url string) string {
	if url == "" || s.Ports == nil || s.Ports.Relay == nil {
		return url
	}
	return s.Ports.Relay.ForHost(url, r.Host)
}

// postOpenEditor lazily starts code-server in the run's workspace and returns
// its browser URL (the run-page "Open editor" path). Blocks while code-server
// boots on first open; idempotent thereafter.
func (s *Server) postOpenEditor(w http.ResponseWriter, r *http.Request) {
	url, err := s.Ports.OpenEditor(r.Context(), r.PathValue("id"))
	if err != nil {
		msg := err.Error()
		status := http.StatusConflict
		switch {
		case errors.Is(err, runports.ErrPortsOff), strings.Contains(msg, "does not support"):
			status = http.StatusServiceUnavailable
		case strings.Contains(msg, "not found"):
			status = http.StatusNotFound
		}
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, EditorURL{URL: s.reach(r, url)})
}

// postObservedPorts receives the in-workspace shim scanner's listener
// snapshot (run-token auth, like the ping endpoints — which also restricts
// it to live runs). 204 on accept.
func (s *Server) postObservedPorts(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !s.authPing(w, r, runID) {
		return
	}
	var body ObservedPortsRequest
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Ports) > 128 {
		writeErr(w, http.StatusBadRequest, "too many ports (max 128)")
		return
	}
	clean := make([]envport.ObservedPort, 0, len(body.Ports))
	for _, p := range body.Ports {
		if p.Port < 1 || p.Port > 65535 {
			continue
		}
		p.Process, p.Service = truncate(p.Process, 64), truncate(p.Service, 64)
		clean = append(clean, p)
	}
	s.Ports.ReportObservedPorts(runID, clean)
	w.WriteHeader(http.StatusNoContent)
}

// getRunPorts lists the run's LIVE exposures plus not-yet-exposed discovered
// listeners — the registry, not events, is authoritative (exposures are
// engine-process-scoped).
func (s *Server) getRunPorts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exposed := s.Ports.List(id)
	if exposed == nil {
		exposed = []portrelay.Exposure{}
	}
	live := map[int]bool{}
	for i, e := range exposed {
		live[e.Port] = true
		exposed[i].URL = s.reach(r, e.URL)
	}
	observed := []envport.ObservedPort{}
	for _, p := range s.Ports.ObservedPorts(id) {
		if !live[p.Port] { // an exposed port graduates out of the discovered list
			observed = append(observed, p)
		}
	}
	writeJSON(w, http.StatusOK, RunPorts{Ports: exposed, Observed: observed, Links: s.Ports.Enabled()})
}

// getRunResources serves the run's rolling window of resource samples
// (cpu/mem/net) for the run-page graphs — engine memory, like ports:
// the sampler, not events, is authoritative. Empty when nothing was sampled
// yet (no env, a provisioner without the capability, or an engine restart).
func (s *Server) getRunResources(w http.ResponseWriter, r *http.Request) {
	samples := s.Resources.Samples(r.PathValue("id"))
	if samples == nil {
		samples = []envport.ResourceSample{}
	}
	writeJSON(w, http.StatusOK, RunResources{Samples: samples})
}
