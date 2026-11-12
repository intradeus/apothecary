package workitem_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
	"apothecary/internal/workitem/items"
	"apothecary/internal/workitem/mergerequest"
	"apothecary/internal/workitem/ticket"
)

// probe is a kind a test shapes: required or not, accompanying or not, and
// recording the order it prepares in.
type probe struct {
	name                  string
	required, accompanies bool
	prepared              *[]string
	fail                  error
}

func (p probe) Name() string                   { return p.name }
func (p probe) Noun() string                   { return p.name + " thing" }
func (p probe) Describe(r workitem.Ref) string { return p.name + " " + r.Key }
func (p probe) Required() bool                 { return p.required }
func (p probe) Accompanies() bool              { return p.accompanies }
func (p probe) Requested(project string, t runinput.Ticket, _ runinput.Input) (workitem.Ref, bool) {
	return workitem.Ref{Kind: p.name, Project: project, Key: t.Title}, t.Title != ""
}
func (p probe) Held(v run.View) []workitem.Ref {
	return []workitem.Ref{{Kind: p.name, Project: v.ProjectID, Key: v.TicketTitle}}
}
func (p probe) Prepare(_ context.Context, s *workitem.Start, item workitem.Ref) error {
	*p.prepared = append(*p.prepared, p.name+":"+item.Key)
	return p.fail
}

func TestRegistryRefusesAKindTwice(t *testing.T) {
	if _, err := workitem.NewRegistry(probe{name: "a"}, probe{name: "a"}); err == nil || !strings.Contains(err.Error(), `"a" registered twice`) {
		t.Errorf("err = %v", err)
	}
}

func TestRegistryAsksEveryKind(t *testing.T) {
	var order []string
	a, b := probe{name: "a", prepared: &order}, probe{name: "b", prepared: &order}
	r, err := workitem.NewRegistry(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Kinds(); len(got) != 2 || got[0].Name() != "a" {
		t.Errorf("kinds = %v", got)
	}
	if k, ok := r.Get("b"); !ok || k.Name() != "b" {
		t.Error("b not found")
	}
	if _, ok := r.Get("c"); ok {
		t.Error("c found")
	}
	items := r.Requested("p", runinput.Ticket{Title: "x"}, runinput.Input{})
	if len(items) != 2 || items[0] != (workitem.Ref{Kind: "a", Project: "p", Key: "x"}) {
		t.Errorf("requested = %v", items)
	}
	v := run.View{ProjectID: "p", TicketTitle: "x"}
	if held := r.Held(v); len(held) != 2 || !r.Holds(v, items[1]) || r.Holds(v, workitem.Ref{Kind: "c"}) {
		t.Errorf("held = %v", held)
	}
	if r.Describe(items[0]) != "a x" || r.Describe(workitem.Ref{Kind: "c", Project: "p", Key: "k"}) != "c:p/k" {
		t.Error("describe")
	}
	// Prepared in registration order, whatever order the items came in.
	if err := r.Prepare(context.Background(), &workitem.Start{}, []workitem.Ref{items[1], items[0]}); err != nil || !slices.Equal(order, []string{"a:x", "b:x"}) {
		t.Errorf("prepare = %v, %v", order, err)
	}
	boom := errors.New("boom")
	bad, _ := workitem.NewRegistry(probe{name: "a", prepared: &order, fail: boom})
	if err := bad.Prepare(context.Background(), &workitem.Start{}, items[:1]); !errors.Is(err, boom) {
		t.Errorf("a failed prepare = %v", err)
	}
}

func TestStartsOn(t *testing.T) {
	r, _ := workitem.NewRegistry(
		probe{name: "loose", accompanies: true},
		probe{name: "strict", required: true},
	)
	loose, strict := workitem.Ref{Kind: "loose"}, workitem.Ref{Kind: "strict"}
	for _, c := range []struct {
		name, kind string
		items      []workitem.Ref
		want       string
	}{
		{"a loose kind on nothing", "loose", nil, ""},
		{"a strict kind on its item", "strict", []workitem.Ref{strict}, ""},
		{"a strict kind with an accompanying item", "strict", []workitem.Ref{loose, strict}, ""},
		{"a strict kind on nothing", "strict", nil, `starts its runs on a strict thing (operates_on: strict): name the strict thing`},
		{"a loose kind with an item that cannot come", "loose", []workitem.Ref{strict}, `starts its runs on a loose thing (operates_on: loose), not on a strict thing`},
		{"an unknown item", "loose", []workitem.Ref{{Kind: "ghost"}}, `not on a ghost`},
		{"an unknown kind", "ghost", nil, `operates on "ghost", which is no kind`},
	} {
		err := r.StartsOn("f", c.kind, c.items)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// An item has one run going at a time; the refusal names it and the run.
func TestIdle(t *testing.T) {
	r := items.All()
	tk := ticket.Ref("p", "tk-1")
	runs := []run.View{
		{RunID: "done", ProjectID: "p", TicketID: "tk-1", State: run.Completed},
		{RunID: "other", ProjectID: "p", TicketID: "tk-2", State: run.Running},
	}
	if err := r.Idle(runs, []workitem.Ref{tk}); err != nil {
		t.Errorf("an ended run held its ticket: %v", err)
	}
	runs = append(runs, run.View{RunID: "live", ProjectID: "p", TicketID: "tk-1", State: run.Running})
	err := r.Idle(runs, []workitem.Ref{tk})
	var busy workitem.ErrBusy
	if !errors.As(err, &busy) || busy.RunID != "live" || busy.Item != tk || busy.ActiveRunID() != "live" {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "ticket tk-1 is busy: run live is still active; abort it or wait for it to end" {
		t.Errorf("message = %q", err)
	}
	mr := mergerequest.Ref("p", "", 4)
	opened := run.View{RunID: "opener", ProjectID: "p", State: run.InReview, MRs: []run.MRView{{Repo: "repo", IID: 4}}}
	if err := r.Idle([]run.View{opened}, []workitem.Ref{mr}); err == nil || !strings.Contains(err.Error(), "merge request !4 is busy: run opener") {
		t.Errorf("a run working on the merge request it opened = %v", err)
	}
}

// Two starts on the same items wait for each other, whatever order they
// name them in; the lock is per item.
func TestLock(t *testing.T) {
	r := items.All()
	a, b := ticket.Ref("p", "a"), ticket.Ref("p", "b")
	unlock := r.Lock([]workitem.Ref{a, b, a})
	got := make(chan struct{})
	go func() {
		defer close(got)
		r.Lock([]workitem.Ref{b, a})()
	}()
	select {
	case <-got:
		t.Fatal("a second start took locks the first holds")
	case <-time.After(20 * time.Millisecond):
	}
	// Another item is free meanwhile.
	r.Lock([]workitem.Ref{ticket.Ref("p", "c")})()
	unlock()
	<-got

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); r.Lock([]workitem.Ref{a, b})() }()
		go func() { defer wg.Done(); r.Lock([]workitem.Ref{b, a})() }()
	}
	wg.Wait()
}

// The kinds registered are exactly the language's operates_on values: a
// kind the language cannot name could never start a run, and a value with
// no kind would start one nothing prepares.
func TestKindsAreTheLanguagesOperatesOn(t *testing.T) {
	var names []string
	for _, k := range items.All().Kinds() {
		names = append(names, k.Name())
	}
	want := []string{flow.OperatesOnTicket, flow.OperatesOnMR}
	if !slices.Equal(names, want) {
		t.Errorf("kinds = %v, operates_on = %v", names, want)
	}
	for _, name := range names {
		doc := "name: f\nversion: 5\noperates_on: " + name + "\nparameters: { harness: fake, model: a/b }\nmain:\n  - { id: s, type: command, run: \"true\" }\n"
		if name == flow.OperatesOnMR {
			doc += "outcomes:\n  - { id: merged, on: mr_merged, then: done }\n  - { id: closed, on: mr_closed, then: done }\n"
		}
		if fl, err := flow.Parse("f.yaml", []byte(doc)); err != nil || fl.OperatesOn != name {
			t.Errorf("operates_on %s: %v", name, err)
		}
	}
}

func TestRefString(t *testing.T) {
	if got := (workitem.Ref{Kind: "ticket", Project: "p", Key: "k"}).String(); got != "ticket:p/k" {
		t.Errorf("ref = %q", got)
	}
}
