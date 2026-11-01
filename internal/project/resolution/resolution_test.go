package resolution

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/run"
)

func parse(t *testing.T, doc string) *flow.Flow {
	t.Helper()
	f, err := flow.Parse("t", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

const flowHead = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
`

func TestRepoHostFor(t *testing.T) {
	primary, extra := &fake.RepoHost{}, &fake.RepoHost{}
	r := &Resolution{RepoHost: primary, RepoHosts: map[string]repoport.RepoHost{"docs": extra}}
	if r.RepoHostFor("") != repoport.RepoHost(primary) || r.RepoHostFor("repo") != repoport.RepoHost(primary) {
		t.Error("the primary checkout is the project's repository host")
	}
	if r.RepoHostFor("docs") != repoport.RepoHost(extra) {
		t.Error("an extra repository has its own")
	}
	if r.RepoHostFor("context-only") != nil {
		t.Error("an extra with no locator has none")
	}
}

func TestCloseIsNilSafeAndReleases(t *testing.T) {
	var nilRes *Resolution
	nilRes.Close()
	(&Resolution{}).Close()
	calls := 0
	(&Resolution{Release: func() { calls++ }}).Close()
	if calls != 1 {
		t.Errorf("released %d times", calls)
	}
}

func TestActionAvailable(t *testing.T) {
	bare := &Resolution{}
	full := &Resolution{RepoHost: &fake.RepoHost{},
		TicketTracker: func(context.Context, string) (trackerport.TicketWriter, string, error) { return nil, "", nil }}
	for _, tc := range []struct {
		action string
		r      *Resolution
		want   string
	}{
		{"commit", bare, ""},
		{"create_mr", full, ""},
		{"create_mr", bare, "needs a repository integration"},
		{"ticket_comment", full, ""},
		{"ticket_comment", bare, "which this build cannot"},
		{"no_such_action", full, "is not registered in this build"},
	} {
		err := tc.r.ActionAvailable(&flow.Stage{Action: tc.action})
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v", tc.action, err)
		}
	}
}

func TestSignalAvailable(t *testing.T) {
	for _, tc := range []struct {
		signal string
		r      *Resolution
		want   string
	}{
		{"manual", &Resolution{}, ""},
		{"mr_merged", &Resolution{RepoHost: &fake.RepoHost{}}, ""},
		{"mr_merged", &Resolution{}, "needs a repository integration"},
		{"no_such_signal", &Resolution{}, "is not registered in this build"},
	} {
		err := tc.r.SignalAvailable(tc.signal)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v", tc.signal, err)
		}
	}
}

func TestPhaseAvailable(t *testing.T) {
	bare, host := &Resolution{}, &Resolution{RepoHost: &fake.RepoHost{}}
	onMR := &flow.Phase{Kind: flow.PhaseMain, BringsMR: true}
	if err := bare.PhaseAvailable(onMR); err == nil || !strings.Contains(err.Error(), "started on a merge request") {
		t.Errorf("main on a merge request = %v", err)
	}
	if err := host.PhaseAvailable(onMR); err != nil {
		t.Errorf("with a repository = %v", err)
	}
	if err := bare.PhaseAvailable(&flow.Phase{Kind: flow.PhaseMain}); err != nil {
		t.Errorf("a ticket's main chain needs nothing: %v", err)
	}
	f := parse(t, flowHead+`agents:
  - { name: a, prompt: p }
main:
  - { id: m, type: agent, harness: $harness, model: $model, agent: a }
outcomes:
  - { id: merged, on: mr_merged, then: done }
`)
	for i := range f.Phases {
		ph := &f.Phases[i]
		if !ph.Observed() {
			continue
		}
		if err := bare.PhaseAvailable(ph); err == nil || !strings.Contains(err.Error(), "mr_merged") {
			t.Errorf("outcome on a signal = %v", err)
		}
		if err := host.PhaseAvailable(ph); err != nil {
			t.Errorf("with a repository = %v", err)
		}
	}
}

func TestRunnerFor(t *testing.T) {
	def, alt := &fake.AgentRunner{}, &fake.AgentRunner{}
	r := &Resolution{Runner: def}
	r.Runners = nil
	if got := r.RunnerFor("alt"); got != def {
		t.Error("no set: the default")
	}
	r2 := *r
	r2.Runners = map[string]agentport.AgentRunner{"alt": alt}
	if got := r2.RunnerFor("alt"); got != alt {
		t.Error("a pinned kind in the set")
	}
	if got := r2.RunnerFor(""); got != def {
		t.Error("nothing pinned: the default")
	}
	if got := r2.RunnerFor("other"); got != def {
		t.Error("a miss falls back")
	}
}

func TestHarnessSet(t *testing.T) {
	one := &Resolution{HarnessKind: "fake"}
	if got := one.HarnessSet(); !slices.Equal(got, []string{"fake"}) {
		t.Errorf("single = %v", got)
	}
	many := &Resolution{HarnessKind: "a", HarnessKinds: []string{"a", "b"}}
	if got := many.HarnessSet(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("set = %v", got)
	}
	for kind, want := range map[string]bool{"a": true, "b": true, "c": false} {
		if got := many.HarnessInSet(kind); got != want {
			t.Errorf("in set %s = %v", kind, got)
		}
	}
	if !one.HarnessInSet("fake") {
		t.Error("the default is always in the set")
	}
}

func TestStaticResolverAndForRun(t *testing.T) {
	ctx := context.Background()
	if _, err := (StaticResolver{}).Resolve(ctx, "p"); !errors.Is(err, ErrUnresolvable) {
		t.Errorf("empty = %v", err)
	}
	r := &Resolution{ProjectID: "p"}
	if got, err := (StaticResolver{R: r}).Resolve(ctx, "ignored"); err != nil || got != r {
		t.Errorf("static = %v, %v", got, err)
	}
	if _, err := ForRun(ctx, StaticResolver{R: r}, run.View{RunID: "r1"}); !errors.Is(err, ErrUnresolvable) || !strings.Contains(err.Error(), "r1") {
		t.Errorf("no project = %v", err)
	}
	if got, err := ForRun(ctx, StaticResolver{R: r}, run.View{RunID: "r1", ProjectID: "p"}); err != nil || got != r {
		t.Errorf("for run = %v, %v", got, err)
	}
}

func TestServiceAddressesAndProbes(t *testing.T) {
	if got := (ServicePort{Port: 3000}).Address(); got != "http://localhost:3000/" {
		t.Errorf("address = %q", got)
	}
	if got := (ServicePort{Port: 3000, Path: "/app"}).Address(); got != "http://localhost:3000/app" {
		t.Errorf("address = %q", got)
	}
	if got := (&Service{}).Address(); got != "" {
		t.Errorf("no port = %q", got)
	}
	if got := (&Service{Ports: []ServicePort{{Port: 1}, {Port: 2}}}).Address(); got != "http://localhost:1/" {
		t.Errorf("first port = %q", got)
	}
	for want, s := range map[string]Service{
		"GET /health": {Health: ServiceHealth{HTTP: "/health", TCP: 1, Command: "x"}},
		"tcp 5432":    {Health: ServiceHealth{TCP: 5432, Command: "x"}},
		"command: pg": {Health: ServiceHealth{Command: "pg", Timeout: time.Second}},
		"":            {},
	} {
		if got := s.Probe(); got != want {
			t.Errorf("probe = %q, want %q", got, want)
		}
	}
}

func TestServicesAvailable(t *testing.T) {
	f := parse(t, flowHead+`main:
  - { id: app, type: service, service: web }
`)
	r := &Resolution{Services: []Service{{Name: "db"}, {Name: "web"}}}
	if err := r.ServicesAvailable(f); err != nil {
		t.Errorf("declared = %v", err)
	}
	if got := r.Service("db"); got == nil || got != &r.Services[0] {
		t.Errorf("service = %v", got)
	}
	if got := r.Service("nope"); got != nil {
		t.Errorf("unknown = %v", got)
	}
	other := &Resolution{Services: []Service{{Name: "db"}, {Name: "cache"}}}
	if err := other.ServicesAvailable(f); err == nil || !strings.Contains(err.Error(), `service "web"`) || !strings.Contains(err.Error(), "db, cache") {
		t.Errorf("undeclared = %v", err)
	}
	if err := (&Resolution{}).ServicesAvailable(f); err == nil || !strings.Contains(err.Error(), "none — declare it") {
		t.Errorf("none declared = %v", err)
	}
}

func TestMCPAvailable(t *testing.T) {
	withSecret := parse(t, flowHead+`mcp_servers:
  - name: tracker
    url: https://mcp.example.com/
    headers: { Authorization: "Bearer ${secrets.tracker_key}" }
agents:
  - { name: a, prompt: p, mcp: [tracker] }
main:
  - { id: m, type: agent, harness: $harness, model: $model, agent: a }
`)
	if err := (&Resolution{}).MCPAvailable(withSecret, true); err == nil || !strings.Contains(err.Error(), `project secret "tracker_key"`) {
		t.Errorf("missing secret = %v", err)
	}
	if err := (&Resolution{Secrets: map[string]string{"tracker_key": "x"}}).MCPAvailable(withSecret, false); err != nil {
		t.Errorf("secret held = %v", err)
	}

	browser := parse(t, flowHead+`mcp_servers:
  - { name: browser, builtin: browser }
agents:
  - { name: a, prompt: p, mcp: [browser] }
main:
  - { id: m, type: agent, harness: $harness, model: $model, agent: a }
`)
	if err := (&Resolution{}).MCPAvailable(browser, true); err == nil || !strings.Contains(err.Error(), "project's browser is off") {
		t.Errorf("browser off = %v", err)
	}
	if err := (&Resolution{Browser: true}).MCPAvailable(browser, false); err == nil || !strings.Contains(err.Error(), "runs no project browser") {
		t.Errorf("no browser here = %v", err)
	}
	if err := (&Resolution{Browser: true}).MCPAvailable(browser, true); err != nil {
		t.Errorf("browser on = %v", err)
	}
}
