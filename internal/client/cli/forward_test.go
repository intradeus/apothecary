package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// forwarding is a signed-in rig whose daemon runs in this process, as the
// real one would in its own: commands reach it over its socket.
func forwarding(t *testing.T) (*rig, *fakeEngine) {
	t.Helper()
	old := followEvery
	followEvery = 10 * time.Millisecond
	t.Cleanup(func() { followEvery = old })
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	r.env.SpawnDaemon = func() error {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runDaemon(ctx, r.env); err != nil {
				t.Errorf("daemon: %v", err)
			}
		}()
		return nil
	}
	t.Cleanup(func() { cancel(); wg.Wait() })
	return r, e
}

// localOf reads where status says a run's port landed.
func localOf(t *testing.T, r *rig, remote int) int {
	t.Helper()
	runs, err := daemonStatus(context.Background(), sockOf(t, r))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range runs {
		for _, p := range st.Ports {
			if p.Remote == remote {
				return p.Local
			}
		}
	}
	return 0
}

func sockOf(t *testing.T, r *rig) string {
	t.Helper()
	s, err := daemonSocket(r.env)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func roundTrip(t *testing.T, port int) string {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write([]byte("hi\n"))
	buf := make([]byte, 64)
	var got []byte
	for !strings.Contains(string(got), "hi\n") {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
		got = append(got, buf[:n]...)
	}
	return string(got)
}

// A named port lands on a free local port, says so, carries connections to
// the run, outlives the command, and is listed by status with the run's
// name; stopping it stops the daemon.
func TestForwardAPortInTheBackground(t *testing.T) {
	r, _ := forwarding(t)
	if code := r.run("forward", "r1", "3000"); code != 0 {
		t.Fatalf("forward = %d %q", code, r.err)
	}
	local := localOf(t, r, 3000)
	if local == 0 || local == 3000 {
		t.Fatalf("local port = %d: a free one, not the run's", local)
	}
	out := r.out.String()
	for _, want := range []string{"PROJ-1 · Fix r1   apo-r1", "3000  → localhost:" + strconv.Itoa(local), "apothecary forward stop apo-r1"} {
		if !strings.Contains(out, want) {
			t.Errorf("forward said %q, lacks %q", out, want)
		}
	}
	if got := roundTrip(t, local); got != "port 3000\nhi\n" {
		t.Errorf("round trip = %q", got)
	}
	if code := r.run("forward", "status"); code != 0 || !strings.Contains(r.out.String(), "PROJ-1 · Fix r1") || !strings.Contains(r.out.String(), "localhost:"+strconv.Itoa(local)) {
		t.Errorf("status = %d %q", code, r.out)
	}
	// Forwarded again (the run named by its workspace), nothing changes.
	if code := r.run("forward", "apo-r1", "3000"); code != 0 || localOf(t, r, 3000) != local {
		t.Errorf("again = %d, local %d", code, localOf(t, r, 3000))
	}
	if code := r.run("forward", "stop", "apo-r1", "3000"); code != 0 || !strings.Contains(r.out.String(), "Stopped forwarding port 3000 of apo-r1") ||
		!strings.Contains(r.out.String(), "Nothing else is forwarded") {
		t.Errorf("stop = %d %q", code, r.out)
	}
	waitFor(t, func() bool { _, err := os.Stat(sockOf(t, r)); return err != nil })
	if code := r.run("forward", "status"); code != 0 || !strings.Contains(r.out.String(), "Nothing is forwarded.") {
		t.Errorf("status after = %d %q", code, r.out)
	}
	if code := r.run("forward", "stop", "--all"); code != 0 || !strings.Contains(r.out.String(), "Nothing is forwarded.") {
		t.Errorf("stop with no daemon = %d %q", code, r.out)
	}
	// Forwarded again later, it lands on the same local port.
	if code := r.run("forward", "r1", "3000"); code != 0 || localOf(t, r, 3000) != local {
		t.Errorf("remembered = %d, local %d want %d", code, localOf(t, r, 3000), local)
	}
	if code := r.run("forward", "logs"); code != 0 || !strings.Contains(r.out.String(), "port 3000 → localhost:") {
		t.Errorf("logs = %d %q", code, r.out)
	}
	_ = r.run("forward", "stop", "--all")
}

// A run forwarded whole follows its ports: new ones land on free ports as
// they open, gone ones close, and two runs forward side by side.
func TestForwardFollowsRunsInParallel(t *testing.T) {
	r, e := forwarding(t)
	e.listen(3000, 5432)
	if code := r.run("forward", "r1"); code != 0 || !strings.Contains(r.out.String(), "New ports are forwarded as the run opens them") {
		t.Fatalf("forward = %d %q %q", code, r.out, r.err)
	}
	if localOf(t, r, 3000) == 0 || localOf(t, r, 5432) == 0 {
		t.Fatal("both ports forwarded at once")
	}
	if code := r.run("forward", "r2", "8080"); code != 0 {
		t.Fatalf("second run = %d %q", code, r.err)
	}
	e.listen(5432, 9000)
	waitFor(t, func() bool { return localOf(t, r, 9000) != 0 && localOf(t, r, 3000) == 0 })
	if code := r.run("forward", "status"); code != 0 || !strings.Contains(r.out.String(), "Fix r2") || !strings.Contains(r.out.String(), "following") {
		t.Errorf("status = %d %q", code, r.out)
	}
	if code := r.run("forward", "stop", "r1"); code != 0 || strings.Contains(r.out.String(), "Nothing else") {
		t.Errorf("stop r1 = %d %q", code, r.out)
	}
	if code := r.run("forward", "stop", "r1"); code != 1 || !strings.Contains(r.err.String(), `"r1" is not forwarded`) {
		t.Errorf("stop twice = %d %q", code, r.err)
	}
	if code := r.run("forward", "stop", "r2", "1"); code != 1 || !strings.Contains(r.err.String(), "port 1 of r2 is not forwarded") {
		t.Errorf("stop a port not forwarded = %d %q", code, r.err)
	}
	if code := r.run("forward", "stop", "--all"); code != 0 || !strings.Contains(r.out.String(), "Stopped every forward") {
		t.Errorf("stop all = %d %q", code, r.out)
	}
}

// --same keeps the run's port numbers; a taken one is refused when named,
// and moves to a free port when followed. A named local port is used as is.
func TestForwardSameAndNamedPorts(t *testing.T) {
	r, e := forwarding(t)
	old := listenLocal
	t.Cleanup(func() { listenLocal = old })
	taken := map[int]bool{4000: true, 4001: true}
	listenLocal = func(port int) (net.Listener, error) {
		if taken[port] {
			return nil, errors.New("address in use")
		}
		return old(port)
	}
	free := func() int {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	same := free()
	if code := r.run("forward", "r1", strconv.Itoa(same), "--same"); code != 0 || localOf(t, r, same) != same {
		t.Fatalf("--same = %d %q, local %d", code, r.err, localOf(t, r, same))
	}
	if code := r.run("forward", "r1", "4000", "--same"); code != 1 || !strings.Contains(r.err.String(), "localhost:4000 is taken") {
		t.Errorf("--same taken = %d %q", code, r.err)
	}
	named := free()
	if code := r.run("forward", "r1", "5000:"+strconv.Itoa(named)); code != 0 || localOf(t, r, 5000) != named {
		t.Errorf("named local = %d, local %d", code, localOf(t, r, 5000))
	}
	if code := r.run("forward", "r1", "5001:4001"); code != 1 || !strings.Contains(r.err.String(), "localhost:4001 is taken") {
		t.Errorf("named taken = %d %q", code, r.err)
	}
	e.listen(4000)
	if code := r.run("forward", "r2", "--same"); code != 0 {
		t.Fatalf("follow --same = %d %q", code, r.err)
	}
	if got := localOf(t, r, 4000); got == 0 || got == 4000 {
		t.Errorf("a taken port followed with --same goes to a free one: %d", got)
	}
	_ = r.run("forward", "stop", "--all")
}

// What the engine refuses ends the forward: an unknown run, a run that is
// gone, a sign-in that is.
func TestForwardRefusals(t *testing.T) {
	r, _ := forwarding(t)
	if code := r.run("forward", "nope"); code != 1 || !strings.Contains(r.err.String(), `no run or workspace named "nope"`) {
		t.Errorf("unknown = %d %q", code, r.err)
	}
	if code := r.run("forward", "rgone"); code != 1 || !strings.Contains(r.err.String(), "no such run") {
		t.Errorf("gone = %d %q", code, r.err)
	}
	// A run whose ports stop being listable mid-way is dropped.
	if code := r.run("forward", "r1", "3000"); code != 0 {
		t.Fatal(r.err)
	}
	_ = saveConfig(r.env, Config{URL: mustConfig(t, r).URL, Token: "bad"})
	if code := r.run("forward", "r1", "3001"); code != 1 || !strings.Contains(r.err.String(), "sign in again") {
		t.Errorf("signed out = %d %q", code, r.err)
	}
	// A connection through a forward whose sign-in is gone drops the run.
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(localOf(t, r, 3000)))
	if err == nil {
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Read(make([]byte, 1))
		c.Close()
	}
	waitFor(t, func() bool { _, err := os.Stat(sockOf(t, r)); return err != nil })
}

func mustConfig(t *testing.T, r *rig) Config {
	t.Helper()
	cfg, err := loadConfig(r.env)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A daemon that cannot be started, or never answers, is said so.
func TestForwardWithoutADaemon(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	old := daemonStart
	daemonStart = 50 * time.Millisecond
	t.Cleanup(func() { daemonStart = old })
	if code := r.run("forward", "r1"); code != 1 || !strings.Contains(r.err.String(), "cannot start the forwarder") {
		t.Errorf("no spawner = %d %q", code, r.err)
	}
	r.env.SpawnDaemon = func() error { return errors.New("no fork") }
	if code := r.run("forward", "r1"); code != 1 || !strings.Contains(r.err.String(), "start the forwarder: no fork") {
		t.Errorf("spawn fails = %d %q", code, r.err)
	}
	r.env.SpawnDaemon = func() error { return nil }
	if code := r.run("forward", "r1"); code != 1 || !strings.Contains(r.err.String(), "did not start") {
		t.Errorf("never answers = %d %q", code, r.err)
	}
	if code := r.run("forward", "logs"); code != 0 || !strings.Contains(r.out.String(), "No log yet") {
		t.Errorf("logs = %d %q", code, r.out)
	}
	r.env.ConfigDir = ""
	for _, args := range [][]string{{"forward", "status"}, {"forward", "stop", "--all"}, {"forward", "logs"}} {
		if code := r.run(args...); code != 1 {
			t.Errorf("%v with no config dir = %d", args, code)
		}
	}
}

// The real spawn starts this binary's daemon command detached; a daemon
// started when one already answers leaves.
func TestSpawnAndASecondDaemon(t *testing.T) {
	r, _ := forwarding(t)
	truth, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true(1)")
	}
	r.env.Executable = truth
	if err := spawnDaemon(r.env); err != nil {
		t.Errorf("spawn = %v", err)
	}
	if code := r.run("forward", "r1", "3000"); code != 0 {
		t.Fatal(r.err)
	}
	if err := runDaemon(context.Background(), r.env); err != nil {
		t.Errorf("a second daemon = %v", err)
	}
	_ = r.run("forward", "stop", "--all")
	r.env.ConfigDir = ""
	if err := spawnDaemon(r.env); err == nil {
		t.Error("spawn with no config dir")
	}
	if err := runDaemon(context.Background(), r.env); err == nil {
		t.Error("daemon with no config dir")
	}
}

// A daemon nobody asks anything leaves after its idle time.
func TestIdleDaemonLeaves(t *testing.T) {
	old := daemonIdle
	daemonIdle = 20 * time.Millisecond
	t.Cleanup(func() { daemonIdle = old })
	r := newRig(t)
	done := make(chan error, 1)
	go func() { done <- runDaemon(context.Background(), r.env) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("idle daemon = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an idle daemon must leave")
	}
}

func TestParseForwardAndHelpers(t *testing.T) {
	req, err := parseForward([]string{"apo-w", "5432", "3000:13000", "--same"})
	if err != nil || req.Run != "apo-w" || !req.Same || len(req.Ports) != 2 || req.Ports[0] != (mapping{5432, 0}) || req.Ports[1] != (mapping{3000, 13000}) {
		t.Errorf("req = %+v %v", req, err)
	}
	for _, bad := range []string{"x", "0", "70000", "1:x", "1:0", "1:70000"} {
		if _, err := parseMapping(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if shortID("01M75S79PPDGDYTCTNQ3SZDAAR") != "01M7…DAAR" || shortID("r1") != "r1" {
		t.Error("short ids")
	}
	if joinInts([]int{1, 2}) != "1, 2" || joinNotes(nil) != "nothing forwarded" || joinNotes([]string{"a", "b"}) != "a; b" {
		t.Error("joins")
	}
	a := loadAssignments(filepath.Join(t.TempDir(), "ports.json"))
	for i := range maxAssigned + 5 {
		a.set("run"+strconv.Itoa(i), 1, 2)
	}
	if len(a.by) > maxAssigned {
		t.Errorf("remembered %d runs", len(a.by))
	}
	again := loadAssignments(a.path)
	if len(again.by) != len(a.by) {
		t.Error("assignments must persist")
	}
	(&assignments{by: map[string]map[int]int{}}).set("r", 1, 2) // no path: memory only
}

// printRun says how busy each forward is.
func TestPrintRun(t *testing.T) {
	out := &lockedBuffer{}
	p := &printer{out: out, err: out}
	printRun(p, runState{ID: "r1", Label: "L", Ports: []portState{{Remote: 1, Local: 2, Active: 1}, {Remote: 3, Local: 4, Active: 2}}})
	printRun(p, runState{ID: "r2", Label: "M"})
	for _, want := range []string{"1 connection ", "2 connections", "nothing listening yet"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("out lacks %q: %q", want, out.String())
		}
	}
}

// A followed run whose listing fails is retried; one that is gone is
// dropped, and the daemon with nothing left leaves.
func TestFollowRetriesThenDrops(t *testing.T) {
	r, e := forwarding(t)
	e.listen(3000)
	if code := r.run("forward", "r1"); code != 0 {
		t.Fatal(r.err)
	}
	e.mu.Lock()
	e.portsAt = 502
	e.mu.Unlock()
	time.Sleep(50 * time.Millisecond) // a few failed listings
	if localOf(t, r, 3000) == 0 {
		t.Fatal("a failed listing must keep the forwards")
	}
	e.mu.Lock()
	e.portsAt = 404
	e.mu.Unlock()
	waitFor(t, func() bool { _, err := os.Stat(sockOf(t, r)); return err != nil })
	if code := r.run("forward", "logs"); code != 0 || !strings.Contains(r.out.String(), "retrying") || !strings.Contains(r.out.String(), "stopped (no such run)") {
		t.Errorf("logs = %q", r.out)
	}
}

// A daemon told to stop with forwards open closes them all.
func TestDaemonStopsWithForwardsOpen(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	r.env.SpawnDaemon = func() error { go func() { done <- runDaemon(ctx, r.env) }(); return nil }
	if code := r.run("forward", "r1", "3000"); code != 0 {
		t.Fatal(r.err)
	}
	local := localOf(t, r, 3000)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("daemon = %v", err)
	}
	if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(local)); err == nil {
		c.Close()
		t.Error("the forward outlived its daemon")
	}
}
