package workspace

import (
	"apothecary/internal/ports/envport"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SampleResources implements envport.ResourceSampler: one `docker stats` reading
// of the run's DinD sandbox on the HOST daemon (hostRun — the sandbox contains
// the whole inner world: inner dockerd, workspace, compose siblings, builds).
// For a legacy pre-DinD env the same name resolves to the host-daemon
// workspace container, which is still the honest per-run footprint.
func (a *Adapter) SampleResources(ctx context.Context, env envport.EnvID) (envport.ResourceSample, error) {
	return a.host.sample(ctx, string(env))
}

// dockerSample is the docker host's SampleResources.
func (a *Adapter) dockerSample(ctx context.Context, name string) (envport.ResourceSample, error) {
	out, stderr, code, err := a.hostRun(ctx, []string{"stats", "--no-stream", "--format", "{{json .}}", name})
	if err != nil {
		return envport.ResourceSample{}, err
	}
	if code != 0 {
		return envport.ResourceSample{}, fmt.Errorf("docker stats %s: exit %d: %s", name, code, tail(string(stderr), 256))
	}
	var row struct {
		CPUPerc  string
		MemUsage string
		NetIO    string
		PIDs     string
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return envport.ResourceSample{}, fmt.Errorf("docker stats %s: unparseable row %q: %w", name, tail(line, 256), err)
	}
	// CPUPerc gates the sample: docker prints "--" for a container that exists
	// but isn't running, and "no sample" (a gap) is the honest answer then.
	cpu, err := parsePercent(row.CPUPerc)
	if err != nil {
		return envport.ResourceSample{}, fmt.Errorf("docker stats %s: %w", name, err)
	}
	s := envport.ResourceSample{CPUPerc: cpu}
	s.MemUsed, s.MemLimit = parseSizePair(row.MemUsage)
	s.NetRx, s.NetTx = parseSizePair(row.NetIO)
	s.PIDs, _ = strconv.Atoi(strings.TrimSpace(row.PIDs))
	s.CPUs = a.hostCPUs(ctx)
	return s, nil
}

// hostCPUs returns the host daemon's online CPU count — the CPUPerc
// denominator (docker normalizes to one core = 100%, so the ceiling is
// CPUs×100). Cached after the first success: it can't change under a running
// daemon. 0 = unknown (retried on the next sample).
func (a *Adapter) hostCPUs(ctx context.Context) int {
	a.cpusMu.Lock()
	n := a.cpus
	a.cpusMu.Unlock()
	if n > 0 {
		return n
	}
	out, _, code, err := a.hostRun(ctx, []string{"info", "--format", "{{.NCPU}}"})
	if err != nil || code != 0 {
		return 0
	}
	n, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n <= 0 {
		return 0
	}
	a.cpusMu.Lock()
	a.cpus = n
	a.cpusMu.Unlock()
	return n
}

func parsePercent(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0, fmt.Errorf("percent %q: %w", s, err)
	}
	return v, nil
}

// parseSizePair splits docker's "used / limit" (MemUsage) or "in / out"
// (NetIO, BlockIO) pairs. Best-effort: an unparseable side reads 0 — the
// sample's validity is gated on CPUPerc, not here.
func parseSizePair(s string) (int64, int64) {
	l, r, _ := strings.Cut(s, "/")
	lv, _ := parseSize(l)
	rv, _ := parseSize(r)
	return lv, rv
}

// sizeUnits covers both flavors docker stats prints: decimal (kB/MB/GB, used
// for net and block IO) and binary (KiB/MiB/GiB, used for memory).
var sizeUnits = map[string]float64{
	"B":  1,
	"KB": 1e3, "KIB": 1 << 10,
	"MB": 1e6, "MIB": 1 << 20,
	"GB": 1e9, "GIB": 1 << 30,
	"TB": 1e12, "TIB": 1 << 40,
	"PB": 1e15, "PIB": 1 << 50,
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, fmt.Errorf("size %q: no numeric prefix", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	mult, ok := sizeUnits[strings.ToUpper(strings.TrimSpace(s[i:]))]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit", s)
	}
	return int64(math.Round(n * mult)), nil
}
