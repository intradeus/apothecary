package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// watcher drives g.watch one tick at a time and records every ruleset applied.
type watcher struct {
	t     *testing.T
	ticks chan time.Time

	mu    sync.Mutex
	rules []string
}

func startWatch(t *testing.T, g *gateway) *watcher {
	t.Helper()
	w := &watcher{t: t, ticks: make(chan time.Time)}
	applyNFT = func(s string) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.rules = append(w.rules, s)
		return nil
	}
	origTick := watchTick
	watchTick = func() <-chan time.Time { return w.ticks }
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		g.watch()
	}()
	t.Cleanup(func() {
		close(w.ticks)
		<-stopped
		watchTick = origTick
	})
	return w
}

// step runs one whole iteration: the loop takes the second tick only once it
// has finished with the first. Both land after any write the test just made.
func (w *watcher) step() {
	w.t.Helper()
	for range 2 {
		select {
		case w.ticks <- time.Now():
		case <-time.After(10 * time.Second):
			w.t.Fatal("watch is stuck")
		}
	}
}

func (w *watcher) lastRules() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.rules) == 0 {
		return ""
	}
	return w.rules[len(w.rules)-1]
}

func (w *watcher) applied() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.rules)
}

// writeNewer writes v as JSON and stamps it past anything already seen, so the
// test does not depend on the filesystem's mtime granularity.
func writeNewer(t *testing.T, path string, v any, offset time.Duration) {
	t.Helper()
	if err := writeAtomic(path, v); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(offset)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func useRouteTable(t *testing.T, iface string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "route")
	writeRouteTable(t, path, iface)
	orig := routeTable
	routeTable = path
	t.Cleanup(func() { routeTable = orig })
}

func writeRouteTable(t *testing.T, path, iface string) {
	t.Helper()
	table := "Iface\tDestination\tGateway\tFlags\n" +
		"wg0\t00005A0A\t00000000\t0001\n" +
		iface + "\t00000000\t0101A8C0\t0003\n"
	if err := os.WriteFile(path+".tmp", []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func TestWatchHotReloadsThePolicy(t *testing.T) {
	g := newTestGateway(t)
	useRouteTable(t, "eth0")
	if err := g.installPolicy(File{Mode: ModeAllow, Domains: []string{"old.example"}}); err != nil {
		t.Fatal(err)
	}
	w := startWatch(t, g)

	// The first tick sees the (absent) file; no reload, nothing changes.
	w.step()
	if !g.pol.Load().verdict("old.example", nil) {
		t.Fatal("no file: the running policy must stay")
	}

	writeNewer(t, g.policyPath, File{Mode: ModeAllow, Domains: []string{"new.example"}}, time.Minute)
	w.step()
	pol := g.pol.Load()
	if !pol.verdict("new.example", nil) || pol.verdict("old.example", nil) {
		t.Fatal("the new policy did not take effect")
	}
	if w.applied() == 0 {
		t.Fatal("a reload must re-apply nft")
	}

	// Unchanged mtime: not reloaded, so no needless nft churn.
	before := w.applied()
	w.step()
	if got := w.applied(); got != before {
		t.Fatalf("nft re-applied %d times with nothing changed", got-before)
	}

	// A flip to deny mode applies live, rules included.
	writeNewer(t, g.policyPath, File{Mode: ModeDeny, Domains: []string{"bad.example"}}, 2*time.Minute)
	w.step()
	if pol := g.pol.Load(); !pol.verdict("anything.example", nil) || pol.verdict("bad.example", nil) {
		t.Fatal("deny mode did not take effect")
	}

	// A broken write keeps enforcing the last good policy.
	if err := os.WriteFile(g.policyPath, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(3 * time.Minute)
	if err := os.Chtimes(g.policyPath, when, when); err != nil {
		t.Fatal(err)
	}
	w.step()
	if pol := g.pol.Load(); pol.verdict("bad.example", nil) {
		t.Fatal("a corrupt policy file must leave the last good one in force")
	}
}

func TestWatchReloadsRoutes(t *testing.T) {
	g := newTestGateway(t)
	useRouteTable(t, "eth0")
	if err := g.installPolicy(File{Mode: ModeAllow}); err != nil {
		t.Fatal(err)
	}
	w := startWatch(t, g)
	writeNewer(t, g.routesPath, Routes{RunSubnets: map[string]string{"run-9": "10.9.0.0/24"}}, time.Minute)
	w.step()
	if got := g.attributeIP(net.ParseIP("10.9.0.7")); got != "run-9" {
		t.Fatalf("attributeIP = %q, want run-9", got)
	}
}

func TestWatchFollowsTheUplinkAndFlushes(t *testing.T) {
	g := newTestGateway(t)
	useRouteTable(t, "eth0")
	if err := g.installPolicy(File{Mode: ModeAllow}); err != nil {
		t.Fatal(err)
	}
	w := startWatch(t, g)
	w.step()
	if got := w.applied(); got != 0 {
		t.Fatalf("nft applied %d times with the uplink unchanged", got)
	}

	writeRouteTable(t, routeTable, "eth7")
	w.step()
	if got := *g.ext.Load(); got != "eth7" {
		t.Fatalf("ext = %q", got)
	}
	if !strings.Contains(w.lastRules(), `iifname != "eth7"`) {
		t.Fatalf("rules not re-rendered for the new uplink:\n%s", w.lastRules())
	}

	// The same tick flushes what the engine ingests.
	g.recordDenial("run-1", "evil.example")
	g.audit.record("run-1", "evil.example", "192.0.2.1", 443, "tls", "deny", 0, 0)
	w.step()
	var df DenialsFile
	if err := readJSON(g.denialsPath, &df); err != nil {
		t.Fatal(err)
	}
	if len(df.Denials) != 1 || df.Denials[0].Host != "evil.example" {
		t.Fatalf("denials file = %+v", df)
	}
	var af AuditFile
	if err := readJSON(g.audit.path, &af); err != nil {
		t.Fatal(err)
	}
	if len(af.Records) != 1 || af.Records[0].Verdict != "deny" {
		t.Fatalf("audit file = %+v", af)
	}
}

func TestDetectEXT(t *testing.T) {
	orig := routeTable
	t.Cleanup(func() { routeTable = orig })
	dir := t.TempDir()

	useFile := func(name, content string) {
		routeTable = filepath.Join(dir, name)
		if err := os.WriteFile(routeTable, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	useFile("default", "Iface\tDestination\tGateway\tFlags\neth3\t0011A8C0\t00000000\t0001\neth1\t00000000\t0100A8C0\t0003\n")
	if got := detectEXT(); got != "eth1" {
		t.Errorf("detectEXT = %q, want the default-route interface", got)
	}
	useFile("none", "Iface\tDestination\tGateway\tFlags\neth3\t0011A8C0\t00000000\t0001\n")
	if got := detectEXT(); got != "" {
		t.Errorf("detectEXT = %q, want none without a default route", got)
	}
	routeTable = filepath.Join(dir, "missing")
	if got := detectEXT(); got != "" {
		t.Errorf("detectEXT = %q, want none when unreadable", got)
	}
}
