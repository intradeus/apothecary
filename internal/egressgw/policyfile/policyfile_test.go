package policyfile

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// TestWireShape pins the on-disk JSON field names. The gateway daemon
// (workspace/gateway/wire.go) keeps a self-contained copy of these structs
// because it must build standalone; if a tag changes here, update the gateway
// copy too — the live interop path depends on these names matching exactly.
func TestWireShape(t *testing.T) {
	b, _ := json.Marshal(File{
		Mode:         ModeDeny,
		Domains:      []string{"x.com"},
		DeniedCIDRs:  []string{"10.0.0.0/8"},
		AllowedCIDRs: []string{"1.2.3.0/24"},
		AllowedPorts: []int{5432},
	})
	for _, key := range []string{`"mode"`, `"domains"`, `"denied_cidrs"`, `"allowed_cidrs"`, `"allowed_ports"`} {
		if !contains(string(b), key) {
			t.Errorf("File JSON missing %s: %s", key, b)
		}
	}
	d, _ := json.Marshal(Denial{Run: "r", Host: "h", Count: 1, Last: time.Unix(0, 0).UTC()})
	for _, key := range []string{`"run"`, `"host"`, `"count"`, `"last"`} {
		if !contains(string(d), key) {
			t.Errorf("Denial JSON missing %s: %s", key, d)
		}
	}
	r, _ := json.Marshal(Routes{RunSubnets: map[string]string{"r": "172.20.0.0/16"}})
	if !contains(string(r), `"run_subnets"`) {
		t.Errorf("Routes JSON missing run_subnets: %s", r)
	}
}

func TestNormMode(t *testing.T) {
	if (File{}).NormMode() != ModeAllow {
		t.Error("empty mode must default to allow")
	}
	if (File{Mode: ModeDeny}).NormMode() != ModeDeny {
		t.Error("deny must round-trip")
	}
}

func TestWriteReadAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "egress-policy.json")
	want := File{Mode: ModeAllow, Domains: []string{"github.com"}}
	if err := WriteAtomic(p, want); err != nil {
		t.Fatal(err)
	}
	var got File
	if err := Read(p, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != want.Mode || len(got.Domains) != 1 || got.Domains[0] != "github.com" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
