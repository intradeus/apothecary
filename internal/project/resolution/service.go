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

// ServicePort is one port a service listens on. Protocol says what a person
// and an agent reach it with: "http" (the default, "" too) is a web app,
// opened in a browser through its preview link; "tcp" is anything else (a
// database, a cache), reached by forwarding it — never exposed, no link.
type ServicePort struct {
	Port     int
	Label    string
	Path     string
	Protocol string
}

// The protocols a service port declares.
const (
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
)

// Web says whether the port serves a web app: it gets a preview link.
func (p ServicePort) Web() bool { return p.Protocol != ProtocolTCP }

// Proto is the port's protocol, the default spelled out.
func (p ServicePort) Proto() string {
	if p.Web() {
		return ProtocolHTTP
	}
	return ProtocolTCP
}

// Name is how the port is listed: its label, else its number.
func (p ServicePort) Name() string {
	if p.Label != "" {
		return p.Label
	}
	return "port " + strconv.Itoa(p.Port)
}

// Address is where the port answers in the workspace: what an agent opens.
// A tcp port has no scheme or path: localhost:5432.
func (p ServicePort) Address() string {
	if !p.Web() {
		return "localhost:" + strconv.Itoa(p.Port)
	}
	path := p.Path
	if path == "" {
		path = "/"
	}
	return "http://localhost:" + strconv.Itoa(p.Port) + path
}

// Address is the service's own: its first port's (its service_ready event
// says it). A flow's run.service.<name> reads every port (Ports).
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
