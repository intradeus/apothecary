package main

// wire.go is a SELF-CONTAINED copy of internal/adapters/egressgw/policyfile — the gateway
// must build standalone (pure stdlib, like the shim), so it cannot import the
// engine-side package. The engine's TestWireCompat pins these two in lockstep;
// keep the json tags identical on both sides.

import (
	"encoding/json"
	"os"
	"time"
)

type Mode string

const (
	ModeAllow Mode = "allow" // deny-all + allowlist (default)
	ModeDeny  Mode = "deny"  // allow-all + denylist
)

type File struct {
	Mode           Mode     `json:"mode"`
	Domains        []string `json:"domains"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	DeniedCIDRs    []string `json:"denied_cidrs,omitempty"`
	AllowedCIDRs   []string `json:"allowed_cidrs,omitempty"`
	AllowedPorts   []int    `json:"allowed_ports,omitempty"`
}

func (f File) normMode() Mode {
	if f.Mode == ModeDeny {
		return ModeDeny
	}
	return ModeAllow
}

type Denial struct {
	Run   string    `json:"run"`
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

type DenialsFile struct {
	Denials []Denial `json:"denials"`
}

type Routes struct {
	RunSubnets map[string]string `json:"run_subnets"`
	Peers      map[string]Peer   `json:"peers,omitempty"`
}

type Peer struct {
	PublicKey string `json:"public_key"`
	IP        string `json:"ip"`
}

type RunRoute struct {
	Subnet string `json:"subnet"`
	Peer   *Peer  `json:"peer,omitempty"`
}

type BuildCacheInfo struct {
	Bytes int64 `json:"bytes"`
}

type AuditRecord struct {
	Run      string    `json:"run"`
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

type AuditFile struct {
	Records []AuditRecord `json:"records"`
}

func writeAtomic(path string, v any) error {
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

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
