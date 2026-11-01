package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
)

// With a gateway: the run's project is recorded before the build (a blocked
// build must be unblockable), the sandbox lands on the run network, the
// tunnel comes up, the build cache is used and refilled.
func TestProvisionThroughTheGateway(t *testing.T) {
	a, r := armedAdapter(t, rule{match: "--format {{.Config.Image}}", resp: response{stdout: "img:built\n"}})
	g := withGateway(t, a)
	os.Remove(filepath.Join(a.folderFor("w"), "gwip")) // recorded by the provision, not the fixture
	os.Remove(filepath.Join(a.folderFor("w"), "gwproject"))

	var projectAtBuild string
	r.mu.Lock()
	r.rules = append([]rule{{match: "up --workspace-folder", fn: func([]string, []byte) *response {
		projectAtBuild = a.projectFor("w")
		return nil
	}, resp: response{stdout: `{"outcome":"success","containerId":"cid1","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/w"}` + "\n"}}}, r.rules...)
	r.mu.Unlock()

	spec := envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git", Project: "P1", ProjectName: "Proj",
		EgressDomains: []string{"github.com"}}
	if _, err := a.Provision(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if projectAtBuild != "P1" {
		t.Errorf("project marker during the build = %q", projectAtBuild)
	}
	if a.gatewayIPFor("w") != "10.77.0.2" {
		t.Errorf("gateway marker = %q", a.gatewayIPFor("w"))
	}
	if g.policy.Mode != policyfile.ModeAllow || g.policy.Domains[0] != "github.com" {
		t.Errorf("policy = %+v", g.policy)
	}
	if len(g.linked) != 1 || g.linked[0] != "w" {
		t.Errorf("linked = %v", g.linked)
	}
	run := r.lines()[r.index("run -d --name w")]
	for _, want := range []string{"--network apo-egress-w", "--dns 10.77.0.2", "--registry-mirror=http://10.77.0.2:5000",
		"--insecure-registry=10.77.0.2:5001", "--label apothecary.project=P1", "--label apothecary.project.name=Proj"} {
		if !strings.Contains(run, want) {
			t.Errorf("sandbox args missing %q:\n%s", want, run)
		}
	}
	if !r.saw("sysctl -w net.ipv6.conf.all.disable_ipv6=1") {
		t.Error("ipv6 left on: a route around the tunnel")
	}
	up := r.lines()[r.index("up --workspace-folder")]
	cache := egressgw.BuildCacheRef("10.77.0.2", "P1")
	image := egressgw.BuildCacheImageRef("10.77.0.2", "P1")
	for _, want := range []string{"--cache-from type=registry,ref=" + image, "--cache-from type=registry,ref=" + cache,
		"--cache-to type=registry,ref=" + cache + ",mode=max,ignore-error=true"} {
		if !strings.Contains(up, want) {
			t.Errorf("up missing %q:\n%s", want, up)
		}
	}
	tag, push := r.index("tag img:built "+image), r.index("push "+image)
	if tag < 0 || push < tag {
		t.Errorf("cache push: tag at %d, push at %d", tag, push)
	}
}

func TestProvisionGatewayFailures(t *testing.T) {
	ctx := context.Background()
	spec := envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git", Project: "P1"}
	t.Run("no gateway, no sandbox", func(t *testing.T) {
		a, r := armedAdapter(t)
		g := withGateway(t, a)
		g.ensureErr = errors.New("gateway image failed")
		if _, err := a.Provision(ctx, spec); err == nil || !strings.Contains(err.Error(), "gateway image failed") {
			t.Fatalf("err = %v", err)
		}
		if r.saw("run -d") {
			t.Error("a sandbox started outside the firewall")
		}
	})
	t.Run("a tunnel that fails stops the build", func(t *testing.T) {
		a, r := armedAdapter(t)
		g := withGateway(t, a)
		g.linkErr = errors.New("no wireguard module")
		if _, err := a.Provision(ctx, spec); err == nil || !strings.Contains(err.Error(), "no wireguard module") {
			t.Fatalf("err = %v", err)
		}
		if r.saw("up --workspace-folder") {
			t.Error("a build ran with no tunnel")
		}
	})
}

// The cache is best-effort at every step: it costs a slower next build and
// never the provision, and it says so in the log.
func TestPushBuildCacheIsBestEffort(t *testing.T) {
	ctx := context.Background()
	const ip, proj = "10.0.0.2", "P1"
	image := egressgw.BuildCacheImageRef(ip, proj)
	cases := []struct {
		name   string
		rules  []rule
		log    string
		noPush bool
	}{
		{"inspect exit", []rule{fail("--format {{.Config.Image}}", 1, "")}, "could not resolve the built image", true},
		{"inspect spawn", []rule{spawnFail("--format {{.Config.Image}}")}, "could not resolve the built image", true},
		{"no image name", []rule{{match: "--format {{.Config.Image}}", resp: response{stdout: "  \n"}}}, "could not resolve the built image", true},
		{"tag exit", []rule{{match: "--format {{.Config.Image}}", resp: response{stdout: "img\n"}}, fail("tag img", 1, "")}, "could not tag img", true},
		{"tag spawn", []rule{{match: "--format {{.Config.Image}}", resp: response{stdout: "img\n"}}, spawnFail("tag img")}, "could not tag img", true},
		{"push exit", []rule{{match: "--format {{.Config.Image}}", resp: response{stdout: "img\n"}}, fail("push "+image, 1, "connection reset")}, "push failed (connection reset)", false},
		{"push spawn", []rule{{match: "--format {{.Config.Image}}", resp: response{stdout: "img\n"}}, spawnFail("push " + image)}, "push failed", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &ruleRunner{rules: c.rules}
			a := newTestAdapter(t, r)
			var log strings.Builder
			a.pushBuildCache(ctx, "w", ip, proj, "cid", &log)
			if !strings.Contains(log.String(), c.log) {
				t.Errorf("log = %q, want %q", log.String(), c.log)
			}
			if c.noPush && r.saw("push ") {
				t.Error("pushed after a failed step")
			}
		})
	}
	// Nothing to push to, or nothing to push: no calls at all.
	r := &ruleRunner{}
	a := newTestAdapter(t, r)
	for _, args := range [][3]string{{"", proj, "c"}, {ip, "", "c"}, {ip, proj, ""}} {
		a.pushBuildCache(ctx, "w", args[0], args[1], args[2], nil)
	}
	if len(r.snapshot()) != 0 {
		t.Errorf("calls = %v", r.lines())
	}
	// A nil log is fine on the failure path too.
	r = &ruleRunner{rules: []rule{fail("inspect", 1, "")}}
	newTestAdapter(t, r).pushBuildCache(ctx, "w", ip, proj, "c", nil)
}

func TestEgressReadsAreScopedToTheRun(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{})
	g := withGateway(t, a)
	last := time.Now().UTC().Truncate(time.Second)
	g.denials = []policyfile.Denial{{Run: "w", Host: "evil.io", Count: 2, Last: last}, {Run: "other", Host: "theirs.io"}}
	g.audit = []policyfile.AuditRecord{
		{Run: "w", Host: "a.io", DstIP: "1.1.1.1", Port: 443, Proto: "tls", Verdict: "allow", Count: 3, BytesOut: 10, BytesIn: 20, First: last, Last: last},
		{Run: "other", Host: "theirs.io"},
	}
	d := a.EgressDenials(ctx, "w")
	if len(d) != 1 || d[0] != (egressport.EgressDenial{Host: "evil.io", Count: 2, Last: last}) {
		t.Errorf("denials = %+v", d)
	}
	rows := a.EgressAudit(ctx, "w")
	if len(rows) != 1 || rows[0].Host != "a.io" || rows[0].DstIP != "1.1.1.1" || rows[0].Proto != "tls" || rows[0].Verdict != "allow" ||
		rows[0].Count != 3 || rows[0].BytesOut != 10 || rows[0].BytesIn != 20 || !rows[0].First.Equal(last) {
		t.Errorf("audit = %+v (one run must never see another's trail)", rows)
	}
}

func TestEgressRulesEditTheProjectPolicy(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{})
	g := withGateway(t, a)
	g.policy = policyfile.File{Domains: []string{"github.com"}}
	if err := a.AllowEgress(ctx, "w", "pypi.org"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetEgressRule(ctx, "w", "github.com", false); err != nil {
		t.Fatal(err)
	}
	p, err := a.EgressPolicy(ctx, "w")
	if err != nil || p.Mode != "allow" || len(p.Domains) != 1 || p.Domains[0] != "pypi.org" || p.BlockedDomains[0] != "github.com" {
		t.Errorf("policy = %+v, %v", p, err)
	}
	g.mu.Lock()
	g.srv.Close()
	g.mu.Unlock()
	if _, err := a.EgressPolicy(ctx, "w"); err == nil {
		t.Error("an unreachable gateway read as a policy")
	}
}

func TestEgressWithoutAGateway(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{})
	if a.EgressDenials(ctx, "w") != nil || a.EgressAudit(ctx, "w") != nil {
		t.Error("reads must be empty with the firewall off")
	}
	for name, err := range map[string]error{
		"allow":  a.AllowEgress(ctx, "w", "x.io"),
		"rule":   a.SetEgressRule(ctx, "w", "x.io", true),
		"policy": func() error { _, err := a.EgressPolicy(ctx, "w"); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "egress gateway is off") {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func TestBuildCacheFacts(t *testing.T) {
	ctx := context.Background()
	off := newTestAdapter(t, &ruleRunner{})
	if info, err := off.BuildCache(ctx, "P1"); err != nil || info.Available {
		t.Errorf("no gateway = %+v, %v", info, err)
	}
	if err := off.ClearBuildCache(ctx, "P1"); err == nil {
		t.Error("cleared a cache nobody holds")
	}
	a := newTestAdapter(t, &ruleRunner{})
	g := withGateway(t, a)
	g.bytes = 2048
	if info, err := a.BuildCache(ctx, ""); err != nil || info.Available {
		t.Errorf("no project = %+v, %v", info, err)
	}
	if info, err := a.BuildCache(ctx, "P1"); err != nil || !info.Available || info.Bytes != 2048 {
		t.Errorf("cache = %+v, %v", info, err)
	}
	if err := a.ClearBuildCache(ctx, ""); err == nil {
		t.Error("cleared with no project")
	}
	if err := a.ClearBuildCache(ctx, "P1"); err != nil || g.cleared != 1 {
		t.Errorf("clear = %v, cleared=%d", err, g.cleared)
	}
	// A project that never ran has no gateway yet: nothing cached, no error.
	g.up = false
	if info, err := a.BuildCache(ctx, "P1"); err != nil || info.Available {
		t.Errorf("gateway down = %+v, %v", info, err)
	}
}

func TestDestroyDropsTheRunsEgress(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	g := withGateway(t, a)
	if err := a.Destroy(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if len(g.routes) != 1 || g.routes[0] != "-w" || len(g.detached) != 1 {
		t.Errorf("routes = %v, detached = %v", g.routes, g.detached)
	}
	mustNotExist(t, a.folderFor("w"))
}

// A rebuild keeps the checkout: agent commits live there and nothing else has
// them yet. Compose takes the sandbox-teardown road, and the promise holds on
// it too.
func TestComposeRebuildKeepsTheCheckout(t *testing.T) {
	a, r := armedAdapter(t, rule{match: "read-configuration", resp: response{stdout: `{"configuration":{"dockerComposeFile":"c.yml"}}` + "\n"}})
	src := a.srcDir("w")
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit := filepath.Join(src, "agent-work.txt")
	if err := os.WriteFile(commit, []byte("unpushed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Rebuild(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"}); err != nil {
		t.Fatal(err)
	}
	mustExist(t, commit)
	if r.saw("clone --") {
		t.Error("a rebuild re-cloned over the run's work")
	}
}

func TestRebuildComposeTearsDownThenProvisions(t *testing.T) {
	sandbox := "ps -aq --filter label=apothecary.env=w --filter label=apothecary.sandbox=true"
	compose := rule{match: "read-configuration", resp: response{stdout: `{"mergedConfiguration":{"dockerComposeFile":"c.yml"}}` + "\n"}}
	a, r := armedAdapter(t, compose, rule{match: sandbox, resp: response{stdout: "sbx1\n"}})
	r.mu.Lock()
	// The teardown removes the fixture's checkout: the clone puts one back.
	r.rules = append([]rule{{match: "clone --", fn: func(args []string, _ []byte) *response {
		seedDevcontainer(t, a, "w")
		armSandbox(t, a, "w")
		return nil
	}}}, r.rules...)
	r.mu.Unlock()
	if err := a.Rebuild(context.Background(), envport.EnvSpec{Name: "w", RepoURL: "https://git.test/r.git"}); err != nil {
		t.Fatal(err)
	}
	rm, up := r.index("rm -f sbx1"), r.index("up --workspace-folder")
	if rm < 0 || up < rm {
		t.Errorf("rm at %d, up at %d", rm, up)
	}
	if r.saw("--remove-existing-container") {
		t.Error("a compose rebuild used the single-container flag")
	}
}

func TestRebuildTeardownFailureStopsIt(t *testing.T) {
	a, r := armedAdapter(t,
		rule{match: "read-configuration", resp: response{stdout: `{"configuration":{"dockerComposeFile":"c.yml"}}` + "\n"}},
		fail("apothecary.sandbox=true", 1, "daemon down"))
	err := a.Rebuild(context.Background(), envport.EnvSpec{Name: "w"})
	if err == nil || !strings.Contains(err.Error(), "rebuild w: teardown") {
		t.Fatalf("err = %v", err)
	}
	if r.saw("up --workspace-folder") {
		t.Error("rebuilt over a sandbox that would not go")
	}
}

func TestRebuildFallsBackToTheRunName(t *testing.T) {
	r := &ruleRunner{rules: []rule{fail("git ", 128, "nope")}}
	a := newTestAdapter(t, r)
	err := a.Rebuild(context.Background(), envport.EnvSpec{RunID: "01ABCDEFGH"})
	if err == nil {
		t.Fatal("clone failure swallowed")
	}
	if !r.saw(a.srcDir(WorkspaceName("01ABCDEFGH"))) {
		t.Errorf("calls = %v", r.lines())
	}
}
