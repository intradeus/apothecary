// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4).
// Ports are shaped by what the engine needs, never by a vendor's API; no
// vendor types cross this boundary. The fakes in internal/adapters/fake are
// the test substrate for every §16 scenario.
package ports

import (
	"context"
	"io"
	"net"
	"strings"
	"time"

	"apothecary/internal/ports/envport"
)

// EgressDenial is one blocked host as the run page consumes it (the gateway
// tracks these per run; the engine surfaces them at GET /runs/{id}/egress-denials).
type EgressDenial struct {
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
	// Status is the host's rule now (EgressPolicy.Status): held until someone
	// allows or blocks it — only held hosts ask for review.
	Status EgressStatus `json:"status"`
}

// EgressStatus is a host's current rule, as a reviewer sees it.
type EgressStatus string

const (
	EgressAllowed EgressStatus = "allowed"
	// EgressHeld is denied by default (allowlist miss) and not yet reviewed:
	// refused for now, still asking for a verdict.
	EgressHeld EgressStatus = "held"
	// EgressBlocked is denied by a rule someone wrote (force-block, or a
	// denylist entry): reviewed, never asks again.
	EgressBlocked EgressStatus = "blocked"
)

// EgressAuditRecord is one aggregated egress fact for a run (a (host, port,
// verdict) tuple) with counts, byte totals and first/last timestamps.
type EgressAuditRecord struct {
	Host     string    `json:"host"`
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`
	Verdict  string    `json:"verdict"`
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// EgressAuditRow is a durable audit record (an EgressAuditRecord tagged with its
// project + engine run id) as stored and queried in the audit trail.
type EgressAuditRow struct {
	Project  string    `json:"project"`
	Run      string    `json:"run"`
	Host     string    `json:"host"`
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`
	Verdict  string    `json:"verdict"`
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first_seen"`
	Last     time.Time `json:"last_seen"`
}

// EgressAuditFilter narrows an audit query; empty fields are ignored.
type EgressAuditFilter struct {
	Run     string
	Project string
	Verdict string
	Host    string
	Limit   int
}

// EgressAuditor is an OPTIONAL EnvProvisioner capability: the aggregated egress
// audit trail for an environment (every host the workspace reached or was
// blocked from, with counts + bytes + timestamps), read from the per-project
// gateway. The engine ingests these into durable storage so the trail survives
// the ephemeral gateway.
type EgressAuditor interface {
	EgressAudit(ctx context.Context, env envport.EnvID) []EgressAuditRecord
}

// EgressPolicy is the firewall policy the gateway enforces RIGHT NOW (read
// back from the hot-reloaded policy file). The UI renders a host's current
// rule from it — as opposed to the audit trail, whose verdicts are records
// of past traffic and never change when the rule does.
type EgressPolicy struct {
	Mode           string   // "allow" (deny-all + allowlist) or "deny" (allow-all + denylist)
	Domains        []string // allowlist (mode=allow) or denylist (mode=deny); exact or "*.suffix"
	BlockedDomains []string // force-deny overrides; win in both modes
}

// Status classifies host under the policy, mirroring the gateway's verdict
// (workspace/gateway policy.verdict): a force-block wins in both modes, then
// mode + list membership decide. The only denial nobody wrote a rule for is an
// allowlist miss — that one is held.
func (p EgressPolicy) Status(host string) EgressStatus {
	if egressMatches(host, p.BlockedDomains) {
		return EgressBlocked
	}
	m := egressMatches(host, p.Domains)
	switch {
	case p.Mode == "deny" && m:
		return EgressBlocked
	case p.Mode == "deny" || m:
		return EgressAllowed
	}
	return EgressHeld
}

// egressMatches mirrors the gateway's hostMatches: an exact entry, or a
// "*.suffix" wildcard matched by suffix (never the bare apex itself).
func egressMatches(host string, list []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if rest, ok := strings.CutPrefix(d, "*."); ok {
			if strings.HasSuffix(h, "."+rest) {
				return true
			}
		} else if h == d {
			return true
		}
	}
	return false
}

// EgressController is an OPTIONAL EnvProvisioner capability: inspect and live-
// edit a run's egress policy, backed by the per-project egress gateway
// (internal/egressgw). AllowEgress is the "add to egress" fast path — it rewrites
// the project policy file and the gateway hot-reloads it, so the next request
// succeeds with no restart. Because the policy is per-project, an allow applies
// to every run of the project; denials stay per-run. Provisioners without a
// gateway simply don't implement it and the engine degrades honestly.
type EgressController interface {
	EgressDenials(ctx context.Context, env envport.EnvID) []EgressDenial
	AllowEgress(ctx context.Context, env envport.EnvID, host string) error
	// SetEgressRule live-toggles a host's rule: allow=true permits it, allow=false
	// force-blocks it (overrides base allowlist defaults). Project-wide, no restart.
	SetEgressRule(ctx context.Context, env envport.EnvID, host string, allow bool) error
	// EgressPolicy reads back the policy currently enforced for the env's
	// project — the source of truth for "what happens to this host now".
	EgressPolicy(ctx context.Context, env envport.EnvID) (EgressPolicy, error)
}

// Browser is the project browser (internal/browser): one container per
// project, a Chromium per agent session, driven through Playwright's MCP
// server — what a flow's builtin `browser` MCP server is. The browser has no
// network of its own: it reaches the run's services at localhost, through
// Dial, and nothing else.
type Browser interface {
	// Session serves one MCP session on stdio (newline-delimited JSON-RPC,
	// as an MCP client speaks to a stdio server) until either side closes,
	// bringing the project's browser up first. One session is one Chromium.
	Session(ctx context.Context, s BrowserSession, stdio io.ReadWriter) error
	// Warm brings the project's browser up (building its image on first
	// use) in the background, so a session does not wait on it. project is
	// the project's id (what names the container, via ProjectHostKey);
	// projectName is a docker label only.
	Warm(project, projectName string)
	// Status is the project's browser as the settings page shows it, by
	// project id.
	Status(ctx context.Context, project string) (BrowserStatus, error)
}

// BrowserSession is who a browser session is for.
type BrowserSession struct {
	// Project is the project's id: what names its container (ProjectHostKey).
	Project string
	// ProjectName is the human name, a docker label on the container only.
	ProjectName string
	Run         string
	// Dial reaches a TCP port the run's workspace serves: where the
	// browser's http://localhost:<port> goes.
	Dial func(ctx context.Context, port int) (net.Conn, error)
}

// BrowserStatus is a project browser's state.
type BrowserStatus struct {
	Container string `json:"container"`
	// State is the container's: absent (never started, or removed),
	// running, or stopped (idle — the next session starts it again).
	State string `json:"state"`
	// Image is what the container runs; Current what this build would run
	// — they differ until the browser is next idle and gets recreated.
	Image   string `json:"image,omitempty"`
	Current string `json:"current,omitempty"`
	// Sessions are the agent sessions using it now, each its own Chromium.
	Sessions int `json:"sessions"`
}

// Notification is the one-way fan-out payload; the CPUI inbox is canonical,
// channels are best-effort mirrors.
type Notification struct {
	// UserID is whose channels the notification goes to: the run's owner.
	// Empty is an instance-wide notice (every channel).
	UserID string
	RunID  string
	PingID string
	Type   string // ping type
	Title  string
	Body   string
	URL    string // deep link into the CPUI
}

type NotificationChannel interface {
	// Send errors are logged, never fail the run.
	Send(ctx context.Context, n Notification) error
}
