package confstore

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// serviceNameRe is a service's name: what a flow's `service:` and
// run.service.<name> say, so the flow grammar's slug.
var serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Service health defaults (ServiceHealth.Timeout, .Interval).
const (
	DefaultServiceTimeout  = 5 * time.Minute
	DefaultServiceInterval = 10 * time.Second
)

// TimeoutOr / IntervalOr are the health durations with their defaults
// (validated on write, so a parse error cannot happen past it).
func (h ServiceHealth) TimeoutOr() time.Duration { return durationOr(h.Timeout, DefaultServiceTimeout) }
func (h ServiceHealth) IntervalOr() time.Duration {
	return durationOr(h.Interval, DefaultServiceInterval)
}

func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// validateServices checks the project's services: each one a flow can name
// and the workspace can run and probe.
func validateServices(svcs []ServiceConfig) error {
	seen := map[string]bool{}
	for i := range svcs {
		s := &svcs[i]
		s.Name = strings.TrimSpace(s.Name)
		at := s.Name
		if at == "" {
			at = "#" + strconv.Itoa(i+1)
		}
		if !serviceNameRe.MatchString(s.Name) {
			return invalidf("project: service %s: name must be a slug (lowercase letters, digits, - and _) — what a flow's service: names", at)
		}
		if seen[s.Name] {
			return invalidf("project: service %q is declared twice", s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Start) == "" {
			return invalidf("project: service %q: start is required (the script that runs it, in the checkout)", s.Name)
		}
		h := s.Health
		probes := 0
		for _, set := range []bool{h.HTTP != "", h.TCP != 0, strings.TrimSpace(h.Command) != ""} {
			if set {
				probes++
			}
		}
		if probes > 1 {
			return invalidf("project: service %q: health takes one probe — http, tcp or command", s.Name)
		}
		if h.HTTP != "" {
			u, err := url.Parse(h.HTTP)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return invalidf("project: service %q: health.http %q must be an http(s) URL, as reached in the workspace (http://localhost:3000/health)", s.Name, h.HTTP)
			}
		}
		if h.TCP != 0 && (h.TCP < 1 || h.TCP > 65535) {
			return invalidf("project: service %q: health.tcp %d must be a port (1-65535)", s.Name, h.TCP)
		}
		for _, d := range []struct{ field, v string }{{"timeout", h.Timeout}, {"interval", h.Interval}} {
			if d.v == "" {
				continue
			}
			if v, err := time.ParseDuration(d.v); err != nil || v <= 0 {
				return invalidf("project: service %q: health.%s %q must be a positive duration (30s, 5m)", s.Name, d.field, d.v)
			}
		}
		ports := map[int]bool{}
		for _, p := range s.Ports {
			if p.Port < 1 || p.Port > 65535 {
				return invalidf("project: service %q: port %d must be 1-65535", s.Name, p.Port)
			}
			if ports[p.Port] {
				return invalidf("project: service %q: port %d is listed twice", s.Name, p.Port)
			}
			ports[p.Port] = true
			if p.Path != "" && !strings.HasPrefix(p.Path, "/") {
				return invalidf("project: service %q: port %d: path %q must start with /", s.Name, p.Port, p.Path)
			}
		}
	}
	return nil
}
