package flow

import (
	"slices"
	"strings"
	"testing"
)

// servicesDoc runs the project's app (a parameter: projects name theirs),
// lets an agent look at it in the browser, and shows a human the result.
const servicesDoc = `name: t
version: 5
parameters:
  harness: fake
  model: fake/model
  app: { type: string, default: default, description: the project service the run brings up }
mcp_servers:
  - { name: browser, builtin: browser }
  - name: tracker
    command: [npx, -y, tracker-mcp]
    env: { TOKEN: "${secrets.tracker_token}" }
  - name: docs
    url: https://mcp.example.com/
    headers: { Authorization: "Bearer ${secrets.docs_key}" }
agents:
  - name: shooter
    prompt: screenshot the app
    mcp: [browser, tracker]
    inputs:
      - { name: app, type: link }
    outputs:
      - { name: shots, type: files }
main:
  - id: up
    type: service
    service: $app
    timeout: 5m
    on_fail: abort
  - id: shoot
    type: agent
    harness: $harness
    model: $model
    agent: shooter
    input:
      app: { from: run.service.$app, type: link }
    output:
      shots: { to: artifacts/shots/, type: files }
  - id: down
    type: service
    service: $app
    do: stop
`

func TestServiceStageAndSource(t *testing.T) {
	f, err := Parse("t.yaml", []byte(servicesDoc))
	if err != nil {
		t.Fatal(err)
	}
	up := f.Stage("up")
	if up.Type != StageService || up.Service != "default" || up.Do != ServiceStart || up.Timeout.Minutes() != 5 {
		t.Errorf("up = %+v", up)
	}
	if down := f.Stage("down"); down.Do != ServiceStop {
		t.Errorf("down.Do = %q", down.Do)
	}
	if in := f.Stage("shoot").Input("app"); in == nil || in.Value != "run.service.default" {
		t.Errorf("app port = %+v", in)
	}
	if got := f.Services(); !slices.Equal(got, []string{"default"}) {
		t.Errorf("Services = %v", got)
	}
	// Bound, the parameter names the project's own app.
	g, err := ParseBound("t.yaml", []byte(servicesDoc), map[string]string{"app": "aio"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Stage("up").Service != "aio" || g.Stage("shoot").Input("app").Value != "run.service.aio" {
		t.Errorf("bound: up=%q app=%q", g.Stage("up").Service, g.Stage("shoot").Input("app").Value)
	}
	if out := f.Stage("shoot").Output("shots"); out == nil || out.Type != "files" {
		t.Errorf("shots = %+v", out)
	}
}

func TestMCPServers(t *testing.T) {
	f, err := Parse("t.yaml", []byte(servicesDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.MCPServers) != 3 || f.MCPServer("browser").Builtin != MCPBrowser {
		t.Fatalf("servers = %+v", f.MCPServers)
	}
	// docs is declared but no agent uses it: its secret is not needed.
	if got := f.Secrets(); !slices.Equal(got, []string{"tracker_token"}) {
		t.Errorf("Secrets = %v", got)
	}
	if !f.UsesBuiltin(MCPBrowser) {
		t.Error("the browser is used")
	}
	v, err := FillSecrets("Bearer ${secrets.docs_key}", map[string]string{"docs_key": "k"})
	if err != nil || v != "Bearer k" {
		t.Errorf("FillSecrets = %q, %v", v, err)
	}
	if _, err := FillSecrets("${secrets.nope}", nil); err == nil {
		t.Error("a missing secret is an error")
	}
}

func TestServicesRefused(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"unknown do", "    do: start\n", "    do: reboot\n", `do "reboot" must be`},
		{"stop with a timeout", "    do: stop\n", "    do: stop\n    timeout: 1m\n", "a stop has none"},
		{"service on an agent", "    agent: shooter\n", "    agent: shooter\n    service: x\n", `field "service" does not apply`},
		{"undeclared mcp", "mcp: [browser, tracker]", "mcp: [browser, nope]", `mcp "nope" is not declared`},
		{"two kinds", "{ name: browser, builtin: browser }", "{ name: browser, builtin: browser, url: https://x/ }", "exactly one of builtin"},
		{"unknown builtin", "builtin: browser }", "builtin: chrome }", `unknown builtin "chrome"`},
		{"secret in a command", "[npx, -y, tracker-mcp]", "[npx, \"${secrets.tracker_token}\"]", "never in the command"},
		{"env on a url", "headers: { Authorization", "env: { Authorization", "env is a command's"},
		{"files to a file", "shots: { to: artifacts/shots/, type: files }", "shots: { to: artifacts/shots.png, type: files }", `must end in "/"`},
		{"a directory as a file", "outputs:\n      - { name: shots, type: files }", "outputs:\n      - { name: shots, type: file }", "is a directory, and one is type: files"},
		{"undeclared service param", "from: run.service.$app", "from: run.service.$nope", `parameter "nope" is not declared`},
	}
	// "unknown do" needs a do to replace.
	doc := strings.Replace(servicesDoc, "    service: $app\n    timeout: 5m\n", "    service: $app\n    do: start\n    timeout: 5m\n", 1)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(doc, c.from, c.to, 1)
			if src == doc {
				t.Fatalf("%q is not in the document", c.from)
			}
			if c.name == "a directory as a file" {
				src = strings.Replace(src, "shots: { to: artifacts/shots/, type: files }", "shots: { to: artifacts/shots/, type: file }", 1)
			}
			_, err := Parse("t.yaml", []byte(src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A file reader is not satisfied by a directory: files feeds files only.
func TestFilesSatisfiesFilesOnly(t *testing.T) {
	doc := strings.Replace(servicesDoc, "    inputs:\n      - { name: app, type: link }\n", "    inputs:\n      - { name: app, type: link }\n      - { name: notes, type: file, optional: true }\n", 1) +
		`  - id: again
    type: agent
    harness: $harness
    model: $model
    agent: shooter
    input:
      app: { from: run.service.$app, type: link }
      notes: { from: artifacts/shots/, type: file }
    output:
      shots: { to: artifacts/more/, type: files }
`
	if _, err := Parse("t.yaml", []byte(doc)); err == nil {
		t.Fatal("a file port read from a files output")
	}
}

// A stage a parameter turned off needs nothing of the project: a flow whose
// screenshots are off runs where there is no service and no browser.
func TestOffStagesNeedNothing(t *testing.T) {
	doc := strings.Replace(servicesDoc, "  app: { type: string, default: default, description: the project service the run brings up }\n",
		"  app: { type: string, default: default, description: the project service the run brings up }\n  shots: { type: bool, default: false }\n", 1)
	doc = strings.Replace(doc, "    type: service\n    service: $app\n    timeout: 5m\n", "    type: service\n    run_if: $shots\n    service: $app\n    timeout: 5m\n", 1)
	doc = strings.Replace(doc, "    agent: shooter\n", "    agent: shooter\n    run_if: $shots\n", 1)
	doc = strings.Replace(doc, "    do: stop\n", "    do: stop\n    run_if: $shots\n", 1)
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Services()) != 0 || f.UsesBuiltin(MCPBrowser) || len(f.Secrets()) != 0 {
		t.Errorf("off: services=%v browser=%v secrets=%v", f.Services(), f.UsesBuiltin(MCPBrowser), f.Secrets())
	}
	on, err := ParseBound("t.yaml", []byte(doc), map[string]string{"shots": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(on.Services()) != 1 || !on.UsesBuiltin(MCPBrowser) {
		t.Errorf("on: services=%v browser=%v", on.Services(), on.UsesBuiltin(MCPBrowser))
	}
}
