package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/ports/envport"
)

// serveSock listens on a socket of env w's shim dir.
func serveSock(t *testing.T, a *Adapter, sock string) net.Listener {
	t.Helper()
	dir := filepath.Join(a.folderFor("w"), "shim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, sock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func TestSidecarAlreadyUpIsNotRestarted(t *testing.T) {
	r := &ruleRunner{}
	a := newTestAdapter(t, r)
	ln := serveSock(t, a, sidecarSock)
	// Accepts everything: sidecarUp's probe connection queues ahead of ours.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	conn, err := a.Sidecar(context.Background(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.snapshot()) != 0 {
		t.Errorf("exec on a live sidecar: %v", r.lines())
	}
	c, err := conn.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if b, err := io.ReadAll(c); err != nil || string(b) != "hi" {
		t.Errorf("read = %q, %v", b, err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestSidecarStartsWhenNothingServes(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	var once sync.Once
	var ln net.Listener
	r := &ruleRunner{rules: []rule{{match: "exec --id-label", fn: func(_ []string, stdin []byte) *response {
		if strings.Contains(string(stdin), "sidecar --daemon") {
			once.Do(func() { ln = serveSock(t, a, sidecarSock) })
		}
		return nil
	}}}}
	a.runner = r
	conn, err := a.Sidecar(context.Background(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if conn == nil || ln == nil {
		t.Fatal("no sidecar")
	}
	script := string(r.snapshot()[0].stdin)
	if !strings.Contains(script, "--listen /opt/apothecary/sidecar.sock") || !strings.Contains(script, `--repo "$HOME/repo"`) {
		t.Errorf("script = %q", script)
	}
}

// Two callers racing on a cold workspace start one sidecar.
func TestSidecarRaceStartsOne(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	var mu sync.Mutex
	starts := 0
	a.runner = &ruleRunner{rules: []rule{{match: "exec --id-label", fn: func(_ []string, stdin []byte) *response {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(string(stdin), "sidecar --daemon") {
			starts++
			serveSock(t, a, sidecarSock)
		}
		return nil
	}}}}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = a.Sidecar(context.Background(), "w")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 {
		t.Errorf("sidecar started %d times", starts)
	}
}

func TestSidecarStartFailures(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "exec --id-label", resp: response{code: 3, stdout: "no shim"}}}})
	if _, err := a.Sidecar(ctx, "w"); err == nil || !strings.Contains(err.Error(), "start sidecar in w: exit 3") {
		t.Errorf("exit = %v", err)
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("exec --id-label")}})
	if _, err := a.Sidecar(ctx, "w"); err == nil {
		t.Error("spawn failure swallowed")
	}
	// It "started" and no socket ever came: the caller's deadline ends the wait.
	a = newTestAdapter(t, &ruleRunner{})
	cctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := a.Sidecar(cctx, "w"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no socket = %v", err)
	}
}

// noSockHost is the test host with a waitSock that gives up at once, as a
// host does when the socket never comes.
type noSockHost struct{ *testHost }

func (noSockHost) waitSock(context.Context, string, string) error {
	return errors.New("never appeared")
}

func TestSSH(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{})
	serveSock(t, a, sidecarSock)
	serveSock(t, a, sshSock)
	conn, err := a.SSH(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	c, err := conn.Dial(ctx)
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	c.Close()
}

func TestSSHFromAnOldSidecarSaysSo(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{})
	a.host = noSockHost{hostOf(t, a)}
	serveSock(t, a, sidecarSock)
	_, err := a.SSH(context.Background(), "w")
	if err == nil || !strings.Contains(err.Error(), "needs a rebuild") {
		t.Errorf("err = %v", err)
	}
}

func TestSSHNeedsItsSidecar(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{rules: []rule{{match: "exec --id-label", resp: response{code: 1}}}})
	if _, err := a.SSH(context.Background(), "w"); err == nil {
		t.Error("ssh with no sidecar")
	}
}

func TestExposePortFailures(t *testing.T) {
	ctx := context.Background()
	cid := rule{match: "ps -q --filter label=apothecary.env", resp: response{stdout: "cid1\n"}}
	t.Run("no container", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{})
		if _, err := a.ExposePort(ctx, "w", 3000); err == nil || !strings.Contains(err.Error(), "no running container") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("relay exit", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{fail("relay --reverse", 1, "taken"), cid}})
		if _, err := a.ExposePort(ctx, "w", 3000); err == nil || !strings.Contains(err.Error(), "expose port 3000 in w: exit 1: taken") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("relay spawn", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("relay --reverse"), cid}})
		if _, err := a.ExposePort(ctx, "w", 3000); err == nil {
			t.Error("spawn failure swallowed")
		}
	})
	t.Run("socket never appears", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{cid}})
		a.host = noSockHost{hostOf(t, a)}
		if _, err := a.ExposePort(ctx, "w", 3000); err == nil || !strings.Contains(err.Error(), "relay socket never appeared") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the caller gives up", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{cid}})
		cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		if _, err := a.ExposePort(cctx, "w", 3000); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestStartRelayNeedsAContainer(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("ps -q")}})
	if err := a.startRelay(context.Background(), "w"); err == nil {
		t.Error("relay with no container")
	}
	a = newTestAdapter(t, &ruleRunner{rules: []rule{fail("ps -q", 1, "")}})
	if _, err := a.containerID(context.Background(), "w"); err == nil {
		t.Error("a failed ps found a container")
	}
}

func TestRelayTargetWhenPsFails(t *testing.T) {
	r := &ruleRunner{rules: []rule{
		{match: "com.docker.compose.project\"", resp: response{stdout: "proj\n"}},
		fail("ps -q --filter label=com.docker.compose.project=proj", 1, ""),
	}}
	a := newTestAdapter(t, r)
	if got := a.relayTarget(context.Background(), "w", "cid", 5432); got != "127.0.0.1" {
		t.Errorf("target = %q", got)
	}
}

func TestInspectFieldUnknowns(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]rule{
		"exit":     fail("inspect", 1, ""),
		"spawn":    spawnFail("inspect"),
		"no value": {match: "inspect", resp: response{stdout: "<no value>\n"}},
	} {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{r}})
		if got := a.inspectField(ctx, "w", "c", "{{.X}}"); got != "" {
			t.Errorf("%s = %q", name, got)
		}
	}
}

func TestScanSiblingPortsEdges(t *testing.T) {
	ctx := context.Background()
	proj := rule{match: `com.docker.compose.project"`, resp: response{stdout: "proj\n"}}
	cid := rule{match: "ps -q --filter label=apothecary.env", resp: response{stdout: "cid1\n"}}
	t.Run("no container", func(t *testing.T) {
		if got := newTestAdapter(t, &ruleRunner{}).ScanSiblingPorts(ctx, "w"); got != nil {
			t.Errorf("got %v", got)
		}
	})
	t.Run("not compose", func(t *testing.T) {
		if got := newTestAdapter(t, &ruleRunner{rules: []rule{cid}}).ScanSiblingPorts(ctx, "w"); got != nil {
			t.Errorf("got %v", got)
		}
	})
	t.Run("ps fails", func(t *testing.T) {
		a := newTestAdapter(t, &ruleRunner{rules: []rule{cid, proj, fail("ps -q --filter label=com.docker.compose.project=proj", 1, "")}})
		if got := a.ScanSiblingPorts(ctx, "w"); got != nil {
			t.Errorf("got %v", got)
		}
	})
	t.Run("filters and routes", func(t *testing.T) {
		table := func(ports ...int) response {
			out := "sl local rem st\n"
			for _, p := range ports {
				out += fmt.Sprintf("0: 00000000:%04X 00000000:0000 0A\n", p)
			}
			return response{stdout: out}
		}
		a := newTestAdapter(t, &ruleRunner{rules: []rule{cid, proj,
			{match: "ps -q --filter label=com.docker.compose.project=proj", resp: response{stdout: "cid1\nsibA\nsibB\nsibC\nsibD\n"}},
			{match: `inspect sibA --format {{index .Config.Labels "com.docker.compose.service"}}`, resp: response{stdout: "db\n"}},
			{match: `inspect sibB --format {{index .Config.Labels "com.docker.compose.service"}}`, resp: response{stdout: "cache\n"}},
			{match: `inspect sibC --format {{index .Config.Labels "com.docker.compose.service"}}`, resp: response{stdout: "\n"}},
			{match: `inspect sibD --format {{index .Config.Labels "com.docker.compose.service"}}`, resp: response{stdout: "bare\n"}},
			{match: "exec sibA", resp: table(5432, 7, 8080, 6379)},
			{match: "exec sibB", resp: table(6379, 11211)},
			fail("exec sibD", 127, "no sh"),
		}})
		a.cfg.EnginePort = 8080
		got := a.ScanSiblingPorts(ctx, "w")
		want := []envport.ObservedPort{{Port: 5432, Service: "db"}, {Port: 6379, Service: "db"}, {Port: 11211, Service: "cache"}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("ports = %+v, want %+v (no system port, no bridge port, first sibling wins)", got, want)
		}
		if a.scannedSibling("w", 11211) != "cache" || a.scannedSibling("w", 7) != "" {
			t.Errorf("routes = %v", a.sibPorts["w"])
		}
		if a.scannedSibling("x", 5432) != "" {
			t.Error("routes leaked to another env")
		}
	})
}

func TestParseWildcardListenersEdges(t *testing.T) {
	table := strings.Join([]string{
		"  sl  local_address rem_address   st",
		"0: 00000000:1F90 00000000:0000 0A",               // 8080
		"1: 00000000:1F90 00000000:0000 0A",               // again: once
		"2: 0100007F:1F91 00000000:0000 0A",               // loopback
		"3: 00000000:1F92 00000000:0000 01",               // established
		"4: nocolon 00000000:0000 0A",                     // malformed
		"5: 00000000:ZZZZ 00000000:0000 0A",               // bad port
		"6: 00000000:0000 00000000:0000 0A",               // port 0
		"7: 000000:1F93 00000000:0000 0A",                 // odd address length
		"8: 00000000000000000000000000000000:0050 0:0 0A", // :: on 80
	}, "\n")
	got := parseWildcardListeners(table)
	if len(got) != 2 || got[0] != 8080 || got[1] != 80 {
		t.Errorf("ports = %v", got)
	}
}

func TestStartEditorSpawnFailure(t *testing.T) {
	a := newTestAdapter(t, &ruleRunner{rules: []rule{spawnFail("exec --id-label")}})
	if err := a.StartEditor(context.Background(), "w", 13337, envport.EditorSettings{}); err == nil {
		t.Error("spawn failure swallowed")
	}
}

func TestCodeServerSettings(t *testing.T) {
	if got := codeServerSettingsJSON(envport.EditorSettings{}); got != nil {
		t.Errorf("nothing set = %s: must leave the user's own settings alone", got)
	}
	got := string(codeServerSettingsJSON(envport.EditorSettings{Theme: "Dark", FontSize: 13, TabSize: 2, FormatOnSave: true, WordWrap: true, TrustWorkspace: true}))
	for _, want := range []string{`"workbench.colorTheme": "Dark"`, `"editor.fontSize": 13`, `"editor.tabSize": 2`, `"editor.formatOnSave": true`,
		`"editor.wordWrap": "on"`, `"security.workspace.trust.enabled": false`, `"security.workspace.trust.startupPrompt": "never"`,
		`"security.workspace.trust.banner": "never"`, `"security.workspace.trust.emptyWindow": true`, `"security.workspace.trust.untrustedFiles": "open"`} {
		if !strings.Contains(got, want) {
			t.Errorf("settings missing %s:\n%s", want, got)
		}
	}
	only := string(codeServerSettingsJSON(envport.EditorSettings{TabSize: 4}))
	if strings.Contains(only, "fontSize") || strings.Contains(only, "trust") {
		t.Errorf("unset keys emitted: %s", only)
	}
}

func TestEditorScriptGallery(t *testing.T) {
	s := editorScript(13337, nil, nil, "10.0.0.2")
	if !strings.Contains(s, "export EXTENSIONS_GALLERY=") || !strings.Contains(s, "http://10.0.0.2:5002/vscode/gallery") {
		t.Errorf("no gallery: %s", s)
	}
	if s := editorScript(13337, nil, nil, ""); strings.Contains(s, "EXTENSIONS_GALLERY") {
		t.Errorf("gallery without a gateway: %s", s)
	}
	if s := editorScript(13337, nil, nil, ""); strings.Contains(s, "settings.json") {
		t.Errorf("settings.json touched with nothing to write: %s", s)
	}
	settings := []byte(`{"a":"it's"}`)
	if s := editorScript(13337, settings, []string{"pub.ext"}, ""); !strings.Contains(s, `'{"a":"it'\''s"}'`) || !strings.Contains(s, "want='pub.ext'") {
		t.Errorf("quoting: %s", s)
	}
}
