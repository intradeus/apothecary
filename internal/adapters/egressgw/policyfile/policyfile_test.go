package policyfile

import (
	"encoding/json"
	"os"
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

// TestWireShapeControl pins the control-API and WireGuard additions, mirrored
// in workspace/gateway/wire.go.
func TestWireShapeControl(t *testing.T) {
	r, _ := json.Marshal(Routes{RunSubnets: map[string]string{"r": "10.90.0.2/32"},
		Peers: map[string]Peer{"r": {PublicKey: "k", IP: "10.90.0.2"}}})
	for _, key := range []string{`"run_subnets"`, `"peers"`, `"public_key"`, `"ip"`} {
		if !contains(string(r), key) {
			t.Errorf("Routes JSON missing %s: %s", key, r)
		}
	}
	rr, _ := json.Marshal(RunRoute{Subnet: "s", Peer: &Peer{}})
	for _, key := range []string{`"subnet"`, `"peer"`} {
		if !contains(string(rr), key) {
			t.Errorf("RunRoute JSON missing %s: %s", key, rr)
		}
	}
	b, _ := json.Marshal(BuildCacheInfo{Bytes: 1})
	if !contains(string(b), `"bytes"`) {
		t.Errorf("BuildCacheInfo JSON missing bytes: %s", b)
	}
}

func TestWriteAtomicFailures(t *testing.T) {
	dir := t.TempDir()
	// Unmarshalable values never touch the disk.
	p := filepath.Join(dir, "x.json")
	if err := WriteAtomic(p, make(chan int)); err == nil {
		t.Error("marshal failure not reported")
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Error("a temp file was left behind")
	}
	if err := WriteAtomic(filepath.Join(dir, "absent", "x.json"), File{}); err == nil {
		t.Error("write into a missing directory succeeded")
	}
	// The rename failing leaves the old policy in place: a directory at the
	// target refuses it.
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(target, File{}); err == nil {
		t.Error("rename onto a directory succeeded")
	}
}

// A replacement is whole: a reader never sees the temp file's half-way state
// because the path changes only at the rename.
func TestWriteAtomicReplaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.json")
	if err := WriteAtomic(p, File{Mode: ModeAllow}); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(p, File{Mode: ModeDeny, Domains: []string{"x.io"}}); err != nil {
		t.Fatal(err)
	}
	var f File
	if err := Read(p, &f); err != nil || f.Mode != ModeDeny || f.Domains[0] != "x.io" {
		t.Errorf("read = %+v, %v", f, err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file survived")
	}
	// The new bytes go to the side first: when that fails, the live policy
	// has not been touched.
	if err := os.Mkdir(p+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(p, File{Mode: ModeAllow}); err == nil {
		t.Fatal("a write with its temp path taken succeeded")
	}
	f = File{}
	if err := Read(p, &f); err != nil || f.Mode != ModeDeny {
		t.Errorf("after a failed write = %+v, %v", f, err)
	}
}

func TestReadFailures(t *testing.T) {
	dir := t.TempDir()
	var f File
	if err := Read(filepath.Join(dir, "absent"), &f); !os.IsNotExist(err) {
		t.Errorf("missing file = %v, want os.IsNotExist", err)
	}
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Read(p, &f); err == nil {
		t.Error("garbage read as a policy")
	}
}

// Anything but an explicit deny is the secure default: a typo'd mode must not
// open the firewall.
func TestNormModeUnknownIsAllowlist(t *testing.T) {
	for _, m := range []Mode{"", "allow", "DENY", "deny ", "open"} {
		if got := (File{Mode: m}).NormMode(); got != ModeAllow {
			t.Errorf("NormMode(%q) = %q", m, got)
		}
	}
}
