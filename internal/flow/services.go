package flow

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"apothecary/internal/flow/agentspec"
)

// Services are the project's long-running processes — the app a run builds,
// its database — declared in the project (like commands), not in the flow: a
// flow names one, usually through a parameter (`service: $app`, default
// "default"), so the same flow serves projects that call their app anything.
// A service stage starts, restarts or stops one; `run.service.<name>` is its
// address while it runs (FLOWLANG §4 Sources).
const (
	ServiceStart   = "start"
	ServiceRestart = "restart"
	ServiceStop    = "stop"

	// ServiceSourcePrefix opens the family of sources that name a running
	// service: run.service.<name>, or run.service.$param.
	ServiceSourcePrefix = "run.service."
)

// ServiceDos are what a service stage can do, in display order.
var ServiceDos = []string{ServiceStart, ServiceRestart, ServiceStop}

// ServiceSource is the source naming a service's address.
func ServiceSource(name string) string { return ServiceSourcePrefix + name }

// SourceService is the service a source names, or "" when it names none.
func SourceService(v string) string {
	name, ok := strings.CutPrefix(v, ServiceSourcePrefix)
	if !ok || !slugRe.MatchString(name) {
		return ""
	}
	return name
}

// Services lists the project services the flow uses — started by a stage or
// read as a source — in document order: what run creation checks the
// project declares. A stage a parameter turned off (run_if: false) uses none.
func (f *Flow) Services() []string {
	var out []string
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	for _, st := range f.liveStages() {
		add(st.Service)
		for _, in := range st.Inputs {
			add(SourceService(in.Value))
		}
	}
	return out
}

// resolveServiceSource reads a run.service.$param source's parameter; any
// other value is returned unchanged.
func (p *parser) resolveServiceSource(v, stage, input string) (string, error) {
	rest, ok := strings.CutPrefix(v, ServiceSourcePrefix)
	if !ok {
		return v, nil
	}
	name, via, err := p.lookup(rest, paramSite{stage: stage, field: "input", input: input, typ: ParamString})
	if err != nil {
		return "", err
	}
	if !slugRe.MatchString(name) {
		return "", fmt.Errorf("%q%s is not a service name (a project service: a lowercase slug)", name, via)
	}
	return ServiceSource(name), nil
}

// MCP servers (FLOWLANG §1 mcp_servers) are tools an agent may call beyond its
// harness's own: a local command speaking MCP on stdio, a remote URL, or one
// apothecary runs (builtin). An agent lists the ones it uses (`mcp:`); a
// secret is read from the project's secrets where a value says
// ${secrets.<name>}, so none is ever written in the flow.
const (
	// MCPBrowser is the project's browser (Workspace.Browser): Chromium
	// driven through Playwright's MCP server, one browser per agent session,
	// reaching the run's services at localhost.
	MCPBrowser = "browser"
)

// MCPBuiltins are the servers apothecary provides, in display order.
var MCPBuiltins = []string{MCPBrowser}

// DocMCPServer is one entry of `mcp_servers:`, as written.
type DocMCPServer struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description" json:"description,omitempty"`
	Builtin     string            `yaml:"builtin" json:"builtin,omitempty"`
	Command     []string          `yaml:"command" json:"command,omitempty"`
	URL         string            `yaml:"url" json:"url,omitempty"`
	Env         map[string]string `yaml:"env" json:"env,omitempty"`
	Headers     map[string]string `yaml:"headers" json:"headers,omitempty"`
}

// MCPServer is one validated MCP server. Env and Headers still carry their
// ${secrets.<name>} references: the engine fills them for a run (Secrets
// lists the names, for run creation's check).
type MCPServer struct {
	Name        string
	Description string
	Builtin     string
	Command     []string
	URL         string
	Env         map[string]string
	Headers     map[string]string
	Secrets     []string
}

// MCPServer returns the flow's server named name, or nil.
func (f *Flow) MCPServer(name string) *MCPServer {
	for i := range f.MCPServers {
		if f.MCPServers[i].Name == name {
			return &f.MCPServers[i]
		}
	}
	return nil
}

// Secrets lists the project secrets the flow reads — those of the servers
// its running agents use — in document order.
func (f *Flow) Secrets() []string {
	var out []string
	for _, m := range f.usedServers() {
		for _, s := range m.Secrets {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// UsesBuiltin reports whether an agent the flow runs uses the builtin server
// b — the browser needs the project's browser on.
func (f *Flow) UsesBuiltin(b string) bool {
	for _, m := range f.usedServers() {
		if m.Builtin == b {
			return true
		}
	}
	return false
}

// liveStages are the stages a run can reach the work of: all but those a
// parameter turned off. What they use is what run creation checks the
// project has — a flow whose screenshots are off needs no browser.
func (f *Flow) liveStages() []*Stage {
	var out []*Stage
	for i := range f.Stages {
		if w := f.Stages[i].RunIf; w != nil && w.Fact == CondNever {
			continue
		}
		out = append(out, &f.Stages[i])
	}
	return out
}

// usedServers are the MCP servers of the agents the live stages run, in
// declaration order.
func (f *Flow) usedServers() []*MCPServer {
	used := map[string]bool{}
	for _, st := range f.liveStages() {
		if sp := f.Agent(st.Agent); st.Type == StageAgent && sp != nil {
			for _, name := range sp.MCP {
				used[name] = true
			}
		}
	}
	var out []*MCPServer
	for i := range f.MCPServers {
		if used[f.MCPServers[i].Name] {
			out = append(out, &f.MCPServers[i])
		}
	}
	return out
}

var (
	secretRefRe = regexp.MustCompile(`\$\{secrets\.([^}]*)\}`)
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerKeyRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// SecretRefs are the project secrets a value reads (${secrets.<name>}).
func SecretRefs(v string) []string {
	var out []string
	for _, m := range secretRefRe.FindAllStringSubmatch(v, -1) {
		out = append(out, m[1])
	}
	return out
}

// FillSecrets replaces each ${secrets.<name>} in v with the secret; a name
// with no secret is an error (run creation checks them first).
func FillSecrets(v string, secrets map[string]string) (string, error) {
	var missing string
	out := secretRefRe.ReplaceAllStringFunc(v, func(ref string) string {
		name := secretRefRe.FindStringSubmatch(ref)[1]
		s, ok := secrets[name]
		if !ok && missing == "" {
			missing = name
		}
		return s
	})
	if missing != "" {
		return "", fmt.Errorf("project secret %q is not set", missing)
	}
	return out, nil
}

func (p *parser) buildMCPServers(raw []DocMCPServer) ([]MCPServer, error) {
	line := p.keyLine("mcp_servers")
	seen := map[string]bool{}
	out := make([]MCPServer, 0, len(raw))
	for i, d := range raw {
		at := p.seqLine("mcp_servers", i)
		if at == 0 {
			at = line
		}
		fail := func(format string, args ...any) ([]MCPServer, error) {
			name := d.Name
			if name == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			return nil, p.errf(at, "mcp server %q: %s", name, fmt.Sprintf(format, args...))
		}
		if !slugRe.MatchString(d.Name) {
			return fail("name is required: a lowercase slug the agents' mcp lists name")
		}
		if seen[d.Name] {
			return fail("declared twice")
		}
		seen[d.Name] = true
		if err := notAParam(d.Description); err != nil {
			return fail("description: %v", err)
		}
		n := 0
		for _, set := range []bool{d.Builtin != "", len(d.Command) > 0, d.URL != ""} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fail("exactly one of builtin (%s), command (a program speaking MCP on stdio) or url is required", strings.Join(MCPBuiltins, " | "))
		}
		m := MCPServer{Name: d.Name, Description: d.Description, Builtin: d.Builtin, Command: d.Command, URL: d.URL, Env: d.Env, Headers: d.Headers}
		switch {
		case d.Builtin != "":
			if !slices.Contains(MCPBuiltins, d.Builtin) {
				return fail("unknown builtin %q (%s)", d.Builtin, strings.Join(MCPBuiltins, " | "))
			}
			if len(d.Env) > 0 || len(d.Headers) > 0 {
				return fail("a builtin is apothecary's: it takes no env or headers")
			}
		case len(d.Command) > 0:
			if strings.TrimSpace(d.Command[0]) == "" {
				return fail("command: the program is required (its first word)")
			}
			if len(d.Headers) > 0 {
				return fail("headers are a url server's; a command takes env")
			}
		default:
			u, err := url.Parse(d.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fail("url %q must be an http(s) URL", d.URL)
			}
			if len(d.Env) > 0 {
				return fail("env is a command's; a url server takes headers")
			}
		}
		for _, w := range append(slices.Clone(d.Command), d.URL) {
			if len(SecretRefs(w)) > 0 {
				return fail("a secret is read in env or headers only — never in the command or the url")
			}
		}
		for _, kv := range []struct {
			field string
			m     map[string]string
			keyRe *regexp.Regexp
		}{{"env", d.Env, envKeyRe}, {"headers", d.Headers, headerKeyRe}} {
			for k, v := range kv.m {
				if !kv.keyRe.MatchString(k) {
					return fail("%s %q is not a valid name", kv.field, k)
				}
				for _, s := range SecretRefs(v) {
					if !slugRe.MatchString(s) {
						return fail("%s %q: ${secrets.%s} must name a project secret (a lowercase slug)", kv.field, k, s)
					}
					if !slices.Contains(m.Secrets, s) {
						m.Secrets = append(m.Secrets, s)
					}
				}
			}
		}
		slices.Sort(m.Secrets)
		out = append(out, m)
	}
	return out, nil
}

// agentMCP checks that every server an agent lists is one the flow declares.
func agentMCP(sp *agentspec.Spec, f *Flow) error {
	for _, name := range sp.MCP {
		if f.MCPServer(name) == nil {
			names := make([]string, 0, len(f.MCPServers))
			for _, m := range f.MCPServers {
				names = append(names, m.Name)
			}
			declared := "none"
			if len(names) > 0 {
				declared = strings.Join(names, ", ")
			}
			return fmt.Errorf("mcp %q is not declared in this flow (add it to the %q block; declared: %s)", name, "mcp_servers", declared)
		}
	}
	return nil
}
