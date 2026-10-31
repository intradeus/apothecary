package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/browsermcp"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/browserport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

const mcpFlowDoc = `name: shots
version: 5
parameters: { harness: fake, model: fake/model }
mcp_servers:
  - { name: browser, builtin: browser }
  - name: figma
    url: https://mcp.figma.test/mcp
    headers: { Authorization: "Bearer ${secrets.figma}" }
  - name: db
    command: [dbmcp, --ro]
    env: { DSN: "pg://u:${secrets.db_pass}@db/app", MODE: ro }
agents:
  - { name: shooter, prompt: Take screenshots., mcp: [browser, figma] }
  - { name: querier, prompt: Query., mcp: [db] }
  - { name: plain, prompt: Plain. }
main:
  - { id: shoot, type: agent, harness: $harness, model: $model, agent: shooter }
  - { id: query, type: agent, harness: $harness, model: $model, agent: querier }
  - { id: other, type: agent, harness: $harness, model: $model, agent: plain }
`

// fakeBrowser stands in for internal/adapters/browser.
type fakeBrowser struct {
	mu      sync.Mutex
	warmed  []string
	session browserport.BrowserSession
	serve   func(browserport.BrowserSession, io.ReadWriter) error
}

func (b *fakeBrowser) Session(_ context.Context, s browserport.BrowserSession, stdio io.ReadWriter) error {
	b.mu.Lock()
	b.session = s
	b.mu.Unlock()
	if b.serve != nil {
		return b.serve(s, stdio)
	}
	return nil
}
func (b *fakeBrowser) Warm(project, _ string) {
	b.mu.Lock()
	b.warmed = append(b.warmed, project)
	b.mu.Unlock()
}
func (b *fakeBrowser) Status(context.Context, string) (browserport.BrowserStatus, error) {
	return browserport.BrowserStatus{State: "running"}, nil
}

type secretMap map[string]string

func (s secretMap) ProjectSecrets(context.Context, string) (map[string]string, error) { return s, nil }

// Run creation refuses a flow whose MCP servers need what the project does
// not have — naming the server and what to do.
func TestRunRefusedForMCPServersTheProjectCannotServe(t *testing.T) {
	for _, c := range []struct {
		name    string
		secrets map[string]string
		browser bool
		wired   bool
		want    string
	}{
		{"missing secret", map[string]string{"figma": "figd_x"}, true, true, `mcp server "db" reads project secret "db_pass", which the project does not have`},
		{"browser off", map[string]string{"figma": "f", "db_pass": "p"}, false, true, "turn on the project's browser in Workshop → Settings"},
		{"no browser here", map[string]string{"figma": "f", "db_pass": "p"}, true, false, "this apothecary runs no project browser"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarnessWith(t, func(_ *harness, d *Deps) {
				if c.wired {
					d.Browser = &fakeBrowser{}
				}
			}, mustFlow(t, mcpFlowDoc))
			h.res.Secrets, h.res.Browser = c.secrets, c.browser
			_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "shots", testProject)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// Each agent's session gets its own servers, resolved when its stage starts:
// secrets as the project holds them THEN (rotated since creation), the
// builtin browser as the workspace's shim command — and no secret value
// reaches the log, even when an agent's failure prints one.
func TestAgentSessionsGetTheirMCPServersResolved(t *testing.T) {
	b := &fakeBrowser{}
	h := newHarnessWith(t, func(_ *harness, d *Deps) {
		d.Browser = b
		d.ProjectSecrets = secretMap{"figma": "figd_rotated_1", "db_pass": "pass-rotated-1"}
	}, mustFlow(t, mcpFlowDoc))
	h.res.Secrets = map[string]string{"figma": "figd_at_creation", "db_pass": "pass-at-creation"}
	h.res.Browser = true
	got := map[string][]agentport.MCPServer{}
	var mu sync.Mutex
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		mu.Lock()
		got[call.Spec.Agent] = call.In.MCPServers
		mu.Unlock()
		if call.Spec.Agent == "querier" {
			return agentport.AgentResult{Output: "connect pg://u:pass-rotated-1@db/app: refused"}, errors.New("db said pass-rotated-1 is wrong")
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "shots", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if !slices.Equal(b.warmed, []string{testProject}) {
		t.Errorf("warmed = %v, want the project's browser warmed at creation", b.warmed)
	}
	shooter := got["shooter"]
	if len(shooter) != 2 || shooter[0].Name != "browser" || !slices.Equal(shooter[0].Command, browsermcp.Command) ||
		shooter[1].URL != "https://mcp.figma.test/mcp" || shooter[1].Headers["Authorization"] != "Bearer figd_rotated_1" {
		t.Errorf("shooter's servers = %+v", shooter)
	}
	q := got["querier"]
	if len(q) != 1 || !slices.Equal(q[0].Command, []string{"dbmcp", "--ro"}) ||
		q[0].Env["DSN"] != "pg://u:pass-rotated-1@db/app" || q[0].Env["MODE"] != "ro" {
		t.Errorf("querier's servers = %+v", q)
	}
	if p := got["plain"]; p != nil {
		t.Errorf("an agent listing no server got %+v", p)
	}
	data, _ := json.Marshal(h.events(t, runID))
	for _, secret := range []string{"pass-rotated-1", "figd_rotated_1", "pass-at-creation", "figd_at_creation"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the log carries secret %q", secret)
		}
	}
}

// A secret deleted after the run was created fails the stage that needs it,
// before its session opens.
func TestStageFailsOnASecretGoneSinceCreation(t *testing.T) {
	h := newHarnessWith(t, func(_ *harness, d *Deps) {
		d.Browser = &fakeBrowser{}
		d.ProjectSecrets = secretMap{"db_pass": "p-123456"}
	}, mustFlow(t, mcpFlowDoc))
	h.res.Secrets = map[string]string{"figma": "f-123456", "db_pass": "p-123456"}
	h.res.Browser = true
	var ran atomic.Bool
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"shooter": func(fake.RunCall) (agentport.AgentResult, error) {
			ran.Store(true)
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "shots", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	sf, ok := findPayload[*event.StageFailed](h.events(t, runID))
	if !ok || sf.StageID != "shoot" || !strings.Contains(sf.Error, `project secret "figma" is not set`) {
		t.Fatalf("stage_failed = %+v", sf)
	}
	if ran.Load() {
		t.Error("the agent ran without its server")
	}
}

// The browser session reaches the run's workspace ports through the
// provisioner's exposer, exposing each port once.
func TestBrowserSessionDialsTheRunsWorkspace(t *testing.T) {
	b := &fakeBrowser{}
	b.serve = func(s browserport.BrowserSession, _ io.ReadWriter) error {
		for range 3 {
			c, err := s.Dial(context.Background(), 3000)
			if err != nil {
				return err
			}
			c.Close()
		}
		return nil
	}
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Browser = b }, mustFlow(t, mcpFlowDoc))
	h.res.Secrets = map[string]string{"figma": "f-123456", "db_pass": "p-123456"}
	h.res.Browser = true
	exp := &countingExposer{EnvProvisioner: h.envp}
	h.res.Envp = exp
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "shots", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	sessions := browsermcp.New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, b)
	if err := sessions.Serve(context.Background(), runID, nil); err != nil {
		t.Fatal(err)
	}
	if b.session.Project != testProject || b.session.Run != runID {
		t.Errorf("session = %+v", b.session)
	}
	if exp.exposed.Load() != 1 {
		t.Errorf("port exposed %d times, want once", exp.exposed.Load())
	}
	h.res.Browser = false
	if err := sessions.Serve(context.Background(), runID, nil); err == nil || !strings.Contains(err.Error(), "browser is off") {
		t.Errorf("browser off: err = %v", err)
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}
}

type countingExposer struct {
	envport.EnvProvisioner
	exposed atomic.Int32
}

func (c *countingExposer) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	c.exposed.Add(1)
	return pipeConn{}, nil
}
func (c *countingExposer) AppPorts(context.Context, envport.EnvID) []int { return nil }

type pipeConn struct{}

func (pipeConn) Dial(context.Context) (net.Conn, error) {
	a, b := net.Pipe()
	go b.Close()
	return a, nil
}
func (pipeConn) Close() error { return nil }
