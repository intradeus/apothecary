package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/ports/envport"
)

// rule answers every CLI call whose "bin args..." contains match, first rule
// first. fn, when set, runs on the call (to create a socket, cancel a
// context...) and may replace the answer.
type rule struct {
	match string
	resp  response
	fn    func(args []string, stdin []byte) *response
}

// ruleRunner is scriptRunner keyed by what a call says instead of by its
// position, so a test states the one step it cares about. Anything no rule
// matches succeeds silently.
type ruleRunner struct {
	mu    sync.Mutex
	calls []call
	rules []rule
}

func (r *ruleRunner) Run(_ context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call{bin: bin, args: args, stdin: stdin, env: env})
	rules := r.rules
	r.mu.Unlock()
	line := bin + " " + strings.Join(args, " ")
	for _, ru := range rules {
		if !strings.Contains(line, ru.match) {
			continue
		}
		resp := ru.resp
		if ru.fn != nil {
			if got := ru.fn(args, stdin); got != nil {
				resp = *got
			}
		}
		if tee != nil && resp.stdout != "" {
			_, _ = tee.Write([]byte(resp.stdout))
		}
		return []byte(resp.stdout), []byte(resp.stderr), resp.code, resp.err
	}
	return nil, nil, 0, nil
}

func (r *ruleRunner) snapshot() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.calls...)
}

// lines is every recorded call as "bin args...".
func (r *ruleRunner) lines() []string {
	var out []string
	for _, c := range r.snapshot() {
		out = append(out, c.bin+" "+strings.Join(c.args, " "))
	}
	return out
}

func (r *ruleRunner) saw(sub string) bool {
	for _, l := range r.lines() {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// index is the position of the first call containing sub, -1 if none.
func (r *ruleRunner) index(sub string) int {
	for i, l := range r.lines() {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

// upRules are the answers a provision needs beyond the default success.
func upRules() []rule {
	return []rule{
		{match: "up --workspace-folder", resp: response{stdout: `{"outcome":"success","containerId":"cid1","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/w"}` + "\n"}},
		{match: "ps -q --filter label=apothecary.env", resp: response{stdout: "cid1\n"}},
		{match: "ps -aq --filter id=cid1", resp: response{stdout: "cid1\n"}}, // up's container is ours
	}
}

// fail makes the calls containing match exit nonzero.
func fail(match string, code int, stderr string) rule {
	return rule{match: match, resp: response{code: code, stderr: stderr}}
}

// spawnFail makes the calls containing match fail to start.
func spawnFail(match string) rule {
	return rule{match: match, resp: response{err: errors.New("spawn failed")}}
}

// armedAdapter is a test adapter whose run w has a checkout definition and a
// running sandbox, ready for a Provision.
func armedAdapter(t *testing.T, rules ...rule) (*Adapter, *ruleRunner) {
	t.Helper()
	r := &ruleRunner{rules: append(rules, upRules()...)}
	a := newTestAdapter(t, r)
	seedDevcontainer(t, a, "w")
	armSandbox(t, a, "w")
	return a, r
}

// gwFake is a gateway host and its control API in one: the egress half a
// workspace sees, with no docker in it.
type gwFake struct {
	mu        sync.Mutex
	srv       *httptest.Server
	up        bool
	ensureErr error
	policy    policyfile.File
	denials   []policyfile.Denial
	audit     []policyfile.AuditRecord
	bytes     int64
	cleared   int
	routes    []string
	windows   []string // "open <run>" / "close <run>": the provisioning egress window
	windowErr bool     // the window endpoints answer 500
	detached  []string
	att       egressgw.Attachment
}

func newGwFake(t *testing.T) *gwFake {
	t.Helper()
	g := &gwFake{att: egressgw.Attachment{GatewayIP: "10.77.0.2", Subnet: "10.77.0.0/16",
		Peer: &policyfile.Peer{PublicKey: "K", IP: "10.90.0.2"}, WGEndpoint: "10.77.0.2:51820", WGSecret: "apo-wg-w"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(g.policy) })
	mux.HandleFunc("PUT /policy", func(_ http.ResponseWriter, r *http.Request) { json.NewDecoder(r.Body).Decode(&g.policy) })
	mux.HandleFunc("PUT /runs/{run}", func(_ http.ResponseWriter, r *http.Request) { g.routes = append(g.routes, "+"+r.PathValue("run")) })
	mux.HandleFunc("DELETE /runs/{run}", func(_ http.ResponseWriter, r *http.Request) { g.routes = append(g.routes, "-"+r.PathValue("run")) })
	window := func(verb string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if g.windowErr {
				http.Error(w, "window refused", http.StatusInternalServerError)
				return
			}
			g.windows = append(g.windows, verb+" "+r.PathValue("run"))
		}
	}
	mux.HandleFunc("PUT /runs/{run}/open", window("open"))
	mux.HandleFunc("DELETE /runs/{run}/open", window("close"))
	mux.HandleFunc("GET /denials", func(w http.ResponseWriter, r *http.Request) {
		var out []policyfile.Denial
		for _, d := range g.denials {
			if d.Run == r.URL.Query().Get("run") {
				out = append(out, d)
			}
		}
		json.NewEncoder(w).Encode(policyfile.DenialsFile{Denials: out})
	})
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(policyfile.AuditFile{Records: g.audit})
	})
	mux.HandleFunc("GET /buildcache", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(policyfile.BuildCacheInfo{Bytes: g.bytes})
	})
	mux.HandleFunc("DELETE /buildcache", func(http.ResponseWriter, *http.Request) { g.cleared++ })
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gwFake) control() egressgw.Control { return egressgw.Control{BaseURL: g.srv.URL} }

func (g *gwFake) EnsureGateway(context.Context, string, string) (egressgw.Control, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.up = true
	return g.control(), g.ensureErr
}

func (g *gwFake) Control(context.Context, string) (egressgw.Control, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.control(), g.up
}

func (g *gwFake) AttachRun(context.Context, string, string, string) (egressgw.Attachment, error) {
	return g.att, nil
}

func (g *gwFake) DetachRun(_ context.Context, _, run string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.detached = append(g.detached, run)
	return nil
}

// withGateway puts the fake gateway under a, with env w on project P1.
func withGateway(t *testing.T, a *Adapter) *gwFake {
	t.Helper()
	g := newGwFake(t)
	g.up = true
	a.gw = egressgw.NewManager(g)
	a.recordGateway("w", "P1", g.att.GatewayIP)
	return g
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s: %v", filepath.Base(path), err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should be gone: %v", filepath.Base(path), err)
	}
}

func attachmentNone() egressgw.Attachment { return egressgw.Attachment{} }

// testHost is the Host the host-blind tests run on: it does what the sandbox
// pod's host does around the definition (the gateway attachment, the run
// folder, the shim dir) without a cluster. Inner calls run through the
// adapter's runner with the inner daemon's DOCKER_HOST, so a test can tell an
// inner call from an engine-side one. Knobs are set by tests that need them.
type testHost struct {
	a *Adapter

	unpriv     bool
	prepErr    error
	state      func(name string) (string, error) // nil: "running" once the run folder exists
	destroys   []string
	destroyErr error
	resets     []string
	resetErr   error
	shimErr    error
	bridged    map[envport.EnvID]bool
	bridgeErr  error
	relays     []string // "name cid"
	relayErr   error
	sampled    envport.ResourceSample
	testErr    error
	closed     bool
}

func newTestHost(a *Adapter) *testHost { return &testHost{a: a, bridged: map[envport.EnvID]bool{}} }

// innerSock is where the test host says the run's inner daemon listens.
func (h *testHost) innerSock(name string) string {
	return filepath.Join(h.a.folderFor(name), "docker.sock")
}

func (h *testHost) prepare(ctx context.Context, name string, spec envport.EnvSpec, plog func(string, ...any), detect func(context.Context, string) (Definition, error)) (prep, error) {
	a := h.a
	if h.prepErr != nil {
		return prep{}, h.prepErr
	}
	folder := a.folderFor(name)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return prep{}, err
	}
	var att egressgw.Attachment
	if a.gw != nil {
		var err error
		if att, err = a.gw.EnsureRunEgress(ctx, spec.Project, spec.ProjectName, name, policyFor(spec)); err != nil {
			return prep{}, err
		}
		a.openProvisionEgress(ctx, spec.Project, name, plog)
		a.recordGateway(name, spec.Project, att.GatewayIP)
	}
	src := a.srcDir(name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return prep{}, err
	}
	def, err := detect(ctx, src)
	if err != nil {
		return prep{}, err
	}
	shimDir := filepath.Join(folder, "shim")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return prep{}, err
	}
	return prep{src: src, shimDir: shimDir, toolsDir: filepath.Join(folder, "tools"), gwIP: att.GatewayIP, def: def}, nil
}

func (h *testHost) unprivileged() bool       { return h.unpriv }
func (h *testHost) unprivilegedHint() string { return "a runtime class with privileged off" }

func (h *testHost) runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error) {
	env := append([]string{"DOCKER_HOST=unix://" + h.innerSock(name)}, extraEnv...)
	return h.a.run(ctx, bin, args, stdin, tee, env...)
}

func (h *testHost) fs(string) sandboxFS { return osFS{} }

func (h *testHost) sandboxState(_ context.Context, name string) (string, error) {
	if h.state != nil {
		return h.state(name)
	}
	if _, err := os.Stat(h.a.folderFor(name)); err != nil {
		return "", nil
	}
	return "running", nil
}

func (h *testHost) destroySandbox(_ context.Context, name string) error {
	h.destroys = append(h.destroys, name)
	return h.destroyErr
}

func (h *testHost) resetSandbox(_ context.Context, name string) error {
	h.resets = append(h.resets, name)
	return h.resetErr
}

func (h *testHost) ensureBridge(env envport.EnvID) error {
	if h.bridgeErr != nil {
		return h.bridgeErr
	}
	h.bridged[env] = true
	return nil
}

func (h *testHost) closeBridge(env envport.EnvID) { delete(h.bridged, env) }
func (h *testHost) bridgePorts() []int            { return []int{h.a.cfg.EnginePort} }

func (h *testHost) startBridgeRelay(_ context.Context, name, cid string) error {
	h.relays = append(h.relays, name+" "+cid)
	return h.relayErr
}

func (h *testHost) engineURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(h.a.cfg.EnginePort)
}

// dial and waitSock: the workspace's sockets, under the run folder's shim dir.
func (h *testHost) dial(name, sock string) envport.PortConn {
	return unixConn{path: filepath.Join(h.a.folderFor(name), "shim", sock)}
}

func (h *testHost) waitSock(ctx context.Context, name, sock string) error {
	path := filepath.Join(h.a.folderFor(name), "shim", sock)
	for range 50 {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s never appeared", sock)
}

func (h *testHost) refreshShim(context.Context, string) error { return h.shimErr }

func (h *testHost) sample(context.Context, string) (envport.ResourceSample, error) {
	return h.sampled, nil
}

func (h *testHost) test(context.Context) error { return h.testErr }
func (h *testHost) close()                     { h.closed = true }

var _ Host = (*testHost)(nil)

// unixConn dials a unix socket per connection.
type unixConn struct{ path string }

func (u unixConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", u.path)
}

func (u unixConn) Close() error { return nil }

// hostOf is a's test host.
func hostOf(t *testing.T, a *Adapter) *testHost {
	t.Helper()
	h, ok := a.host.(*testHost)
	if !ok {
		t.Fatalf("adapter host is %T, not the test host", a.host)
	}
	return h
}
