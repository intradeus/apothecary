package resolution

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/flow"
)

// Service is one project service as a run uses it (confstore's
// ServiceConfig without the import, its durations defaulted).
type Service struct {
	Name        string
	Description string
	Start       string
	Stop        string
	Health      ServiceHealth
	Ports       []ServicePort
}

// ServiceHealth is the service's probe — at most one of HTTP, TCP, Command
// (none: up once started) — how long a start may take to pass it, and how
// often a running one is probed.
type ServiceHealth struct {
	HTTP     string
	TCP      int
	Command  string
	Timeout  time.Duration
	Interval time.Duration
}

type ServicePort struct {
	Port  int
	Label string
	Path  string
}

// Address is where the port answers in the workspace: what an agent opens.
func (p ServicePort) Address() string {
	path := p.Path
	if path == "" {
		path = "/"
	}
	return "http://localhost:" + strconv.Itoa(p.Port) + path
}

// Address is the service's own: its first port's.
func (s *Service) Address() string {
	if len(s.Ports) == 0 {
		return ""
	}
	return s.Ports[0].Address()
}

// Probe describes the service's health probe for people ("GET /health",
// "tcp 5432", "command: …"), or "" when it has none.
func (s *Service) Probe() string {
	switch {
	case s.Health.HTTP != "":
		return "GET " + s.Health.HTTP
	case s.Health.TCP != 0:
		return "tcp " + strconv.Itoa(s.Health.TCP)
	case s.Health.Command != "":
		return "command: " + s.Health.Command
	}
	return ""
}

// Service is the project's service named name, or nil.
func (r *Resolution) Service(name string) *Service {
	for i := range r.Services {
		if r.Services[i].Name == name {
			return &r.Services[i]
		}
	}
	return nil
}

// ServicesAvailable checks that every service the flow uses is one the
// project declares — at run creation, like run_refs: flows are portable,
// services the project's.
func (r *Resolution) ServicesAvailable(fl *flow.Flow) error {
	for _, name := range fl.Services() {
		if r.Service(name) != nil {
			continue
		}
		names := make([]string, 0, len(r.Services))
		for _, s := range r.Services {
			names = append(names, s.Name)
		}
		declared := "none — declare it in the project's workspace settings"
		if len(names) > 0 {
			declared = strings.Join(names, ", ")
		}
		return fmt.Errorf("service %q is not one of the project's services (%s)", name, declared)
	}
	return nil
}
