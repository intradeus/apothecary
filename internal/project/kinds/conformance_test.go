//go:build conformance

package kinds

// The harness conformance tests: the real pinned binaries of the tools bundle
// against exactly what the engine hands them — the native config and auth
// files the kinds generate, the env, the engine endpoints they call. Only the
// harness itself can say it accepts them: its config schemas, auth files and
// wire protocols are its own, versioned with it (docs/model-providers-plan.md).
//
// Not part of `go test ./...`: they need the bundle, which holds Linux
// binaries whose wrappers name its mount point. `task test:integration:harness`
// builds the bundle the engine expects and runs them in a Linux container:
//
//	go test -tags conformance -run Conformance ./internal/project/kinds/
//
// With the tag, a missing or different bundle FAILS: these tests never skip.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/workspace"
)

// toolsDir is where every workspace mounts the tools bundle (the devcontainer
// provisioner's bind mount); the bundle's wrappers exec by that path, so the
// tests need it there too. The glibc build: the test container is Debian.
const toolsDir = "/opt/apothecary/tools"

// toolsBin is the bundle's binary name, failing unless the bundle is the one
// this engine expects (its TOOLS_HASH is the engine's own ToolsHash).
func toolsBin(t *testing.T, name string) string {
	t.Helper()
	hash, err := os.ReadFile(filepath.Join(toolsDir, "TOOLS_HASH"))
	if err != nil {
		t.Fatalf("no tools bundle at %s (%v): run `task test:integration:harness`", toolsDir, err)
	}
	if got, want := strings.TrimSpace(string(hash)), workspace.ToolsHash(); got != want {
		t.Fatalf("the tools bundle at %s is %s, this engine expects %s: rebuild it (`task test:integration:harness`)", toolsDir, got, want)
	}
	bin := filepath.Join(toolsDir, "gnu", "bin", name)
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the tools bundle has no %s: %v", name, err)
	}
	return bin
}

// wantVersion fails unless `bin --version` names version.
func wantVersion(t *testing.T, bin, version string) {
	t.Helper()
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), version) {
		t.Fatalf("%s --version = %q (%v), want %s: the bundle and the kind's pin disagree", filepath.Base(bin), strings.TrimSpace(string(out)), err, version)
	}
}

// workspaceHome is a fake workspace whose user's home is a real directory:
// the kinds probe $HOME through the env (wshome) and write their files there,
// and materialize lays those files on disk for the binary to read.
func workspaceHome(t *testing.T) (*fake.EnvProvisioner, envport.EnvID, string) {
	t.Helper()
	home := t.TempDir()
	envp := fake.NewEnvProvisioner()
	envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if cmd.Script == `printf %s "$HOME"` {
			return envport.ExecResult{Output: home}, nil
		}
		return envport.ExecResult{}, nil
	}
	id, err := envp.Provision(context.Background(), envport.EnvSpec{RunID: "conformance"})
	if err != nil {
		t.Fatal(err)
	}
	return envp, id, home
}

// materialize writes the files the engine put in the workspace user's home to
// disk, returning their paths relative to it. Files elsewhere (/apothecary/…)
// are the engine's own, not the harness's.
func materialize(t *testing.T, envp *fake.EnvProvisioner, id envport.EnvID, home string) []string {
	t.Helper()
	var out []string
	for p, data := range envp.Env(id).Files {
		rel, ok := strings.CutPrefix(p, home+"/")
		if !ok {
			continue
		}
		dst := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatal(err)
		}
		out = append(out, path.Clean(rel))
	}
	return out
}

// harnessEnv is a clean environment for a harness in home: nothing of the
// test process's own (no stray key, no host config) reaches it.
func harnessEnv(home string, extra ...string) []string {
	return append([]string{
		"HOME=" + home, "PATH=" + filepath.Join(toolsDir, "gnu", "bin") + ":/usr/local/bin:/usr/bin:/bin",
		"TMPDIR=" + os.TempDir(), "NO_COLOR=1", "TERM=dumb",
	}, extra...)
}

// runBin runs bin with args in dir, under env, for at most two minutes.
func runBin(t *testing.T, env []string, dir, bin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env, cmd.Dir = env, dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fakeJWT is an unsigned JWT carrying claims: the harnesses decode tokens
// (expiry, plan, account) but cannot verify a vendor's signature offline.
func fakeJWT(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(claims) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// --- an ACP client, as the workspace sidecar is one ---------------------------

// acpClient drives a harness over ACP on stdio (newline-delimited JSON-RPC),
// started as the engine starts it: the profile's Command, through sh.
type acpClient struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *strings.Builder

	mu      sync.Mutex
	nextID  int
	waiting map[int]chan acpReply
	updates []json.RawMessage
	exited  chan struct{}
}

type acpReply struct {
	Result json.RawMessage
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
}

func startACP(t *testing.T, command string, env []string, dir string) *acpClient {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exec "+command)
	cmd.Env, cmd.Dir = env, dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &acpClient{t: t, cmd: cmd, stdin: stdin, stderr: &strings.Builder{}, waiting: map[int]chan acpReply{}, exited: make(chan struct{})}
	cmd.Stderr = &lockedWriter{mu: &c.mu, w: c.stderr}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", command, err)
	}
	go c.read(stdout)
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-c.exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-c.exited
		}
		if t.Failed() {
			t.Logf("%s stderr:\n%s", command, tail(c.stderrText(), 4000))
		}
	})
	return c
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (c *acpClient) stderrText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

func (c *acpClient) read(stdout io.Reader) {
	defer func() { _ = c.cmd.Wait(); close(c.exited) }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			acpReply
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.answer(m.ID, m.Method, m.Params)
		case m.Method == "session/update":
			c.mu.Lock()
			c.updates = append(c.updates, m.Params)
			c.mu.Unlock()
		case m.Method == "":
			var id int
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.waiting[id]
			delete(c.waiting, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m.acpReply
			}
		}
	}
}

// answer replies to the agent's own requests: a permission is granted once
// (the sidecar auto-approves too); nothing else is offered — the sidecar
// declares no fs or terminal.
func (c *acpClient) answer(id json.RawMessage, method string, params json.RawMessage) {
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	if method == "session/request_permission" {
		var p struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &p)
		for _, o := range p.Options {
			if strings.HasPrefix(o.Kind, "allow") {
				reply["result"] = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}
				break
			}
		}
	}
	if reply["result"] == nil {
		reply["error"] = map[string]any{"code": -32601, "message": "method not found: " + method}
	}
	c.write(reply)
}

func (c *acpClient) write(msg map[string]any) {
	b, _ := json.Marshal(msg)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.stdin.Write(append(b, '\n'))
}

// call sends one request and waits for its reply (or the agent's exit).
func (c *acpClient) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan acpReply, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("%s: %d %s %s", method, r.Error.Code, r.Error.Message, r.Error.Data)
		}
		return r.Result, nil
	case <-c.exited:
		return nil, fmt.Errorf("%s: the agent exited", method)
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: no reply in %s", method, timeout)
	}
}

// mustCall is call, failing the test on an error.
func (c *acpClient) mustCall(method string, params any, timeout time.Duration) json.RawMessage {
	c.t.Helper()
	res, err := c.call(method, params, timeout)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

// initialize opens the connection as the sidecar does (workspace/shim acp.go).
func (c *acpClient) initialize() {
	c.t.Helper()
	c.mustCall("initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}, time.Minute)
}

// acpSession is what session/new answers that the sidecar reads: the modes
// and models, offered either way ACP spells them (workspace/shim
// parseSessionCaps).
type acpSession struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	Models *struct {
		AvailableModels []struct {
			ModelID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []struct {
		ID       string `json:"id"`
		Category string `json:"category"`
		Options  []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"configOptions"`
}

// offered lists the session's modes (kind "mode") or models ("model").
func (s acpSession) offered(kind string) []string {
	var out []string
	switch {
	case kind == "mode" && s.Modes != nil:
		for _, m := range s.Modes.AvailableModes {
			out = append(out, m.ID)
		}
	case kind == "model" && s.Models != nil:
		for _, m := range s.Models.AvailableModels {
			out = append(out, m.ModelID)
		}
	}
	for _, o := range s.ConfigOptions {
		if o.ID == kind || o.Category == kind {
			for _, v := range o.Options {
				out = append(out, v.Value)
			}
		}
	}
	return out
}

func (c *acpClient) newSession(cwd string) acpSession {
	c.t.Helper()
	var s acpSession
	res := c.mustCall("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, 2*time.Minute)
	if err := json.Unmarshal(res, &s); err != nil || s.SessionID == "" {
		c.t.Fatalf("session/new = %s (%v)", res, err)
	}
	return s
}

// set selects the session's mode or model as the sidecar does: the dedicated
// method (session/set_mode, session/set_model), else the config option.
func (c *acpClient) set(s acpSession, kind, value string) {
	c.t.Helper()
	_, err := c.call("session/set_"+kind, map[string]any{"sessionId": s.SessionID, kind + "Id": value}, time.Minute)
	if err == nil {
		return
	}
	for _, o := range s.ConfigOptions {
		if o.ID == kind || o.Category == kind {
			if _, cerr := c.call("session/set_config_option", map[string]any{"sessionId": s.SessionID, "configId": o.ID, "value": value}, time.Minute); cerr == nil {
				return
			}
		}
	}
	c.t.Fatalf("set %s %q: %v (offered: %v)", kind, value, err, s.offered(kind))
}

// prompt runs one turn, returning its stop reason and the agent's text.
func (c *acpClient) prompt(sid, text string) (string, string) {
	c.t.Helper()
	c.mu.Lock()
	from := len(c.updates)
	c.mu.Unlock()
	res := c.mustCall("session/prompt", map[string]any{
		"sessionId": sid, "prompt": []any{map[string]any{"type": "text", "text": text}},
	}, 3*time.Minute)
	var r struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &r)
	c.mu.Lock()
	defer c.mu.Unlock()
	var said strings.Builder
	for _, u := range c.updates[from:] {
		var n struct {
			Update struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(u, &n) == nil && n.Update.Kind == "agent_message_chunk" {
			said.WriteString(n.Update.Content.Text)
		}
	}
	return r.StopReason, said.String()
}
