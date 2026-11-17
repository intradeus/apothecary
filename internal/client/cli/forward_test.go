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

// A run forwarded whole forwards the ports it listens on then, once: one
// opened later is forwarded by asking again, and nothing closes by itself.
// Two runs forward side by side; a run listening on nothing is refused.
func TestForwardEveryPortOnceRunsInParallel(t *testing.T) {
	r, e := forwarding(t)
	e.listen()
	if code := r.run("forward", "r1"); code != 1 || !strings.Contains(r.err.String(), "listens on no port yet") {
		t.Errorf("nothing listening = %d %q", code, r.err)
	}
	e.listen(3000, 5432)
	if code := r.run("forward", "r1"); code != 0 || localOf(t, r, 3000) == 0 || localOf(t, r, 5432) == 0 {
		t.Fatalf("forward = %d %q %q", code, r.out, r.err)
	}
	if code := r.run("forward", "r2", "8080"); code != 0 {
		t.Fatalf("second run = %d %q", code, r.err)
	}
	e.listen(5432, 9000)
	if localOf(t, r, 9000) != 0 || localOf(t, r, 3000) == 0 {
		t.Error("a forward does not follow the run")
	}
	if code := r.run("forward", "r1"); code != 0 || localOf(t, r, 9000) == 0 || localOf(t, r, 3000) == 0 {
		t.Errorf("again = %d %q", code, r.err)
	}
	if code := r.run("forward", "status"); code != 0 || !strings.Contains(r.out.String(), "Fix r2") || strings.Contains(r.out.String(), "following") {
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
	// A run whose ports cannot be listed is not forwarded.
	e.mu.Lock()
	e.portsAt = 502
	e.mu.Unlock()
	if code := r.run("forward", "r1"); code != 1 {
		t.Errorf("listing fails = %d %q", code, r.err)
	}
}

// offered is a listener that says it is on the port asked for: the ports a
// test offers are free whatever this machine runs.
type offered struct {
	net.Listener
	port int
}

func (l offered) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.port} }

// takenPorts makes taken ports refused, and ports in [lo, hi) free.
func takenPorts(t *testing.T, lo, hi int, taken ...int) {
	t.Helper()
	old := listenLocal
	t.Cleanup(func() { listenLocal = old })
	set := map[int]bool{}
	for _, p := range taken {
		set[p] = true
	}
	listenLocal = func(port int) (net.Listener, error) {
		if set[port] {
			return nil, errors.New("address in use")
		}
		if port >= lo && port < hi {
			ln, err := old(0)
			if err != nil {
				return nil, err
			}
			return offered{ln, port}, nil
		}
		return old(port)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// --same keeps the run's port numbers and a named local port is used as
// is; one taken refuses the whole command, which forwards nothing and
// offers itself again with only the taken ports moved to free ones.
func TestForwardSameAndNamedPorts(t *testing.T) {
	r, e := forwarding(t)
	takenPorts(t, 4000, 6000, 4000, 4001, 5001)
	same := freePort(t)
	if code := r.run("forward", "r1", strconv.Itoa(same), "--same"); code != 0 || localOf(t, r, same) != same {
		t.Fatalf("--same = %d %q, local %d", code, r.err, localOf(t, r, same))
	}
	if code := r.run("forward", "r1", "4000", "--same"); code != 1 || !strings.Contains(r.err.String(), "localhost:4000 is taken") ||
		!strings.Contains(r.err.String(), "nothing was forwarded") || !strings.Contains(r.out.String(), "apothecary forward r1 4000:4002 --same\n") {
		t.Errorf("--same taken = %d %q %q", code, r.err, r.out)
	}
	if code := r.run("forward", "r1", "5000:4500"); code != 0 || localOf(t, r, 5000) != 4500 {
		t.Errorf("named local = %d, local %d", code, localOf(t, r, 5000))
	}
	if code := r.run("forward", "r1", "5001:4001"); code != 1 || !strings.Contains(r.err.String(), "localhost:4001 is taken") ||
		!strings.Contains(r.out.String(), "apothecary forward r1 5001:4002\n") {
		t.Errorf("named taken = %d %q %q", code, r.err, r.out)
	}
	// Several named, two taken: none forwarded, both offered in place.
	ok := freePort(t)
	if code := r.run("forward", "apo-r1", "6000:4000", strconv.Itoa(ok), "4001", "--same"); code != 1 || localOf(t, r, ok) != 0 ||
		!strings.Contains(r.out.String(), "apothecary forward apo-r1 6000:4002 "+strconv.Itoa(ok)+" 4001:4003 --same\n") {
		t.Errorf("two taken = %d %q %q", code, r.err, r.out)
	}
	// A port forwarded already, elsewhere than asked: refused, nothing offered.
	if code := r.run("forward", "r1", "5000:4600"); code != 1 || !strings.Contains(r.err.String(), "forwarded to localhost:4500 already (apothecary forward stop apo-r1 5000 first)") ||
		strings.Contains(r.out.String(), "free now") {
		t.Errorf("forwarded elsewhere = %d %q %q", code, r.err, r.out)
	}
	// The whole run with --same, one port taken: the others are let go, and
	// the command offered names only the taken one, as an exception.
	other := freePort(t)
	e.listen(other, 4000)
	if code := r.run("forward", "r2", "--same"); code != 1 || localOf(t, r, other) != 0 ||
		!strings.Contains(r.out.String(), "apothecary forward r2 --same 4000:4002\n") || strings.Contains(r.out.String(), "Fix r2") {
		t.Errorf("whole run, one taken = %d %q %q", code, r.err, r.out)
	}
	// The exception taken too: another one offered.
	if code := r.run("forward", "r2", "--same", "4000:5001"); code != 1 || localOf(t, r, other) != 0 ||
		!strings.Contains(r.out.String(), "apothecary forward r2 --same 4000:5002\n") {
		t.Errorf("exception taken = %d %q %q", code, r.err, r.out)
	}
	if code := r.run("forward", "r2", "--same", "4000:4002"); code != 0 || localOf(t, r, other) != other || localOf(t, r, 4000) != 4002 {
		t.Errorf("as offered = %d %q, locals %d %d", code, r.err, localOf(t, r, other), localOf(t, r, 4000))
	}
	if code := r.run("forward", "r2", "--same", "4000:4002"); code != 0 {
		t.Errorf("as offered, again = %d %q", code, r.err)
	}
	_ = r.run("forward", "stop", "--all")
}

// The free ports offered are the nearest above the taken one, none twice
// and none to avoid, else any free one; with none at all, the bare port.
func TestFreeInstead(t *testing.T) {
	takenPorts(t, 7000, 7100)
	old := listenLocal
	listenLocal = func(port int) (net.Listener, error) {
		if port > 7000 && port <= 7002+suggestSpan && port != 7003 && port != 7004 {
			return nil, errors.New("address in use")
		}
		return old(port)
	}
	if freeInstead(nil, nil) != nil {
		t.Error("nothing taken, nothing offered")
	}
	// 7003 and 7004 are the free ones near 7000 and 7002; 7004 is to be
	// avoided, so the second gets any.
	got := freeInstead([]mapping{{1, 7000}, {2, 7002}}, map[int]bool{7004: true})
	if len(got) != 2 || got[0] != (mapping{1, 7003}) || got[1].Remote != 2 || got[1].Local == 0 || (got[1].Local > 7000 && got[1].Local < 7100) {
		t.Errorf("offered %+v", got)
	}
	listenLocal = func(int) (net.Listener, error) { return nil, errors.New("no ports") }
	if got := freeInstead([]mapping{{1, 65535}}, nil); got[0].Local != 0 {
		t.Errorf("with no free port, offered %+v", got)
	}
	for _, c := range []struct {
		args  []string
		taken []mapping
		want  string
	}{
		{[]string{"apo-w", "--same"}, []mapping{{5000, 5001}}, "apothecary forward apo-w --same 5000:5001"},
		{[]string{"apo-w", "5432:2345", "3000", "8080", "--same"}, []mapping{{5432, 2346}, {8080, 8081}}, "apothecary forward apo-w 5432:2346 3000 8080:8081 --same"},
		{[]string{"3000", "3000"}, []mapping{{3000, 0}}, "apothecary forward 3000 3000"},
	} {
		if got := retryCommand(c.args, c.taken); got != c.want {
			t.Errorf("retry %v = %q, want %q", c.args, got, c.want)
		}
	}
}

// A port another process listens on is taken even where the loopback bind
// alone would succeed: macOS and Windows allow 127.0.0.1:p beside another
// process's wildcard :p, which still answers localhost over IPv6.
func TestListenLocalRefusesAnsweredPort(t *testing.T) {
	for _, addr := range []string{":0", "127.0.0.1:0"} {
		other, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		port := other.Addr().(*net.TCPAddr).Port
		if ln, err := listenLocal(port); err == nil {
			ln.Close()
			t.Errorf("listenLocal(%d) beside a listener on %s succeeded", port, addr)
		}
		other.Close()
	}
	ln, err := listenLocal(0)
	if err != nil {
		t.Fatalf("a free port: %v", err)
	}
	ln.Close()
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
	if _, err := parseForward([]string{"apo-w", "5432", "5432:1"}); err == nil || !strings.Contains(err.Error(), "port 5432 is named twice") {
		t.Errorf("a port twice = %v", err)
	}
	if runRef("apo-w", "r1") != "apo-w" || runRef("", "r1") != "r1" {
		t.Error("run refs")
	}
	if shortID("01M75S79PPDGDYTCTNQ3SZDAAR") != "01M7…DAAR" || shortID("r1") != "r1" {
		t.Error("short ids")
	}
	if joinInts([]int{1, 2}) != "1, 2" {
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
	for _, want := range []string{"1 connection ", "2 connections"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("out lacks %q: %q", want, out.String())
		}
	}
}

// A connection to a run that is gone drops its forwards, and the daemon
// with nothing left leaves.
func TestForwardToARunGone(t *testing.T) {
	r, e := forwarding(t)
	if code := r.run("forward", "r1", "3000"); code != 0 {
		t.Fatal(r.err)
	}
	e.mu.Lock()
	e.portsAt = 404
	e.mu.Unlock()
	if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(localOf(t, r, 3000))); err == nil {
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Read(make([]byte, 1))
		c.Close()
	}
	waitFor(t, func() bool { _, err := os.Stat(sockOf(t, r)); return err != nil })
	if code := r.run("forward", "logs"); code != 0 || !strings.Contains(r.out.String(), "stopped (no such run)") {
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
