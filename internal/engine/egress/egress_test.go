package egress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/wsname"
)

const project = "prj-1"

// provisioner is the fake with the gateway's audit on top. plain (below) is
// one with no gateway at all.
type provisioner struct {
	*fake.EnvProvisioner
	audit    map[envport.EnvID][]egressport.EgressAuditRecord
	asked    []envport.EnvID
	policy   error
	policyMu sync.Mutex
}

func (p *provisioner) EgressAudit(_ context.Context, id envport.EnvID) []egressport.EgressAuditRecord {
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	p.asked = append(p.asked, id)
	return p.audit[id]
}

func (p *provisioner) EgressPolicy(ctx context.Context, id envport.EnvID) (egressport.EgressPolicy, error) {
	if p.policy != nil {
		return egressport.EgressPolicy{}, p.policy
	}
	return p.EnvProvisioner.EgressPolicy(ctx, id)
}

// plain is a provisioner without a gateway: it embeds only the port.
type plain struct{ envport.EnvProvisioner }

// resolver counts what it opened and closed: every path must close what it
// resolved.
type resolver struct {
	envp   envport.EnvProvisioner
	err    error
	opened atomic.Int32
	closed atomic.Int32
}

func (r *resolver) Resolve(_ context.Context, id string) (*resolution.Resolution, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.opened.Add(1)
	return &resolution.Resolution{ProjectID: id, Envp: r.envp, Release: func() { r.closed.Add(1) }}, nil
}

func (r *resolver) balanced(t *testing.T) {
	t.Helper()
	if o, c := r.opened.Load(), r.closed.Load(); o != c {
		t.Errorf("resolved %d resolutions, closed %d", o, c)
	}
}

type auditStore struct {
	rows      []egressport.EgressAuditRow
	upsertErr error
	upserts   int
	filters   []egressport.EgressAuditFilter
	queryErr  error
}

func (s *auditStore) UpsertEgressAudit(_ context.Context, rows []egressport.EgressAuditRow) error {
	s.upserts++
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *auditStore) QueryEgressAudit(_ context.Context, f egressport.EgressAuditFilter) ([]egressport.EgressAuditRow, error) {
	s.filters = append(s.filters, f)
	return s.rows, s.queryErr
}

type rig struct {
	store *event.MemStore
	prov  *provisioner
	res   *resolver
	audit *auditStore
	svc   *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	store := event.NewMemStore()
	prov := &provisioner{EnvProvisioner: fake.NewEnvProvisioner(), audit: map[envport.EnvID][]egressport.EgressAuditRecord{}}
	res := &resolver{envp: prov}
	audit := &auditStore{}
	return &rig{store: store, prov: prov, res: res, audit: audit, svc: New(&run.Reader{Store: store}, res, audit)}
}

// live seeds a run with a workspace; "" leaves it provisioning.
func (r *rig) live(t *testing.T, runID, project, env string) {
	t.Helper()
	ps := []event.Payload{&event.RunCreated{ProjectID: project, Branch: "feat/" + runID}}
	if env != "" {
		ps = append(ps, &event.EnvReady{EnvID: env})
	}
	if _, err := r.store.Append(context.Background(), runID, ps...); err != nil {
		t.Fatal(err)
	}
}

func TestControlOfARunWithNoGatewayIsOff(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(s *Service, id string) error{
		"allow":    func(s *Service, id string) error { return s.Allow(ctx, id, "example.test") },
		"set rule": func(s *Service, id string) error { return s.SetRule(ctx, id, "example.test", false) },
		"policy": func(s *Service, id string) error {
			_, err := s.Policy(ctx, id)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.res.envp = plain{r.prov.EnvProvisioner}
			r.live(t, "r1", project, "env-1")
			if err := call(r.svc, "r1"); !errors.Is(err, ErrOff) {
				t.Errorf("no gateway: err = %v, want ErrOff", err)
			}
			r.res.balanced(t)

			// A run of no project has nothing to resolve.
			r.live(t, "r2", "", "env-2")
			if err := call(r.svc, "r2"); !errors.Is(err, ErrOff) || !strings.Contains(err.Error(), "r2") {
				t.Errorf("no project: err = %v, want ErrOff naming the run", err)
			}

			// An unknown run is not "off": the caller must be told it is unknown.
			if err := call(r.svc, "ghost"); err == nil || errors.Is(err, ErrOff) || !strings.Contains(err.Error(), "ghost") {
				t.Errorf("unknown run: err = %v", err)
			}

			r.res.envp = r.prov
			r.res.err = errors.New("vault sealed")
			if err := call(r.svc, "r1"); err == nil || !strings.Contains(err.Error(), "vault sealed") {
				t.Errorf("resolver down: err = %v", err)
			}
		})
	}
}

func TestAllowAndSetRuleReachTheGateway(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.live(t, "r1", project, "env-1")

	if err := r.svc.Allow(ctx, "r1", "pypi.org"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetRule(ctx, "r1", "evil.test", false); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetRule(ctx, "r1", "npmjs.org", true); err != nil {
		t.Fatal(err)
	}
	if got := r.prov.Allowed["env-1"]; !reflect.DeepEqual(got, []string{"pypi.org"}) {
		t.Errorf("allowed = %v", got)
	}
	if got := r.prov.Rules["env-1"]; !reflect.DeepEqual(got, []string{"evil.test=deny", "npmjs.org=allow"}) {
		t.Errorf("rules = %v", got)
	}
	pol, err := r.svc.Policy(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pol.BlockedDomains, []string{"evil.test"}) {
		t.Errorf("policy = %+v", pol)
	}
	r.res.balanced(t)
}

// The most important time to allow a host is while a build is blocked on it:
// a run still provisioning has no env id yet, and control still works, under
// the name its workspace will take.
func TestControlWorksWhileTheRunIsProvisioning(t *testing.T) {
	r := newRig(t)
	r.live(t, "r1", project, "")
	if err := r.svc.Allow(context.Background(), "r1", "pypi.org"); err != nil {
		t.Fatal(err)
	}
	env := envport.EnvID(wsname.For("feat/r1", "r1"))
	if got := r.prov.Allowed[env]; !reflect.DeepEqual(got, []string{"pypi.org"}) {
		t.Errorf("allowed = %v, want pypi.org on %s", r.prov.Allowed, env)
	}
}

func TestDenialsAreClassifiedAgainstThePolicyInForce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.live(t, "r1", project, "env-1")
	r.prov.Denied = map[envport.EnvID][]egressport.EgressDenial{
		"env-1": {{Host: "allowed.test"}, {Host: "unreviewed.test"}, {Host: "blocked.test"}},
	}
	r.prov.Allowed = map[envport.EnvID][]string{"env-1": {"allowed.test"}}
	r.prov.Rules = map[envport.EnvID][]string{"env-1": {"blocked.test=deny"}}

	got := map[string]egressport.EgressStatus{}
	for _, d := range r.svc.Denials(ctx, "r1") {
		got[d.Host] = d.Status
	}
	want := map[string]egressport.EgressStatus{
		"allowed.test":    egressport.EgressAllowed,
		"unreviewed.test": egressport.EgressHeld,
		"blocked.test":    egressport.EgressBlocked,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	r.res.balanced(t)
}

// Without a readable policy nothing can be called reviewed: asking twice
// beats never asking.
func TestDenialsStayHeldWithoutAPolicy(t *testing.T) {
	r := newRig(t)
	r.live(t, "r1", project, "env-1")
	r.prov.policy = errors.New("gateway unreachable")
	r.prov.Denied = map[envport.EnvID][]egressport.EgressDenial{"env-1": {{Host: "a.test"}, {Host: "b.test"}}}
	r.prov.Rules = map[envport.EnvID][]string{"env-1": {"a.test=deny"}}
	dens := r.svc.Denials(context.Background(), "r1")
	if len(dens) != 2 {
		t.Fatalf("denials = %+v", dens)
	}
	for _, d := range dens {
		if d.Status != egressport.EgressHeld {
			t.Errorf("%s = %s, want held", d.Host, d.Status)
		}
	}
}

func TestDenialsOfAnUnreachableRunAreEmpty(t *testing.T) {
	r := newRig(t)
	if got := r.svc.Denials(context.Background(), "ghost"); got != nil {
		t.Errorf("denials = %+v", got)
	}
}

func TestIngestPersistsEveryActiveRunsAudit(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	first, last := time.Unix(100, 0), time.Unix(200, 0)
	r.live(t, "r1", project, "env-1")
	r.live(t, "r2", "prj-2", "env-2")
	r.live(t, "orphan", "", "env-3") // no project: skipped, not fatal
	r.prov.audit["env-1"] = []egressport.EgressAuditRecord{{
		Host: "pypi.org", DstIP: "10.0.0.1", Port: 443, Proto: "tcp", Verdict: "allow",
		Count: 3, BytesOut: 10, BytesIn: 20, First: first, Last: last,
	}}
	r.prov.audit["env-2"] = []egressport.EgressAuditRecord{{Host: "evil.test", Verdict: "deny", Count: 1}}
	r.prov.audit["env-3"] = []egressport.EgressAuditRecord{{Host: "nobody.test"}}
	r.svc.ActiveRuns = func() []string { return []string{"r1", "ghost", "orphan", "r2"} }

	if err := r.svc.IngestOnce(ctx); err != nil {
		t.Fatal(err)
	}
	want := []egressport.EgressAuditRow{
		{Project: project, Run: "r1", Host: "pypi.org", DstIP: "10.0.0.1", Port: 443, Proto: "tcp", Verdict: "allow",
			Count: 3, BytesOut: 10, BytesIn: 20, First: first, Last: last},
		{Project: "prj-2", Run: "r2", Host: "evil.test", Verdict: "deny", Count: 1},
	}
	if !reflect.DeepEqual(r.audit.rows, want) {
		t.Errorf("rows = %+v\nwant %+v", r.audit.rows, want)
	}
	if r.audit.upserts != 1 {
		t.Errorf("upserts = %d, want one batch", r.audit.upserts)
	}
	r.res.balanced(t)
}

func TestIngestSkipsAProjectThatWontResolveAndWritesNothingEmpty(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.live(t, "r1", project, "env-1")
	r.svc.ActiveRuns = func() []string { return []string{"r1"} }

	// Nothing to say: the store is not touched.
	if err := r.svc.IngestOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r.audit.upserts != 0 {
		t.Errorf("an empty ingest wrote %d times", r.audit.upserts)
	}

	r.prov.audit["env-1"] = []egressport.EgressAuditRecord{{Host: "a.test"}}
	r.res.err = errors.New("vault sealed")
	if err := r.svc.IngestOnce(ctx); err != nil {
		t.Errorf("one project's outage failed the ingest: %v", err)
	}
	if r.audit.upserts != 0 {
		t.Errorf("wrote %d times with nothing resolved", r.audit.upserts)
	}

	r.res.err = nil
	r.audit.upsertErr = errors.New("db down")
	if err := r.svc.IngestOnce(ctx); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("a failed write must reach the caller: %v", err)
	}
}

func TestIngestWithoutAStoreOrARunListIsANoOp(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.live(t, "r1", project, "env-1")
	r.prov.audit["env-1"] = []egressport.EgressAuditRecord{{Host: "a.test"}}

	// No run list: nobody to ingest.
	if err := r.svc.IngestOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.prov.asked) != 0 {
		t.Errorf("gateway asked about %v without any active run", r.prov.asked)
	}

	// Audit off: the gateway is not even read.
	off := New(&run.Reader{Store: r.store}, r.res, nil)
	off.ActiveRuns = func() []string { return []string{"r1"} }
	if err := off.IngestOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.prov.asked) != 0 {
		t.Errorf("gateway asked with the audit off: %v", r.prov.asked)
	}
	if rows, err := off.RunAudit(ctx, "r1", "", "", 0); rows != nil || err != nil {
		t.Errorf("run audit off = %v, %v", rows, err)
	}
	if rows, err := off.ProjectAudit(ctx, project, "", "", 0); rows != nil || err != nil {
		t.Errorf("project audit off = %v, %v", rows, err)
	}
}

// A provisioner with a gateway but no auditor has nothing to ingest.
func TestIngestOfAProvisionerWithoutAnAuditor(t *testing.T) {
	r := newRig(t)
	r.res.envp = plain{r.prov.EnvProvisioner}
	r.live(t, "r1", project, "env-1")
	r.svc.ActiveRuns = func() []string { return []string{"r1"} }
	if err := r.svc.IngestOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.audit.upserts != 0 {
		t.Errorf("upserts = %d", r.audit.upserts)
	}
	r.res.balanced(t)
}

// The run's last activity is flushed before its workspace goes. The write is
// best-effort: a failure must not stop the teardown.
func TestBeforeDestroyFlushesTheLastAudit(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.live(t, "r1", project, "env-1")
	v, err := r.svc.Runs.View(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	res := &resolution.Resolution{Envp: r.prov}

	r.svc.BeforeDestroy(ctx, v, res) // nothing recorded: nothing written
	if r.audit.upserts != 0 {
		t.Errorf("wrote %d times with nothing to flush", r.audit.upserts)
	}

	r.prov.audit["env-1"] = []egressport.EgressAuditRecord{{Host: "last.test", Verdict: "deny"}}
	r.svc.BeforeDestroy(ctx, v, res)
	if len(r.audit.rows) != 1 || r.audit.rows[0].Run != "r1" || r.audit.rows[0].Host != "last.test" {
		t.Errorf("rows = %+v", r.audit.rows)
	}

	r.audit.upsertErr = errors.New("db down")
	r.svc.BeforeDestroy(ctx, v, res) // must not panic or report

	off := New(&run.Reader{Store: r.store}, r.res, nil)
	before := len(r.prov.asked)
	off.BeforeDestroy(ctx, v, res)
	if len(r.prov.asked) != before {
		t.Error("audit off still read the gateway at teardown")
	}
}

func TestAuditQueriesCarryTheirFilter(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.audit.rows = []egressport.EgressAuditRow{{Host: "a.test"}}
	rows, err := r.svc.RunAudit(ctx, "r1", "deny", "a.test", 5)
	if err != nil || len(rows) != 1 {
		t.Fatalf("run audit = %v, %v", rows, err)
	}
	if _, err := r.svc.ProjectAudit(ctx, project, "allow", "b.test", 7); err != nil {
		t.Fatal(err)
	}
	want := []egressport.EgressAuditFilter{
		{Run: "r1", Verdict: "deny", Host: "a.test", Limit: 5},
		{Project: project, Verdict: "allow", Host: "b.test", Limit: 7},
	}
	if !reflect.DeepEqual(r.audit.filters, want) {
		t.Errorf("filters = %+v", r.audit.filters)
	}
	r.audit.queryErr = errors.New("db down")
	if _, err := r.svc.RunAudit(ctx, "r1", "", "", 0); err == nil {
		t.Error("query failure swallowed")
	}
}

// The ticker loop ingests until its context ends, and a failure does not stop
// it: the next tick tries again.
func TestRunIngestsOnEveryTickAndStops(t *testing.T) {
	r := newRig(t)
	r.live(t, "r1", project, "env-1")
	r.prov.audit["env-1"] = []egressport.EgressAuditRecord{{Host: "a.test"}}
	var ticks atomic.Int32
	r.svc.ActiveRuns = func() []string { ticks.Add(1); return []string{"r1"} }
	r.audit.upsertErr = errors.New("db down")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.svc.Run(ctx, time.Millisecond)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("the loop stopped ticking")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run outlived its context")
	}
}

// rules is the project side of an adoption: what was saved, per project.
type rules struct {
	saved map[string][]string
	err   error
	calls int
}

func (r *rules) SaveEgressRule(_ context.Context, projectID string, allow bool, hosts ...string) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	if !allow {
		panic("adoption only ever allows")
	}
	if r.saved == nil {
		r.saved = map[string][]string{}
	}
	r.saved[projectID] = append(r.saved[projectID], hosts...)
	return nil
}

// What a provisioning reached only through its open window is allowed, on
// the project and live; what the policy already rules on, IP literals and
// what came through the policy are left alone — and each host is looked at
// once per run.
func TestIngestAllowsWhatProvisioningReached(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rl := &rules{}
	r.svc.Rules = rl
	r.live(t, "r1", project, "") // still provisioning
	env := envport.EnvID(wsname.ForRun(run.View{Branch: "feat/r1"}, "r1"))
	r.prov.Rules = map[envport.EnvID][]string{env: {"blocked.test=deny", "known.test=allow"}}
	r.prov.audit[env] = []egressport.EgressAuditRecord{
		{Host: "ghcr.io", Verdict: "allow", Provisioning: true},
		{Host: "ghcr.io", Port: 80, Verdict: "allow", Provisioning: true}, // same host, another port
		{Host: "deb.debian.org", Verdict: "allow", Provisioning: true},
		{Host: "known.test", Verdict: "allow", Provisioning: true},   // allowed already
		{Host: "blocked.test", Verdict: "allow", Provisioning: true}, // blocked since: left blocked
		{Host: "203.0.113.9", Verdict: "allow", Provisioning: true},  // an IP literal
		{Host: "policy.test", Verdict: "allow"},                      // the policy let it through
		{Host: "denied.test", Verdict: "deny", Provisioning: true},
	}
	r.svc.ActiveRuns = func() []string { return []string{"r1"} }
	if err := r.svc.IngestOnce(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"ghcr.io", "deb.debian.org"}
	if got := rl.saved[project]; !reflect.DeepEqual(got, want) {
		t.Errorf("saved = %v, want %v", got, want)
	}
	if got := r.prov.Allowed[env]; !reflect.DeepEqual(got, want) {
		t.Errorf("allowed live = %v, want %v", got, want)
	}
	for _, row := range r.audit.rows {
		if row.Host == "ghcr.io" && row.Verdict != "allow" {
			t.Errorf("row = %+v", row)
		}
	}

	// Once per run: someone taking it off the allowlist is not undone.
	r.prov.Rules[env] = append(r.prov.Rules[env], "ghcr.io=deny")
	r.prov.Allowed = nil
	if err := r.svc.IngestOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rl.calls != 1 || len(r.prov.Allowed[env]) != 0 {
		t.Errorf("adopted again: %d saves, %v", rl.calls, r.prov.Allowed[env])
	}
	// Forgotten with the run.
	r.svc.Forget("r1")
	r.svc.mu.Lock()
	if _, ok := r.svc.adopted["r1"]; ok {
		t.Error("the run's adoptions outlived it")
	}
	r.svc.mu.Unlock()
	r.res.balanced(t)
}

// A save that fails leaves the hosts held and asks again on the next pass;
// a policy that cannot be read does too. Without Rules nothing is adopted.
func TestAdoptionRetriesWhatItCouldNotSave(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	rl := &rules{err: errors.New("db down")}
	r.svc.Rules = rl
	r.live(t, "r1", project, "env-1")
	r.prov.audit["env-1"] = []egressport.EgressAuditRecord{{Host: "ghcr.io", Verdict: "allow", Provisioning: true}}
	v, err := r.svc.Runs.View(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	res := &resolution.Resolution{Envp: r.prov}

	r.svc.BeforeDestroy(ctx, v, res)
	if len(r.prov.Allowed["env-1"]) != 0 {
		t.Fatal("allowed live what the project never saved")
	}
	r.prov.policy = errors.New("gateway gone")
	r.svc.BeforeDestroy(ctx, v, res)
	r.prov.policy = nil
	rl.err = nil
	r.svc.BeforeDestroy(ctx, v, res)
	if got := rl.saved[project]; !reflect.DeepEqual(got, []string{"ghcr.io"}) {
		t.Errorf("after the retry saved = %v", got)
	}

	// No gateway to ask, no project, or no Rules: nothing happens.
	r2 := newRig(t)
	r2.live(t, "r1", project, "env-1")
	r2.prov.audit["env-1"] = r.prov.audit["env-1"]
	r2.svc.ActiveRuns = func() []string { return []string{"r1"} }
	r2.svc.Audit = nil
	if err := r2.svc.IngestOnce(ctx); err != nil || len(r2.prov.asked) != 0 {
		t.Errorf("no audit, no rules: %v, asked %v", err, r2.prov.asked)
	}
	rl2 := &rules{}
	r2.svc.Rules = rl2
	if err := r2.svc.IngestOnce(ctx); err != nil || rl2.saved[project] == nil {
		t.Errorf("rules without an audit store: %v, saved %v", err, rl2.saved)
	}
	rl3 := &rules{}
	r2.svc.Rules = rl3
	r2.svc.adopt(ctx, v, plain{r2.prov.EnvProvisioner}, []egressport.EgressAuditRecord{{Host: "x.test", Verdict: "allow", Provisioning: true}})
	r2.svc.adopt(ctx, run.View{RunID: "r9"}, r2.prov, []egressport.EgressAuditRecord{{Host: "x.test", Verdict: "allow", Provisioning: true}})
	if rl3.calls != 0 {
		t.Errorf("saved without a gateway or a project: %d", rl3.calls)
	}
}
