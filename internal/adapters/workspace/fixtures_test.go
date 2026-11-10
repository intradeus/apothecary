package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/egressgw/policyfile"
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
// sandbox socket in place, ready for a Provision.
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
	linkErr   error
	policy    policyfile.File
	denials   []policyfile.Denial
	audit     []policyfile.AuditRecord
	bytes     int64
	cleared   int
	routes    []string
	windows   []string // "open <run>" / "close <run>": the provisioning egress window
	windowErr bool     // the window endpoints answer 500
	detached  []string
	linked    []string
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

func (g *gwFake) LinkSandbox(_ context.Context, _ egressgw.Attachment, sandbox string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.linked = append(g.linked, sandbox)
	return g.linkErr
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
