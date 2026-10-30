package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"apothecary/internal/builtin"
	"apothecary/internal/egress"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/run"
)

// A run's egress: the defaults, what the gateway blocked, the policy in
// force, the audit trail, and a person allowing or ruling on a host.

// getEgressDefaults serves the compiled-in "sensible defaults" allowlist the
// project form offers via its "Add sensible defaults" button. Static, no auth —
// it's the same list baked into the binary. The form copies what the operator
// keeps into the project's extra_domains (they are no longer force-seeded).
func (s *Server) getEgressDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, EgressDefaults{Domains: builtin.EgressDefaults()})
}

// getEgressDenials lists hosts the per-project gateway blocked for this run:
// the visible diagnosis for "the firewall ate my model provider". Empty list
// when the run has no gateway or nothing was blocked.
func (s *Server) getEgressDenials(w http.ResponseWriter, r *http.Request) {
	dens := s.Egress.Denials(r.Context(), r.PathValue("id"))
	if dens == nil {
		dens = []egressport.EgressDenial{}
	}
	writeJSON(w, http.StatusOK, dens)
}

// getRunEgressPolicy serves the egress policy the gateway enforces for a run
// right now — the "current rule" side of the firewall UI. The audit rows only
// carry historical verdicts, so the rule toggles render from this instead.
func (s *Server) getRunEgressPolicy(w http.ResponseWriter, r *http.Request) {
	pol, err := s.Egress.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, egress.ErrOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	if pol.Domains == nil {
		pol.Domains = []string{}
	}
	if pol.BlockedDomains == nil {
		pol.BlockedDomains = []string{}
	}
	writeJSON(w, http.StatusOK, EgressPolicy{
		Mode: pol.Mode, Domains: pol.Domains, BlockedDomains: pol.BlockedDomains,
	})
}

// getRunEgressAudit serves the durable egress audit trail for a run — every
// external host it reached or was blocked from, with counts, byte totals and
// timestamps. Filterable via ?verdict=allow|deny, ?host=<substring>, ?limit=N.
func (s *Server) getRunEgressAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Egress.RunAudit(r.Context(), r.PathValue("id"),
		r.URL.Query().Get("verdict"), r.URL.Query().Get("host"), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if rows == nil {
		rows = []egressport.EgressAuditRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// getProjectEgressAudit serves the durable egress audit trail across all of a
// project's runs. Same filters as the run-level endpoint.
func (s *Server) getProjectEgressAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Egress.ProjectAudit(r.Context(), r.PathValue("id"),
		r.URL.Query().Get("verdict"), r.URL.Query().Get("host"), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if rows == nil {
		rows = []egressport.EgressAuditRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// validEgressHost accepts a hostname or `*.suffix` wildcard — no scheme,
// port or path (the proxy matches host only).
func validEgressHost(h string) bool {
	if h == "" || len(h) > 253 || strings.ContainsAny(h, " /:@") {
		return false
	}
	body := strings.TrimPrefix(h, "*.")
	if body == "" || !strings.Contains(body, ".") {
		return false
	}
	for _, r := range body {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// postEgressAllow adds a host to a run's live proxy allowlist AND persists
// it to the project's extra_domains (M18 "add to egress" button): the hung
// agent proceeds instantly, and future runs never hit the wall.
func (s *Server) postEgressAllow(w http.ResponseWriter, r *http.Request) {
	var body EgressHostRequest
	if !readJSON(w, r, &body) {
		return
	}
	host := strings.ToLower(strings.TrimSpace(body.Host))
	if !validEgressHost(host) {
		writeErr(w, http.StatusBadRequest, "host must be a bare hostname or *.wildcard (no scheme/port/path)")
		return
	}
	runID := r.PathValue("id")

	// Instant: unblock the live run.
	if err := s.Egress.Allow(r.Context(), runID, host); err != nil {
		status := http.StatusConflict
		if errors.Is(err, egress.ErrOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}

	// Persist to the project so future runs inherit it (best-effort — the
	// live unblock already succeeded).
	persisted := false
	if s.Conf != nil {
		if evs, err := s.Store.Events(r.Context(), runID, 0); err == nil && len(evs) > 0 {
			if pid := run.Reduce(evs).ProjectID; pid != "" {
				if p, err := s.Conf.GetProject(r.Context(), pid); err == nil {
					if !containsStr(p.Workspace.ExtraDomains, host) {
						p.Workspace.ExtraDomains = append(p.Workspace.ExtraDomains, host)
						if _, err := s.Conf.UpdateProject(r.Context(), pid, p); err == nil {
							persisted = true
						}
					} else {
						persisted = true
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, EgressAllowResult{Allowed: host, Persisted: persisted})
}

// postEgressRule live-toggles a host's egress rule for a run (allow or force-
// block) via the gateway, and persists the change to the project (allow →
// extra_domains, block → blocked_domains) so future runs inherit it.
func (s *Server) postEgressRule(w http.ResponseWriter, r *http.Request) {
	var body EgressRuleRequest
	if !readJSON(w, r, &body) {
		return
	}
	host := strings.ToLower(strings.TrimSpace(body.Host))
	if !validEgressHost(host) {
		writeErr(w, http.StatusBadRequest, "host must be a bare hostname or *.wildcard (no scheme/port/path)")
		return
	}
	runID := r.PathValue("id")

	if err := s.Egress.SetRule(r.Context(), runID, host, body.Allow); err != nil {
		status := http.StatusConflict
		if errors.Is(err, egress.ErrOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}

	// Persist to the project: keep the two lists mutually exclusive so future
	// runs inherit the same effective rule (best-effort — the live change won).
	persisted := false
	if s.Conf != nil {
		if evs, err := s.Store.Events(r.Context(), runID, 0); err == nil && len(evs) > 0 {
			if pid := run.Reduce(evs).ProjectID; pid != "" {
				if p, err := s.Conf.GetProject(r.Context(), pid); err == nil {
					if body.Allow {
						p.Workspace.BlockedDomains = removeStr(p.Workspace.BlockedDomains, host)
						if !containsStr(p.Workspace.ExtraDomains, host) {
							p.Workspace.ExtraDomains = append(p.Workspace.ExtraDomains, host)
						}
					} else {
						p.Workspace.ExtraDomains = removeStr(p.Workspace.ExtraDomains, host)
						if !containsStr(p.Workspace.BlockedDomains, host) {
							p.Workspace.BlockedDomains = append(p.Workspace.BlockedDomains, host)
						}
					}
					if _, err := s.Conf.UpdateProject(r.Context(), pid, p); err == nil {
						persisted = true
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, EgressRuleResult{Host: host, Allow: body.Allow, Persisted: persisted})
}

// egressRoutes is a run's and a project's egress.
func (s *Server) egressRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/egress-defaults", ID: "getEgressDefaults", Tag: tagEgress,
			Summary: "The built-in allowlist every project starts from",
			Handler: s.getEgressDefaults, Returns: EgressDefaults{}},
		{Method: "GET", Path: "/api/runs/{id}/egress-denials", ID: "listEgressDenials", Tag: tagEgress,
			Summary: "Hosts the gateway blocked for this run",
			Handler: s.getEgressDenials, Returns: []egressport.EgressDenial{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/egress-allow", ID: "allowEgressHost", Tag: tagEgress,
			Summary: "Unblock a host for a live run and persist it to the project",
			Handler: s.postEgressAllow, Body: EgressHostRequest{}, Returns: EgressAllowResult{}, Scope: ScopeRun},
		{Method: "POST", Path: "/api/runs/{id}/egress-rule", ID: "setEgressRule", Tag: tagEgress,
			Summary: "Allow or force-block a host for a run",
			Handler: s.postEgressRule, Body: EgressRuleRequest{}, Returns: EgressRuleResult{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/egress-audit", ID: "getRunEgressAudit", Tag: tagEgress,
			Summary: "Everything this run's workspace reached or was blocked from",
			Query:   auditQuery, Handler: s.getRunEgressAudit, Returns: []egressport.EgressAuditRow{}, Scope: ScopeRun},
		{Method: "GET", Path: "/api/runs/{id}/egress-policy", ID: "getRunEgressPolicy", Tag: tagEgress,
			Summary: "The firewall policy in force for a run right now",
			Handler: s.getRunEgressPolicy, Returns: EgressPolicy{}, Scope: ScopeRun},
	}
}
