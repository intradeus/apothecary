package egressgw

import (
	"context"
	"os"
	"slices"
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

func TestSetRuleToggles(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, nil)
	_ = m.WritePolicy("p", policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"github.com"}})

	// Block a base-allowed host → goes to BlockedDomains (wins), dropped from Domains.
	if err := m.SetRule("p", "github.com", false); err != nil {
		t.Fatal(err)
	}
	var f policyfile.File
	_ = policyfile.Read(m.policyPath("p"), &f)
	if !contains(f.BlockedDomains, "github.com") || contains(f.Domains, "github.com") {
		t.Fatalf("block: blocked=%v domains=%v", f.BlockedDomains, f.Domains)
	}
	// Allow it back → lifts the block, adds to the allowlist. Fresh var: Unmarshal
	// doesn't clear fields absent from the JSON (blocked_domains is now omitted).
	if err := m.SetRule("p", "github.com", true); err != nil {
		t.Fatal(err)
	}
	var g policyfile.File
	_ = policyfile.Read(m.policyPath("p"), &g)
	if contains(g.BlockedDomains, "github.com") || !contains(g.Domains, "github.com") {
		t.Fatalf("allow: blocked=%v domains=%v", g.BlockedDomains, g.Domains)
	}
}

// Policy reads back what the gateway enforces — the run page renders each
// host's CURRENT rule from it (audit rows only carry historical verdicts).
func TestPolicyReadsBack(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, nil)

	// No file yet: the default deny-all allowlist, never an error.
	f, err := m.Policy("p")
	if err != nil || f.Mode != policyfile.ModeAllow || len(f.Domains) != 0 {
		t.Fatalf("empty policy = %+v, %v", f, err)
	}

	_ = m.WritePolicy("p", policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"github.com"}})
	if err := m.SetRule("p", "github.com", false); err != nil {
		t.Fatal(err)
	}
	f, err = m.Policy("p")
	if err != nil || !contains(f.BlockedDomains, "github.com") || contains(f.Domains, "github.com") {
		t.Fatalf("after block: %+v, %v", f, err)
	}
	if err := m.SetRule("p", "github.com", true); err != nil {
		t.Fatal(err)
	}
	f, err = m.Policy("p")
	if err != nil || contains(f.BlockedDomains, "github.com") || !contains(f.Domains, "github.com") {
		t.Fatalf("after allow: %+v, %v", f, err)
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

// testGatewayImage is the tag the fake ensureImage builds; a fakeDocker whose
// existing gateway reports a DIFFERENT image is a staged upgrade.
const testGatewayImage = "apothecary/egress-gw:test"

// fakeDocker answers docker calls and records them. Zero value = a host with no
// gateway yet; the fields stage the states ensureGateway must distinguish.
type fakeDocker struct {
	calls      [][]string
	gatewayHas bool   // a gateway container exists (set by `run -d`, cleared by `rm`)
	gatewayImg string // its image ("" = testGatewayImage, i.e. up to date)
	running    string // running-sandbox probe output ("" = project idle)
	netIPs     string // per-network address inspect output
	failRun    bool   // `run -d` fails
}

func (f *fakeDocker) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	switch {
	// The running-sandbox probe (upgrade gate) — distinct from the gateway
	// name probe, which is also a `ps`.
	case args[0] == "ps" && strings.Contains(joined, "apothecary.sandbox=true"):
		return []byte(f.running), nil, 0, nil
	case args[0] == "ps":
		if f.gatewayHas {
			return []byte("gwid\n"), nil, 0, nil
		}
		return nil, nil, 0, nil // gateway missing
	case args[0] == "run" && args[1] == "-d":
		if f.failRun {
			return nil, []byte("boom"), 1, nil
		}
		f.gatewayHas = true
		return []byte("cid\n"), nil, 0, nil
	case args[0] == "rm":
		f.gatewayHas = false
		return nil, nil, 0, nil
	case args[0] == "network" && args[1] == "inspect":
		return []byte("172.20.0.0/16\n"), nil, 0, nil
	case args[0] == "inspect" && strings.Contains(joined, ".Config.Image"):
		if f.gatewayImg != "" {
			return []byte(f.gatewayImg + "\n"), nil, 0, nil
		}
		return []byte(testGatewayImage + "\n"), nil, 0, nil
	case args[0] == "inspect" && strings.Contains(joined, "$v.IPAddress"):
		return []byte(f.netIPs), nil, 0, nil
	case args[0] == "inspect":
		return []byte("172.20.0.2\n"), nil, 0, nil
	default:
		return nil, nil, 0, nil
	}
}

// count tallies recorded calls whose argv starts with the given words.
func (f *fakeDocker) count(prefix ...string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) < len(prefix) {
			continue
		}
		if slices.Equal(c[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

// findCall returns the first recorded call containing the substring.
func (f *fakeDocker) findCall(t *testing.T, substr string) string {
	t.Helper()
	for _, c := range f.calls {
		if joined := strings.Join(c, " "); strings.Contains(joined, substr) {
			return joined
		}
	}
	t.Fatalf("no docker call containing %q in %v", substr, f.calls)
	return ""
}

func testImageFn(context.Context) (string, error) { return testGatewayImage, nil }

func TestEnsureRunEgressSharesGatewayPerProject(t *testing.T) {
	dir := t.TempDir()
	fd := &fakeDocker{}
	img := testImageFn
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

	// The gateway also hosts the project's image mirror: kept warm across daemon
	// restarts, cache on a named volume, config from gwDir.
	run := fd.findCall(t, "run -d")
	for _, want := range []string{
		"--restart unless-stopped",
		"-v apo-gw-acme-cache:/var/lib/registry",
		"--mirror-config " + m.mirrorConfigPath("acme"),
	} {
		if !strings.Contains(run, want) {
			t.Errorf("gateway run args missing %q:\n%s", want, run)
		}
	}
	if n := fd.count("volume", "create"); n != 1 {
		t.Errorf("cache volume create count = %d, want 1 (idempotent, once)", n)
	}
	if got := m.mirrorConfigPath("acme"); !strings.Contains(fd.findCall(t, "run -d"), got) {
		t.Errorf("mirror config path %q not passed to the gateway", got)
	}
	if b, err := os.ReadFile(m.mirrorConfigPath("acme")); err != nil {
		t.Errorf("mirror config not written: %v", err)
	} else if !strings.Contains(string(b), "remoteurl: https://registry-1.docker.io") {
		t.Errorf("mirror config is not a pull-through proxy:\n%s", b)
	}
}

// A gateway on a stale image must be REPLACED: nothing reaps gateways anymore,
// so recreate-on-upgrade is the only path by which a new gateway build ships.
func TestEnsureGatewayRecreatesOnStaleImage(t *testing.T) {
	dir := t.TempDir()
	fd := &fakeDocker{
		gatewayHas: true,
		gatewayImg: "apothecary/egress-gw:old",
		netIPs:     "bridge 172.17.0.9\napo-egress-run0 172.20.0.2\n",
	}
	m := NewManager(dir, fd.run, testImageFn)

	if _, err := m.EnsureRunEgress(context.Background(), "acme", "run1", policyfile.File{}); err != nil {
		t.Fatal(err)
	}
	if n := fd.count("rm", "-f", m.gwName("acme")); n != 1 {
		t.Fatalf("stale gateway not removed (rm count %d)", n)
	}
	if n := fd.count("run", "-d"); n != 1 {
		t.Fatalf("replacement gateway not created (run -d count %d)", n)
	}
	if !strings.Contains(fd.findCall(t, "run -d"), testGatewayImage) {
		t.Error("replacement gateway does not use the current image")
	}
	if n := fd.count("start"); n != 0 {
		t.Errorf("stale gateway was started instead of replaced (start count %d)", n)
	}
	// The run the old gateway served must come back at the SAME address: its
	// sandbox baked --dns/--registry-mirror at that IP and `docker start` cannot
	// re-flag it. The bridge uplink is docker's to assign, not ours to restore.
	reattach := fd.findCall(t, "connect --ip")
	if !strings.Contains(reattach, "--ip 172.20.0.2 apo-egress-run0") {
		t.Errorf("run net not re-attached at its old IP: %s", reattach)
	}
	for _, c := range fd.calls {
		if joined := strings.Join(c, " "); strings.Contains(joined, "--ip 172.17.0.9") {
			t.Errorf("bridge uplink should not be re-attached by hand: %s", joined)
		}
	}
}

// Upgrading yanks egress from every attached run, so a project with a live
// sandbox keeps its old gateway until it goes idle.
func TestEnsureGatewayDefersUpgradeWhileRunning(t *testing.T) {
	dir := t.TempDir()
	fd := &fakeDocker{
		gatewayHas: true,
		gatewayImg: "apothecary/egress-gw:old",
		running:    "sbx1\n",
	}
	m := NewManager(dir, fd.run, testImageFn)

	if _, err := m.EnsureRunEgress(context.Background(), "acme", "run1", policyfile.File{}); err != nil {
		t.Fatal(err)
	}
	if n := fd.count("rm", "-f", m.gwName("acme")); n != 0 {
		t.Errorf("live project's gateway was removed mid-run (rm count %d)", n)
	}
	if n := fd.count("run", "-d"); n != 0 {
		t.Errorf("gateway recreated while a sandbox is running (run -d count %d)", n)
	}
	if n := fd.count("start"); n != 1 {
		t.Errorf("start count = %d, want 1 (old gateway kept)", n)
	}
}

// Destroying the project's last run must NOT reap the gateway: it holds the warm
// image cache, and its policy dir must survive for the project's next run.
func TestDestroyRunEgressKeepsGateway(t *testing.T) {
	dir := t.TempDir()
	fd := &fakeDocker{}
	m := NewManager(dir, fd.run, testImageFn)
	ctx := context.Background()

	if _, err := m.EnsureRunEgress(ctx, "acme", "run1", policyfile.File{Mode: policyfile.ModeAllow}); err != nil {
		t.Fatal(err)
	}
	before := len(fd.calls)
	if err := m.DestroyRunEgress(ctx, "acme", "run1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range fd.calls[before:] {
		if c[0] == "rm" {
			t.Errorf("gateway reaped on last run destroy: %v", c)
		}
		if c[0] == "volume" {
			t.Errorf("cache volume touched on destroy: %v", c)
		}
	}
	if _, err := os.Stat(m.gwDir("acme")); err != nil {
		t.Errorf("gwDir removed on destroy: %v", err)
	}
	if _, err := os.Stat(m.policyPath("acme")); err != nil {
		t.Errorf("policy file removed on destroy: %v", err)
	}
	// The run's own network and route entry DO go away.
	var r policyfile.Routes
	_ = policyfile.Read(m.routesPath("acme"), &r)
	if _, ok := r.RunSubnets["run1"]; ok {
		t.Error("destroyed run still in routes.json")
	}
}

// An unchanged config must not be rewritten: the gateway restarts its registry
// child on any mtime bump, and every provision calls through here.
func TestWriteMirrorConfigOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, (&fakeDocker{}).run, testImageFn)
	if err := os.MkdirAll(m.gwDir("acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.writeMirrorConfig("acme"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(m.mirrorConfigPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.writeMirrorConfig("acme"); err != nil {
		t.Fatal(err)
	}
	again, err := os.Stat(m.mirrorConfigPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(fi.ModTime()) {
		t.Error("unchanged config was rewritten — would restart the registry child on every provision")
	}

	// A changed (or corrupted) file is restored.
	if err := os.WriteFile(m.mirrorConfigPath("acme"), []byte("proxy: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.writeMirrorConfig("acme"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(m.mirrorConfigPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != registryConfig {
		t.Errorf("stale config not restored:\n%s", b)
	}
}
