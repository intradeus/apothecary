// Package policyfile is the on-disk wire schema shared (by value, via JSON on a
// bind mount) between the engine-side egressgw.Manager and the in-container
// gateway daemon (workspace/gateway). This package is the CANONICAL definition;
// the gateway keeps a self-contained copy (it must build standalone, pure
// stdlib, like the shim) — the two are kept in lockstep by identical json tags
// and TestWireCompat in this package. Do not add non-stdlib deps here.
package policyfile

import (
	"encoding/json"
	"os"
	"time"
)

// Mode selects the firewall posture.
type Mode string

const (
	// ModeAllow is deny-all + allowlist (the secure default; == the pre-gateway
	// behaviour). Only hosts matching Domains egress.
	ModeAllow Mode = "allow"
	// ModeDeny is allow-all + denylist. Everything egresses except hosts
	// matching Domains (and IPs matching DeniedCIDRs for non-HTTP/TLS).
	ModeDeny Mode = "deny"
)

// File is the per-PROJECT policy at <RunsDir>/gw/<project>/egress-policy.json.
// The gateway watches it (mtime) and hot-swaps its in-memory policy on change.
type File struct {
	Mode Mode `json:"mode"`
	// Domains is the allow-list (ModeAllow) or deny-list (ModeDeny): exact hosts
	// or "*.suffix" wildcards, matched against TLS SNI / HTTP Host.
	Domains []string `json:"domains"`
	// DeniedCIDRs (ModeDeny) blocks non-HTTP/TLS egress to these IP ranges.
	DeniedCIDRs []string `json:"denied_cidrs,omitempty"`
	// AllowedCIDRs/AllowedPorts (ModeAllow) are the escape hatch for legitimate
	// raw external egress (e.g. an external managed database) that would
	// otherwise be dropped by the non-80/443 backstop.
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	AllowedPorts []int    `json:"allowed_ports,omitempty"`
}

// Normalize returns the effective mode, defaulting empty to ModeAllow.
func (f File) NormMode() Mode {
	if f.Mode == ModeDeny {
		return ModeDeny
	}
	return ModeAllow
}

// Denial is one blocked host, attributed to the run whose sandbox originated it.
type Denial struct {
	Run   string    `json:"run"`
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// DenialsFile is <RunsDir>/gw/<project>/egress-denials.json — the gateway writes
// it, the engine reads it (filtered per run) for GET /runs/{id}/egress-denials.
type DenialsFile struct {
	Denials []Denial `json:"denials"`
}

// Routes is <RunsDir>/gw/<project>/routes.json — the Manager maintains it as the
// gateway connects/disconnects run networks, so the gateway can attribute a
// denial (by source subnet) to the right run.
type Routes struct {
	RunSubnets map[string]string `json:"run_subnets"` // run id -> CIDR of apo-egress-<run>
}

// AuditRecord is one aggregated egress fact for the audit trail: a (run, host,
// port, verdict) tuple the gateway saw, with counts, byte totals and the first/
// last time it occurred. The engine ingests these into Postgres so the audit
// survives the (ephemeral, per-project) gateway.
type AuditRecord struct {
	Run      string    `json:"run"`
	Host     string    `json:"host"` // SNI / HTTP Host / DNS-resolved name / dst IP
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`   // tls | http | other
	Verdict  string    `json:"verdict"` // allow | deny
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// AuditFile is <RunsDir>/gw/<project>/egress-audit.json — the gateway writes it
// (aggregated), the engine ingests it.
type AuditFile struct {
	Records []AuditRecord `json:"records"`
}

// WriteAtomic marshals v and replaces path atomically (tmp + rename), so a
// concurrent reader (the gateway's mtime watcher) never sees a partial file.
func WriteAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read unmarshals path into v. A missing file is reported via os.IsNotExist.
func Read(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
