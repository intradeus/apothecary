package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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

func TestPortScanSingletonGuard(t *testing.T) {
	ln, err := net.Listen("unix", singletonAddr)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer ln.Close()
	if _, err := net.Listen("unix", singletonAddr); err == nil {
		t.Error("second bind on the abstract singleton socket must fail")
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
