package egressgw

import (
	"context"
	"os"
	"strings"
	"testing"

	"apothecary/internal/egressgw/policyfile"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Acme Corp":  "acme-corp",
		"a/b:c":      "a-b-c",
		"":           "default",
		"--weird--":  "weird",
		"UPPER_case": "upper-case",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWritePolicyAndAllow(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, nil)

	if err := m.WritePolicy("proj", policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"github.com"}}); err != nil {
		t.Fatal(err)
	}
	// Allow (allow mode) appends.
	if err := m.Allow("proj", "example.com"); err != nil {
		t.Fatal(err)
	}
	var f policyfile.File
	if err := policyfile.Read(m.policyPath("proj"), &f); err != nil {
		t.Fatal(err)
	}
	if !contains(f.Domains, "github.com") || !contains(f.Domains, "example.com") {
		t.Fatalf("allow-mode domains = %v", f.Domains)
	}

	// Allow (deny mode) removes.
	_ = m.WritePolicy("d", policyfile.File{Mode: policyfile.ModeDeny, Domains: []string{"bad.com", "worse.com"}})
	if err := m.Allow("d", "bad.com"); err != nil {
		t.Fatal(err)
	}
	var g policyfile.File
	_ = policyfile.Read(m.policyPath("d"), &g)
	if contains(g.Domains, "bad.com") || !contains(g.Domains, "worse.com") {
		t.Fatalf("deny-mode domains = %v, want bad.com removed", g.Domains)
	}
}

func TestAuditReadsFile(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, nil)
	_ = os.MkdirAll(m.gwDir("proj"), 0o755)
	_ = policyfile.WriteAtomic(m.auditPath("proj"), policyfile.AuditFile{Records: []policyfile.AuditRecord{
		{Run: "run1", Host: "github.com", Port: 443, Verdict: "allow", Count: 3},
		{Run: "run2", Host: "x.com", Port: 443, Verdict: "deny", Count: 1},
	}})
	got := m.Audit("proj")
	if len(got) != 2 {
		t.Fatalf("audit records = %d, want 2", len(got))
	}
}

func TestDenialsFilterByRun(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, nil)
	_ = m.WritePolicy("proj", policyfile.File{Mode: policyfile.ModeAllow})
	_ = policyfile.WriteAtomic(m.denialsPath("proj"), policyfile.DenialsFile{Denials: []policyfile.Denial{
		{Run: "run1", Host: "a.com", Count: 2},
		{Run: "run2", Host: "b.com", Count: 1},
	}})
	got := m.Denials("proj", "run1")
	if len(got) != 1 || got[0].Host != "a.com" {
		t.Fatalf("Denials(run1) = %v", got)
	}
}

// fakeDocker dispatches on the first two args and records calls.
type fakeDocker struct {
	calls      [][]string
	gatewayHas bool // toggled true after the gateway "run"
}

func (f *fakeDocker) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args[:min2(2, len(args))], " ")
	switch {
	case key == "ps -aq" || (len(args) > 0 && args[0] == "ps"):
		if f.gatewayHas {
			return []byte("gwid\n"), nil, 0, nil
		}
		return nil, nil, 0, nil // gateway missing
	case key == "run -d":
		f.gatewayHas = true
		return []byte("cid\n"), nil, 0, nil
	case key == "network inspect":
		return []byte("172.20.0.0/16\n"), nil, 0, nil
	case args[0] == "inspect":
		return []byte("172.20.0.2\n"), nil, 0, nil
	default:
		return nil, nil, 0, nil
	}
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestEnsureRunEgressSharesGatewayPerProject(t *testing.T) {
	dir := t.TempDir()
	fd := &fakeDocker{}
	img := func(context.Context) (string, error) { return "apothecary/egress-gw:test", nil }
	m := NewManager(dir, fd.run, img)

	pol := policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"github.com"}}
	gw1, err := m.EnsureRunEgress(context.Background(), "acme", "run1", pol)
	if err != nil {
		t.Fatal(err)
	}
	gw2, err := m.EnsureRunEgress(context.Background(), "acme", "run2", pol)
	if err != nil {
		t.Fatal(err)
	}
	if gw1 != "172.20.0.2" || gw2 != "172.20.0.2" {
		t.Errorf("gwIPs = %q, %q", gw1, gw2)
	}

	var runs, starts, connects int
	for _, c := range fd.calls {
		switch {
		case c[0] == "run" && c[1] == "-d":
			runs++
		case c[0] == "start":
			starts++
		case c[0] == "network" && c[1] == "connect":
			connects++
		}
	}
	// ONE gateway created for the project; the second run starts it (idempotent);
	// both run networks connect to it.
	if runs != 1 {
		t.Errorf("gateway run count = %d, want 1 (shared per project)", runs)
	}
	if starts != 1 {
		t.Errorf("gateway start count = %d, want 1 (second run reuses it)", starts)
	}
	if connects != 2 {
		t.Errorf("network connect count = %d, want 2", connects)
	}
}
