package mrs

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

const project = "prj-1"

// resolver hands out res and counts what it opened and closed.
type resolver struct {
	res    *resolution.Resolution
	err    error
	opened atomic.Int32
	closed atomic.Int32
}

func (r *resolver) Resolve(context.Context, string) (*resolution.Resolution, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.opened.Add(1)
	res := *r.res
	res.Release = func() { r.closed.Add(1) }
	return &res, nil
}

func (r *resolver) balanced(t *testing.T) {
	t.Helper()
	if o, c := r.opened.Load(), r.closed.Load(); o != c {
		t.Errorf("resolved %d, closed %d", o, c)
	}
}

type rig struct {
	store *event.MemStore
	host  *fake.RepoHost
	res   *resolver
	svc   *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	store := event.NewMemStore()
	host := &fake.RepoHost{}
	res := &resolver{res: &resolution.Resolution{ProjectID: project, RepoHost: host}}
	return &rig{store: store, host: host, res: res, svc: New(res, &run.Reader{Store: store})}
}

// runWith seeds a run that opened one merge request per entry.
func (r *rig) runWith(t *testing.T, runID string, mrs ...*event.MRCreated) {
	t.Helper()
	ps := []event.Payload{&event.RunCreated{ProjectID: project}}
	for _, m := range mrs {
		ps = append(ps, m)
	}
	if _, err := r.store.Append(context.Background(), runID, ps...); err != nil {
		t.Fatal(err)
	}
}

func TestReadsReachTheNamedRepository(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.host.AddOpenMR("alice", "primary mr", "feat/a", "main", "abc")
	other := &fake.RepoHost{User: "bot-web"}
	other.AddOpenMR("bob", "web mr", "feat/b", "main", "def")
	r.res.res.RepoHosts = map[string]repoport.RepoHost{"web": other}

	// "" is the primary.
	list, err := r.svc.ListMRs(ctx, project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "primary mr" {
		t.Errorf("primary list = %+v", list)
	}
	list, err = r.svc.ListMRs(ctx, project, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "web mr" {
		t.Errorf("web list = %+v", list)
	}

	mr, err := r.svc.MR(ctx, project, "", 1)
	if err != nil || mr.Title != "primary mr" {
		t.Errorf("MR = %+v, %v", mr, err)
	}
	if mr, err = r.svc.MR(ctx, project, "web", 1); err != nil || mr.Title != "web mr" {
		t.Errorf("MR web = %+v, %v", mr, err)
	}
	if _, err = r.svc.MR(ctx, project, "", 99); err == nil || !strings.Contains(err.Error(), "99") {
		t.Errorf("missing MR: %v", err)
	}

	who, err := r.svc.RepoHostUser(ctx, project, "")
	if err != nil || who != "apothecary" {
		t.Errorf("whoami = %q, %v", who, err)
	}
	if who, err = r.svc.RepoHostUser(ctx, project, "web"); err != nil || who != "bot-web" {
		t.Errorf("whoami web = %q, %v", who, err)
	}
	r.res.balanced(t)
}

func TestReadsNameTheRepositoryNoIntegrationReaches(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(s *Service, repo string) error{
		"list": func(s *Service, repo string) error { _, err := s.ListMRs(ctx, project, repo); return err },
		"mr":   func(s *Service, repo string) error { _, err := s.MR(ctx, project, repo, 1); return err },
		"user": func(s *Service, repo string) error { _, err := s.RepoHostUser(ctx, project, repo); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.res.res.RepoHost = nil
			for repo, want := range map[string]string{"web": `"web"`, "": `"` + event.PrimaryCheckoutName + `"`} {
				err := call(r.svc, repo)
				if err == nil || !strings.Contains(err.Error(), "no integration reaches repository "+want) {
					t.Errorf("repo %q: err = %v", repo, err)
				}
			}
			r.res.balanced(t)

			r.res.err = errors.New("vault sealed")
			if err := call(r.svc, ""); err == nil || !strings.Contains(err.Error(), "vault sealed") {
				t.Errorf("resolver down: %v", err)
			}
		})
	}
}

func TestStatusesOfARunWithoutMergeRequestsAskNobody(t *testing.T) {
	r := newRig(t)
	r.runWith(t, "r1")
	r.res.err = errors.New("must not resolve")
	got, err := r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("statuses = %#v, want an empty list (the API's [])", got)
	}
}

func TestStatusesOfAnUnknownRunOrUnresolvedProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.svc.MRStatuses(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown run: %v", err)
	}
	ref := r.host.AddOpenMR("", "t", "feat", "main", "abc")
	r.runWith(t, "r1", &event.MRCreated{Branch: "feat", MRURL: ref.URL, MRIID: ref.IID})
	r.res.err = errors.New("vault sealed")
	if _, err := r.svc.MRStatuses(ctx, "r1"); err == nil || !strings.Contains(err.Error(), "vault sealed") {
		t.Errorf("resolver down: %v", err)
	}
}

// An open merge request carries the host's whole picture: the state, what CI
// says of the head, approval, and the newest comment still waiting.
func TestStatusOfAnOpenMergeRequest(t *testing.T) {
	r := newRig(t)
	ref := r.host.AddOpenMR("", "t", "feat", "main", "head-1")
	r.host.SetMergeable(ref.IID, repoport.MergeConflicts, "head-1")
	r.host.SetMergeableNow(ref.IID, true)
	r.host.SetChecks(ref.IID, repoport.Check{Name: "ci", State: repoport.CheckSuccess})
	r.host.SetApprovedBy(ref.IID, "alice")
	older, newer := time.Unix(1000, 0), time.Unix(2000, 0)
	r.host.AddComment(ref.IID, "t1", "a.go", "alice", "fix this", older)
	r.host.AddComment(ref.IID, "t2", "b.go", "bob", "and this", newer)
	r.runWith(t, "r1", &event.MRCreated{Branch: "feat", Target: "stale", MRURL: ref.URL, MRIID: ref.IID})

	got, err := r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("statuses = %+v", got)
	}
	l := got[0]
	if l.Error != "" {
		t.Fatalf("error = %q", l.Error)
	}
	if l.State != repoport.MROpen || l.Mergeable != repoport.MergeConflicts || l.HeadSHA != "head-1" || !l.MergeableNow {
		t.Errorf("status = %+v", l)
	}
	if l.Target != "main" {
		t.Errorf("target = %q: the host's word beats the run's record", l.Target)
	}
	if l.Checks == nil || l.Checks.State != repoport.AggregateChecks(l.Checks.Items) || len(l.Checks.Items) != 1 || l.Checks.Head != "head-1" {
		t.Errorf("checks = %+v", l.Checks)
	}
	if l.Approval == nil || !l.Approval.Approved || !reflect.DeepEqual(l.Approval.By, []string{"alice"}) {
		t.Errorf("approval = %+v", l.Approval)
	}
	if l.PendingThreads != 2 {
		t.Errorf("pending = %d", l.PendingThreads)
	}
	if l.LastCommentAt == nil || !l.LastCommentAt.Equal(newer) {
		t.Errorf("last comment = %v", l.LastCommentAt)
	}
	if l.StackedOn != nil {
		t.Errorf("stacked on %+v with no other merge request", l.StackedOn)
	}
	r.res.balanced(t)
}

// A merged one is not asked for CI or approval, nor checked for stacking.
func TestStatusOfAMergedMergeRequestAsksNoMore(t *testing.T) {
	r := newRig(t)
	ref := r.host.AddOpenMR("", "t", "feat", "main", "h")
	r.host.SetMRState(ref.IID, repoport.MRMerged)
	r.runWith(t, "r1", &event.MRCreated{Branch: "feat", Target: "main", MRURL: ref.URL, MRIID: ref.IID})
	got, err := r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	l := got[0]
	if l.State != repoport.MRMerged || l.Checks != nil || l.Approval != nil || l.StackedOn != nil {
		t.Errorf("status = %+v", l)
	}
	if r.host.ChecksCalls != 0 {
		t.Errorf("CI asked %d times for a merged one", r.host.ChecksCalls)
	}
}

// A merge request targeting another's branch is stacked on it: merging lands
// there, not on the main line. The run's own branch is not that other one.
func TestStatusNamesTheMergeRequestItIsStackedOn(t *testing.T) {
	r := newRig(t)
	base := r.host.AddOpenMR("alice", "the base", "feat/base", "main", "h1")
	top := r.host.AddOpenMR("", "the top", "feat/top", "feat/base", "h2")
	r.runWith(t, "r1", &event.MRCreated{Branch: "feat/top", Target: "feat/base", MRURL: top.URL, MRIID: top.IID})
	got, err := r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].StackedOn == nil || got[0].StackedOn.IID != base.IID {
		t.Errorf("stacked on = %+v, want !%d", got[0].StackedOn, base.IID)
	}
}

// failingHost fails exactly one read, the way a host's API fails one call.
type failingHost struct {
	*fake.RepoHost
	checks, approval, threads error
	list                      error
}

func (h *failingHost) MRChecks(ctx context.Context, mr repoport.MRRef, head string) (repoport.Checks, error) {
	if h.checks != nil {
		return repoport.Checks{}, h.checks
	}
	return h.RepoHost.MRChecks(ctx, mr, head)
}

func (h *failingHost) MRApproval(ctx context.Context, mr repoport.MRRef) (repoport.Approval, error) {
	if h.approval != nil {
		return repoport.Approval{}, h.approval
	}
	return h.RepoHost.MRApproval(ctx, mr)
}

func (h *failingHost) MRThreads(ctx context.Context, mr repoport.MRRef) ([]repoport.ReviewThread, error) {
	if h.threads != nil {
		return nil, h.threads
	}
	return h.RepoHost.MRThreads(ctx, mr)
}

func (h *failingHost) ListMRs(ctx context.Context) ([]repoport.MRInfo, error) {
	if h.list != nil {
		return nil, h.list
	}
	return h.RepoHost.ListMRs(ctx)
}

// Whatever the host fails to answer lands on that merge request's Error, as
// an observation: the rest of the run's merge requests still report.
func TestStatusCarriesWhatTheHostCouldNotAnswer(t *testing.T) {
	boom := errors.New("host says no")
	cases := []struct {
		name string
		fail func(*failingHost)
		// wantChecks: the checks read before the failure stayed.
		wantChecks bool
	}{
		{"status", func(h *failingHost) { h.SetMRStateErr(boom) }, false},
		{"checks", func(h *failingHost) { h.checks = boom }, false},
		{"approval", func(h *failingHost) { h.approval = boom }, true},
		{"threads", func(h *failingHost) { h.threads = boom }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			h := &failingHost{RepoHost: r.host}
			r.res.res.RepoHost = h
			fine := &fake.RepoHost{}
			r.res.res.RepoHosts = map[string]repoport.RepoHost{"web": fine}
			ref := r.host.AddOpenMR("", "t", "feat", "main", "h")
			okRef := fine.AddOpenMR("", "ok", "feat", "main", "h")
			r.runWith(t, "r1",
				&event.MRCreated{Branch: "feat", MRURL: ref.URL, MRIID: ref.IID},
				&event.MRCreated{Repo: "web", Branch: "feat", MRURL: okRef.URL, MRIID: okRef.IID})
			c.fail(h)

			got, err := r.svc.MRStatuses(context.Background(), "r1")
			if err != nil {
				t.Fatalf("one host's failure failed the call: %v", err)
			}
			if got[0].Error != boom.Error() {
				t.Errorf("error = %q", got[0].Error)
			}
			if (got[0].Checks != nil) != c.wantChecks {
				t.Errorf("checks = %+v, want present=%v", got[0].Checks, c.wantChecks)
			}
			if got[0].StackedOn != nil {
				t.Errorf("stacked with an error: %+v", got[0].StackedOn)
			}
			if got[1].Error != "" || got[1].State != repoport.MROpen {
				t.Errorf("the other repository = %+v", got[1])
			}
		})
	}
}

func TestStatusOfARepositoryNoIntegrationReaches(t *testing.T) {
	r := newRig(t)
	ref := r.host.AddOpenMR("", "t", "feat", "main", "h")
	r.runWith(t, "r1",
		&event.MRCreated{Repo: "gone", Branch: "feat", Target: "main", MRURL: "https://gone/1", MRIID: 7},
		&event.MRCreated{Branch: "feat", MRURL: ref.URL, MRIID: ref.IID})
	got, err := r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Error != "no integration reaches this repository" || got[0].URL != "https://gone/1" || got[0].Target != "main" {
		t.Errorf("unreachable = %+v", got[0])
	}
	if got[1].Error != "" {
		t.Errorf("reachable = %+v", got[1])
	}
}

// Stacking is best effort: a list that fails leaves no notice over a status
// that was read, and each repository is listed only once.
func TestStackingIsBestEffortAndListedOncePerRepository(t *testing.T) {
	r := newRig(t)
	h := &countingHost{failingHost: failingHost{RepoHost: r.host}}
	r.res.res.RepoHost = h
	a := r.host.AddOpenMR("", "a", "feat/a", "main", "h1")
	b := r.host.AddOpenMR("", "b", "feat/b", "main", "h2")
	r.runWith(t, "r1",
		&event.MRCreated{Branch: "feat/a", MRURL: a.URL, MRIID: a.IID},
		&event.MRCreated{Branch: "feat/b", MRURL: b.URL, MRIID: b.IID})

	got, err := r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if h.lists.Load() != 1 {
		t.Errorf("listed %d times", h.lists.Load())
	}
	if got[0].Error != "" || got[1].Error != "" {
		t.Errorf("errors: %+v", got)
	}

	h.list = errors.New("list down")
	got, err = r.svc.MRStatuses(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Error != "" || got[0].StackedOn != nil || got[0].State != repoport.MROpen {
		t.Errorf("failed list changed the status: %+v", got[0])
	}
}

type countingHost struct {
	failingHost
	lists atomic.Int32
}

func (h *countingHost) ListMRs(ctx context.Context) ([]repoport.MRInfo, error) {
	h.lists.Add(1)
	return h.failingHost.ListMRs(ctx)
}
