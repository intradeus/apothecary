package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseProcNetFiltersListeners(t *testing.T) {
	// Columns: sl local_address rem_address st ... uid timeout inode
	// st 0A = LISTEN, 01 = ESTABLISHED. local_address = HEX_IP:HEX_PORT.
	tcp4 := "  sl  local_address rem_address   st ... \n" +
		"   0: 0100007F:1F90 00000000:0000 0A 0 0 0 0 0 111\n" + // 127.0.0.1:8080 LISTEN
		"   1: 00000000:0BB8 00000000:0000 0A 0 0 0 0 0 222\n" + // 0.0.0.0:3000 wildcard LISTEN
		"   2: 0100007F:0050 0100007F:C001 01 0 0 0 0 0 333\n" + // :80 ESTABLISHED — skip
		"   3: 0200A8C0:1538 00000000:0000 0A 0 0 0 0 0 444\n" + // 192.168.0.2:5432 non-loopback — skip
		"   4: 0100007F:0005 00000000:0000 0A 0 0 0 0 0 555\n" // 127.0.0.1:5 port<9 handled by caller
	got := parseProcNet(tcp4)
	if got[8080] != "111" || got[3000] != "222" {
		t.Errorf("expected loopback+wildcard listeners, got %v", got)
	}
	if _, ok := got[80]; ok {
		t.Error("established socket must not be reported")
	}
	if _, ok := got[5432]; ok {
		t.Error("non-loopback bind must not be reported")
	}
	// port 5 IS parsed here (the <9 filter lives in scanListeners, not the parser)
	if got[5] != "555" {
		t.Errorf("parser should return all local listeners incl :5, got %v", got)
	}

	tcp6 := " sl local_address rem_address st\n" +
		"  0: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 0A 0 0 0 0 0 666\n" + // ::1:8081
		"  1: 00000000000000000000000000000000:1F92 00000000000000000000000000000000:0000 0A 0 0 0 0 0 777\n" // :::8082 wildcard
	g6 := parseProcNet(tcp6)
	if g6[8081] != "666" || g6[8082] != "777" {
		t.Errorf("expected v6 loopback+wildcard, got %v", g6)
	}
}

func TestScanListenersFiltersAndDedupes(t *testing.T) {
	dir := t.TempDir()
	v4 := filepath.Join(dir, "tcp")
	v6 := filepath.Join(dir, "tcp6")
	// 8080 appears in both v4 and v6 (dedupe); 8079 excluded; 5 filtered <9.
	os.WriteFile(v4, []byte("h\n 0: 0100007F:1F90 0:0 0A 0 0 0 0 0 1\n 1: 0100007F:1F8F 0:0 0A 0 0 0 0 0 2\n 2: 0100007F:0005 0:0 0A 0 0 0 0 0 3\n"), 0o644)
	os.WriteFile(v6, []byte("h\n 0: 00000000000000000000000001000000:1F90 0:0 0A 0 0 0 0 0 9\n"), 0o644)

	old := procTCPPaths
	oldRoot := procRoot
	procTCPPaths = []string{v4, v6}
	procRoot = dir // no numeric pid dirs -> no process names, exercised harmlessly
	t.Cleanup(func() { procTCPPaths, procRoot = old, oldRoot })

	got := scanListeners(map[int]bool{8079: true})
	if len(got) != 1 || got[0].Port != 8080 {
		t.Fatalf("want just :8080 (8079 excluded, :5 filtered, v6 deduped), got %+v", got)
	}
}

func TestLocalOrWildcard(t *testing.T) {
	yes := []string{"0100007F", "00000000", "00000000000000000000000001000000", "00000000000000000000000000000000"}
	no := []string{"0200A8C0", "0101007F", "0A00A8C0000000000000000000000000"}
	for _, h := range yes {
		if !localOrWildcard(h) {
			t.Errorf("%s should be local/wildcard", h)
		}
	}
	for _, h := range no {
		if localOrWildcard(h) {
			t.Errorf("%s should NOT be local/wildcard", h)
		}
	}
}

// fastScan compresses intervals and points the env + proc files at fixtures.
func fastScan(t *testing.T) (setListeners func(hexRows string)) {
	t.Helper()
	oldPaths, oldRoot, oldScan, oldBeat, oldBase, oldCap, oldEnv :=
		procTCPPaths, procRoot, scanInterval, heartbeatInterval, backoffBase, backoffCap, runEnvPath
	dir := t.TempDir()
	tcp := filepath.Join(dir, "tcp")
	os.WriteFile(tcp, []byte("header\n"), 0o644)
	procTCPPaths = []string{tcp}
	procRoot = dir
	scanInterval = time.Millisecond
	heartbeatInterval = 30 * time.Millisecond
	backoffBase, backoffCap = time.Millisecond, 5*time.Millisecond
	runEnvPath = filepath.Join(dir, "run.env")
	t.Cleanup(func() {
		procTCPPaths, procRoot, scanInterval, heartbeatInterval, backoffBase, backoffCap, runEnvPath =
			oldPaths, oldRoot, oldScan, oldBeat, oldBase, oldCap, oldEnv
	})
	return func(hexRows string) { os.WriteFile(tcp, []byte("header\n"+hexRows), 0o644) }
}

func TestPortScanPostsOnChangeAndHeartbeat(t *testing.T) {
	setListeners := fastScan(t)
	setListeners(" 0: 0100007F:1F90 0:0 0A 0 0 0 0 0 1\n") // :8080

	var mu sync.Mutex
	var bodies [][]observedPort
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok1" {
			w.WriteHeader(401)
			return
		}
		var body struct {
			Ports []observedPort `json:"ports"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body.Ports)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "tok1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scanLoop(ctx, map[int]bool{}); close(done) }()

	// First report (initial), then a change adds :9000, then quiet -> heartbeat.
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(bodies) >= 1 })
	setListeners(" 0: 0100007F:1F90 0:0 0A 0 0 0 0 0 1\n 1: 0100007F:2328 0:0 0A 0 0 0 0 0 2\n")
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies) >= 2 && len(bodies[len(bodies)-1]) == 2
	})
	before := countReports(&mu, &bodies)
	// No change now: within a couple heartbeats we should still see new POSTs.
	waitFor(t, func() bool { return countReports(&mu, &bodies) > before })

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if bodies[0][0].Port != 8080 {
		t.Errorf("first report = %+v, want :8080", bodies[0])
	}
}

func TestPortScanRereadsEnvOn401(t *testing.T) {
	setListeners := fastScan(t)
	setListeners(" 0: 0100007F:1F90 0:0 0A 0 0 0 0 0 1\n")

	var mu sync.Mutex
	accepted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		accepted = true
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "stale")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scanLoop(ctx, map[int]bool{}); close(done) }()

	time.Sleep(10 * time.Millisecond)
	writeTestEnv(t, srv.URL, "fresh") // rotate; scanner re-reads run.env each report
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return accepted })
	cancel()
	<-done
}

// testSingleton is a singleton address for one test: a socket file in a
// short temp dir (sun_path is ~104 bytes), not the production abstract name,
// so a killed shim leaves nothing in the source tree and two runs never meet.
func testSingleton(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "singleton.sock")
}

func TestPortScanSingletonGuard(t *testing.T) {
	addr := testSingleton(t)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer ln.Close()
	if _, err := net.Listen("unix", addr); err == nil {
		t.Error("second bind on the singleton socket must fail")
	}
}

func countReports(mu *sync.Mutex, bodies *[][]observedPort) int {
	mu.Lock()
	defer mu.Unlock()
	return len(*bodies)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestParseExclude(t *testing.T) {
	got, err := parseExclude([]string{"--exclude", "8080, 9090", "--exclude", "1"})
	if err != nil || len(got) != 3 || !got[8080] || !got[9090] || !got[1] {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := parseExclude(nil); err != nil || len(got) != 0 {
		t.Errorf("nothing = %v, %v", got, err)
	}
	for _, args := range [][]string{
		{"8080"},
		{"--exclude"},
		{"--exclude", "http"},
		{"--exclude", "0"},
		{"--exclude", "65536"},
		{"--exclude", "8080,"},
	} {
		if got, err := parseExclude(args); err == nil {
			t.Errorf("%v = %v, want an error", args, got)
		}
	}
}

// A port is named after the process holding its socket, found through /proc;
// what cannot be read is skipped and the port still reports.
func TestProcessNames(t *testing.T) {
	root := t.TempDir()
	proc := func(pid, comm string, fds map[string]string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, pid, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		if comm != "" {
			if err := os.WriteFile(filepath.Join(root, pid, "comm"), []byte(comm+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for fd, link := range fds {
			if err := os.Symlink(link, filepath.Join(root, pid, "fd", fd)); err != nil {
				t.Fatal(err)
			}
		}
	}
	proc("100", "node", map[string]string{"3": "socket:[111]", "4": "pipe:[9]"})
	proc("200", "", map[string]string{"3": "socket:[222]"}) // no comm: gone mid-scan
	proc("300", "postgres", map[string]string{"7": "socket:[333]", "8": "socket:[333]"})
	proc("350", "worker", map[string]string{"7": "socket:[333]"})          // forked: the first holder names it
	if err := os.MkdirAll(filepath.Join(root, "400"), 0o755); err != nil { // no fd dir: not ours to read
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "500"), nil, 0o644); err != nil { // not a directory
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sys"), 0o755); err != nil { // not a pid
		t.Fatal(err)
	}
	// A regular file under fd/ is not a link.
	if err := os.WriteFile(filepath.Join(root, "100", "fd", "9"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })

	got := processNames(map[int]string{8080: "111", 5432: "333", 9000: "222"})
	if got[8080] != "node" || got[5432] != "postgres" || got[9000] != "" {
		t.Errorf("names = %v", got)
	}
	// Everything found: the walk stops there.
	if got := processNames(map[int]string{8080: "111"}); got[8080] != "node" {
		t.Errorf("names = %v", got)
	}
	procRoot = filepath.Join(root, "missing")
	if got := processNames(map[int]string{8080: "111"}); len(got) != 0 {
		t.Errorf("no /proc = %v", got)
	}
}

// A report is capped, an unreadable table skipped, and the rows the kernel
// might write that are not listeners on a port never surface.
func TestScanListenersEdges(t *testing.T) {
	dir := t.TempDir()
	var table strings.Builder
	table.WriteString("header\n")
	for p := 1000; p < 1005; p++ {
		table.WriteString(" 0: 0100007F:" + strings.ToUpper(strconv.FormatInt(int64(p), 16)) + " 0:0 0A 0 0 0 0 0 1\n")
	}
	tcp := filepath.Join(dir, "tcp")
	if err := os.WriteFile(tcp, []byte(table.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPaths, oldRoot, oldMax := procTCPPaths, procRoot, maxReportPorts
	procTCPPaths, procRoot, maxReportPorts = []string{filepath.Join(dir, "gone"), tcp}, dir, 3
	t.Cleanup(func() { procTCPPaths, procRoot, maxReportPorts = oldPaths, oldRoot, oldMax })

	got := scanListeners(nil)
	if len(got) != 3 || got[0].Port != 1000 || got[2].Port != 1002 {
		t.Errorf("capped report = %+v", got)
	}

	rows := "h\n" +
		" 0: 0100007F 0:0 0A 0 0 0 0 0 1\n" + // no port
		" 1: 0100007F:ZZZZ 0:0 0A 0 0 0 0 0 2\n" + // not hex
		" 2: 0100007F:0000 0:0 0A 0 0 0 0 0 3\n" + // port 0
		" 3: 0100007F:1F90 0:0 0A\n" // too few columns
	if got := parseProcNet(rows); len(got) != 0 {
		t.Errorf("bad rows parsed = %v", got)
	}
}

func TestLocalOrWildcardMappedAndOddLengths(t *testing.T) {
	for _, h := range []string{
		"0000000000000000FFFF00000100007F", // ::ffff:127.0.0.1
		"0000000000000000FFFF000000000000", // ::ffff:0.0.0.0
		"00000000000000000000FFFF0100007F",
	} {
		if !localOrWildcard(h) {
			t.Errorf("%s should be local", h)
		}
	}
	for _, h := range []string{"0000000000000000FFFF00000200A8C0", "0100", ""} {
		if localOrWildcard(h) {
			t.Errorf("%q should not be local", h)
		}
	}
}

// A report that fails to arrive is retried; one the engine refuses waits for
// the next heartbeat rather than hammering it.
func TestPortScanBacksOffAndKeepsGoing(t *testing.T) {
	setListeners := fastScan(t)
	setListeners(" 0: 0100007F:1F90 0:0 0A 0 0 0 0 0 1\n")
	var reports atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// No run.env yet: the engine is not reachable, and the scanner waits.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scanLoop(ctx, nil); close(done) }()
	time.Sleep(10 * time.Millisecond)
	if n := reports.Load(); n != 0 {
		t.Fatalf("reports with no run.env = %d", n)
	}
	writeTestEnv(t, srv.URL, "t")
	waitFor(t, func() bool { return reports.Load() >= 2 })
	cancel()
	<-done
}

func TestRunPortScanRefusesBadArguments(t *testing.T) {
	if r := shim(t, "", "portscan", "--nope"); r.code != 2 || !strings.Contains(r.stderr, "portscan: unknown arg") {
		t.Errorf("bad arg = %+v", r)
	}
}

// A scanner already holding the singleton is left to it.
func TestRunPortScanIsASingleton(t *testing.T) {
	addr := testSingleton(t)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cmd := shimCmd(t, "portscan")
	cmd.Env = append(cmd.Env, shimSingleton+"="+addr)
	if r := runShim(t, "", cmd); r.code != 0 {
		t.Errorf("a second scanner = %+v", r)
	}
}

// The daemon as the provisioner spawns it: it reports the listeners it finds,
// less the relay's own, to the run the env file names.
func TestRunPortScanReports(t *testing.T) {
	dir := t.TempDir()
	tcp := filepath.Join(dir, "tcp")
	rows := "h\n 0: 0100007F:1F90 0:0 0A 0 0 0 0 0 1\n 1: 0100007F:1F91 0:0 0A 0 0 0 0 0 2\n"
	if err := os.WriteFile(tcp, []byte(rows), 0o644); err != nil {
		t.Fatal(err)
	}
	type report struct{ path, auth, body string }
	got := make(chan report, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body strings.Builder
		_, _ = io.Copy(&body, r.Body)
		select {
		case got <- report{r.URL.Path, r.Header.Get("Authorization"), body.String()}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	env := filepath.Join(dir, "run.env")
	content := "APOTHECARY_RUN_ID=run1\nAPOTHECARY_RUN_TOKEN=tok\nAPOTHECARY_ENGINE_URL=" + srv.URL + "\n"
	if err := os.WriteFile(env, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "portscan", "--exclude", "8081")
	cmd.Env = append(cmd.Env, shimRunEnv+"="+env, shimProcNet+"="+tcp, shimFastSupport+"=1",
		shimSingleton+"="+testSingleton(t))
	startShim(t, cmd)
	select {
	case r := <-got:
		if r.path != "/api/runs/run1/observed-ports" || r.auth != "Bearer tok" || r.body != `{"ports":[{"port":8080}]}` {
			t.Errorf("report = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no report reached the engine")
	}
}
