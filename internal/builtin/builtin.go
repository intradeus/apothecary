// Package builtin holds the compiled-in defaults: the "default" flow, the
// agent library it references, and the suggested egress-allowlist defaults
// (EgressDefaults). They are embedded in the binary, so a completely empty
// database still resolves a working configuration — no seeding step, no drift,
// nothing to delete by accident. Note the egress list is OFFERED, not enforced:
// a project opts in via the form, unlike flows/agents which back a resolve.
//
// Layering contract (shadowing): the database always wins. A user-saved
// flow or a project-authored agent with the same name overrides the
// built-in through the ordinary save paths (copy-on-write); deleting the
// override falls back to the built-in ("reset to default"). Nothing here
// is ever written to the database.
//
// This package may import only flow and agentspec — stores and the engine
// import it, never the other way around.
package builtin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"apothecary/internal/agentspec"
	"apothecary/internal/flow"
)

//go:embed flows/*.yaml
var flowFS embed.FS

//go:embed agents/*.yaml
var agentFS embed.FS

// DefaultFlowName is the flow every fresh install can run out of the box;
// projects created without a default_flow fall back to it.
const DefaultFlowName = "default"

type registry struct {
	flows      map[string]*flow.Flow
	flowYAML   map[string]string
	agents     map[string]*agentspec.Spec
	agentOrder []string
}

// load parses the embedded documents once. The content is compiled in and
// locked by tests, so a parse failure is a broken build — panic loudly
// rather than limp along without defaults.
var load = sync.OnceValue(func() *registry {
	r := &registry{
		flows:    map[string]*flow.Flow{},
		flowYAML: map[string]string{},
		agents:   map[string]*agentspec.Spec{},
	}
	fentries, err := flowFS.ReadDir("flows")
	if err != nil {
		panic(fmt.Sprintf("builtin: reading embedded flows: %v", err))
	}
	for _, e := range fentries {
		data, err := flowFS.ReadFile("flows/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("builtin: %v", err))
		}
		f, err := flow.Parse(e.Name(), data)
		if err != nil {
			panic(fmt.Sprintf("builtin: embedded flow %s does not parse: %v", e.Name(), err))
		}
		if stem := strings.TrimSuffix(e.Name(), path.Ext(e.Name())); f.Name != stem {
			panic(fmt.Sprintf("builtin: flow %s is named %q; the name must equal the filename stem", e.Name(), f.Name))
		}
		r.flows[f.Name] = f
		r.flowYAML[f.Name] = string(data)
	}
	aentries, err := agentFS.ReadDir("agents")
	if err != nil {
		panic(fmt.Sprintf("builtin: reading embedded agents: %v", err))
	}
	for _, e := range aentries {
		data, err := agentFS.ReadFile("agents/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("builtin: %v", err))
		}
		stem := strings.TrimSuffix(e.Name(), path.Ext(e.Name()))
		sp, err := agentspec.Parse(stem, data)
		if err != nil {
			panic(fmt.Sprintf("builtin: embedded agent %s does not parse: %v", e.Name(), err))
		}
		r.agents[sp.Name] = sp
		r.agentOrder = append(r.agentOrder, sp.Name)
	}
	sort.Strings(r.agentOrder)
	return r
})

// Flow returns the parsed built-in flow, or nil.
func Flow(name string) *flow.Flow { return load().flows[name] }

// FlowYAML returns the built-in flow's raw document, for the editor/UI.
func FlowYAML(name string) (string, bool) {
	y, ok := load().flowYAML[name]
	return y, ok
}

// FlowNames lists the built-in flows, sorted.
func FlowNames() []string {
	r := load()
	names := make([]string, 0, len(r.flows))
	for n := range r.flows {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Agent returns the built-in agent spec, or nil.
func Agent(name string) *agentspec.Spec { return load().agents[name] }

// Agents lists the built-in agent specs, sorted by name.
func Agents() []*agentspec.Spec {
	r := load()
	out := make([]*agentspec.Spec, 0, len(r.agentOrder))
	for _, n := range r.agentOrder {
		out = append(out, r.agents[n])
	}
	return out
}

// FlowSource layers the built-ins under another source: the database wins,
// missing names fall back to the compiled-in defaults. Wrap the store's
// source with this everywhere the engine resolves flows.
func FlowSource(next flow.Source) flow.Source { return layeredSource{next: next} }

type layeredSource struct{ next flow.Source }

func (s layeredSource) Get(ctx context.Context, name string) (*flow.Flow, error) {
	f, err := s.next.Get(ctx, name)
	if errors.Is(err, flow.ErrNotFound) {
		if b := Flow(name); b != nil {
			return b, nil
		}
	}
	return f, err
}
