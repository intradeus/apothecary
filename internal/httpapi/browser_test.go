package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// A project secret is write-only: set, listed by name, removed — and its
// value appears in no response, the project's included.
func TestProjectSecretsAPINeverReturnsAValue(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	const value = "figd_PLANTED-secret-value"
	resp, data := x.do(t, "PUT", "/api/projects/"+pid+"/secrets/figma", map[string]string{"value": value}, nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(data), value) || !strings.Contains(string(data), `"name":"figma"`) {
		t.Fatalf("set = %d %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "PUT", "/api/projects/"+pid+"/secrets/Figma", map[string]string{"value": "x"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad name = %d %s", resp.StatusCode, data)
	}
	for _, path := range []string{"/api/projects/" + pid + "/secrets", "/api/projects/" + pid, "/api/projects"} {
		resp, data := x.do(t, "GET", path, nil, nil)
		if resp.StatusCode != http.StatusOK || strings.Contains(string(data), value) {
			t.Errorf("GET %s = %d, leaks the value? %s", path, resp.StatusCode, data)
		}
	}
	_, data = x.do(t, "GET", "/api/projects/"+pid+"/secrets", nil, nil)
	var list []struct {
		Name      string    `json:"name"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &list); err != nil || len(list) != 1 || list[0].Name != "figma" || list[0].UpdatedAt.IsZero() {
		t.Fatalf("list = %s (%v)", data, err)
	}
	if v, _ := x.conf.ProjectSecrets(t.Context(), pid); v["figma"] != value {
		t.Errorf("stored value = %q", v["figma"])
	}
	if resp, _ := x.do(t, "DELETE", "/api/projects/"+pid+"/secrets/figma", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", "/api/projects/"+pid+"/secrets/figma", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete = %d", resp.StatusCode)
	}
}

// The settings page reads the toggle and whether this apothecary runs
// browsers at all.
func TestProjectBrowserStatus(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	resp, data := x.do(t, "GET", "/api/projects/"+pid+"/browser", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"enabled":false`) || !strings.Contains(string(data), `"available":false`) {
		t.Fatalf("no browser = %d %s", resp.StatusCode, data)
	}
	x.e.SetBrowser(&echoBrowser{})
	_, data = x.do(t, "GET", "/api/projects/"+pid+"/browser", nil, nil)
	var info BrowserInfo
	if err := json.Unmarshal(data, &info); err != nil || !info.Available || info.State != "running" || info.Container != "apo-browser-web" || info.Sessions != 1 {
		t.Fatalf("with a browser = %s (%v)", data, err)
	}
}

// echoBrowser answers each line with "echo: <line>", or fails.
type echoBrowser struct{ fail error }

func (b *echoBrowser) Session(_ context.Context, _ ports.BrowserSession, stdio io.ReadWriter) error {
	if b.fail != nil {
		return b.fail
	}
	sc := bufio.NewScanner(stdio)
	for sc.Scan() {
		if _, err := io.WriteString(stdio, "echo: "+sc.Text()+"\n"); err != nil {
			return err
		}
	}
	return nil
}
func (b *echoBrowser) Warm(string) {}
func (b *echoBrowser) Status(_ context.Context, project string) (ports.BrowserStatus, error) {
	return ports.BrowserStatus{Container: "apo-browser-" + project, State: "running", Sessions: 1}, nil
}

// upgrade opens the browser stream as the workspace's shim does.
func upgrade(t *testing.T, x *env, runID, token string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(x.ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	req := "GET /api/runs/" + runID + "/browser-mcp HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + token +
		"\r\nConnection: Upgrade\r\nUpgrade: apothecary-mcp\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp
}

// The workspace's shim upgrades to a raw stream with the run's token; what
// the engine refuses it refuses before the upgrade, and a session that then
// fails says why on its last line.
func TestBrowserMCPStream(t *testing.T) {
	x := newEnv(t, engine.Config{})
	b := &echoBrowser{}
	x.e.SetBrowser(b)
	x.res.Browser = true
	x.res.Envp = exposingEnv{x.envp}
	release := make(chan struct{})
	x.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ports.AgentResult{TurnsUsed: 1}, nil
	}
	defer close(release)
	runID, err := x.e.CreateRun(context.Background(), engine.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		evs, _ := x.store.Events(context.Background(), runID, 0)
		if token = x.e.RunToken(runID); token != "" && run.Reduce(evs).EnvID != "" {
			break
		}
	}

	if _, _, resp := upgrade(t, x, runID, "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token = %d", resp.StatusCode)
	}
	c, br, resp := upgrade(t, x, runID, token)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upgrade = %d %s", resp.StatusCode, body)
	}
	if _, err := io.WriteString(c, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil || line != `echo: {"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n" {
		t.Fatalf("stream gave %q (%v)", line, err)
	}
	c.Close()

	b.fail = errors.New("chromium would not start")
	_, br, resp = upgrade(t, x, runID, token)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade = %d", resp.StatusCode)
	}
	line, _ = br.ReadString('\n')
	if !strings.Contains(line, `"apothecary_error"`) || !strings.Contains(line, "chromium would not start") {
		t.Errorf("failed session said %q", line)
	}

	x.res.Browser = false
	_, _, resp = upgrade(t, x, runID, token)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "browser is off") {
		t.Errorf("browser off = %d %s", resp.StatusCode, body)
	}
}

// exposingEnv is the fake provisioner with a port exposer.
type exposingEnv struct{ *fake.EnvProvisioner }

func (exposingEnv) ExposePort(context.Context, ports.EnvID, int) (ports.PortConn, error) {
	return nil, errors.New("not in this test")
}
func (exposingEnv) AppPorts(context.Context, ports.EnvID) []int { return nil }
