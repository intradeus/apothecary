package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/run"
	"apothecary/internal/workspace/wsname"
)

const sandboxFlowYAML = "name: sandbox\nversion: 5\noperates_on: nothing\nparameters: {}\nmain:\n  - { id: work, type: manual, question: \"yours\" }\n"

// saveSandboxFlow gives the project a flow whose runs are started on nothing.
func (x *confEnv) saveSandboxFlow(t *testing.T, pid string) {
	t.Helper()
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "sandbox", "yaml": sandboxFlowYAML}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save flow = %d: %s", resp.StatusCode, data)
	}
}

// A run on nothing is started from the project: of a flow whose runs are
// started on nothing, named or not, on a fresh branch forked from one of the
// repository's or on that branch itself — and any number at once.
func TestStartARunOnNothing(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	x.saveSandboxFlow(t, pid)
	start := "/api/projects/" + pid + "/runs"
	view := func(runID string) (title, branch, base, source, ticket string) {
		t.Helper()
		evs, err := x.store.Events(t.Context(), runID, 0)
		if err != nil {
			t.Fatal(err)
		}
		v := run.Reduce(evs)
		return v.TicketTitle, v.Branch, v.BranchBase, v.SourceBranch, v.TicketID
	}

	// Nothing said but the flow: named for the moment, on a branch of its own
	// off the repository's default.
	resp, data := x.do(t, "POST", start, map[string]any{"flow": "sandbox"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d: %s", resp.StatusCode, data)
	}
	first := decode[RunRef](t, data).RunID
	title, branch, base, source, ticket := view(first)
	if !strings.HasPrefix(title, "manual run - 20") || ticket != "" || source != "main" ||
		!strings.HasPrefix(base, "apothecary/manual-run-20") || branch != base+"-"+wsname.RunIDTail(first) {
		t.Errorf("run = %q on %q (base %q) off %q, ticket %q", title, branch, base, source, ticket)
	}

	// A second beside it, named, forked from another branch.
	resp, data = x.do(t, "POST", start, map[string]any{"flow": "sandbox", "title": "  Poke at the cache ", "branch": "dev"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a second beside the first = %d: %s", resp.StatusCode, data)
	}
	second := decode[RunRef](t, data).RunID
	if title, branch, _, source, _ := view(second); title != "Poke at the cache" || source != "dev" ||
		branch != "apothecary/poke-at-the-cache-"+wsname.RunIDTail(second) {
		t.Errorf("named run = %q on %q off %q", title, branch, source)
	}

	// On the branch itself: the one named, or the repository's default.
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"flow": "sandbox", "branch": "dev", "on_branch": true}, "dev"},
		{map[string]any{"flow": "sandbox", "on_branch": true}, "main"},
	} {
		resp, data := x.do(t, "POST", start, c.body, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("on the branch itself = %d: %s", resp.StatusCode, data)
		}
		if _, branch, _, source, _ := view(decode[RunRef](t, data).RunID); branch != c.want || source != c.want {
			t.Errorf("on %v: branch %q off %q, want %q itself", c.body, branch, source, c.want)
		}
	}

	// The fresh branch named by the start: used as written, forked from the
	// branch picked. Naming it and working on the other itself are one or the
	// other, and the branch it starts from is not a new one.
	resp, data = x.do(t, "POST", start, map[string]any{"flow": "sandbox", "branch": "dev", "new_branch": " spike/cache "}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a named branch = %d: %s", resp.StatusCode, data)
	}
	if _, branch, base, source, _ := view(decode[RunRef](t, data).RunID); branch != "spike/cache" || base != "spike/cache" || source != "dev" {
		t.Errorf("named branch: %q (base %q) off %q", branch, base, source)
	}
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"flow": "sandbox", "new_branch": "spike/cache", "on_branch": true}, `say one of the two`},
		{map[string]any{"flow": "sandbox", "new_branch": "main"}, `to work on it itself, say on_branch`},
		{map[string]any{"flow": "sandbox", "branch": "dev", "new_branch": "dev"}, `to work on it itself, say on_branch`},
		{map[string]any{"flow": "sandbox", "new_branch": "bad..name"}, `are not allowed`},
	} {
		resp, data := x.do(t, "POST", start, c.body, nil)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), c.want) {
			t.Errorf("%v = %d: %s", c.body, resp.StatusCode, data)
		}
	}

	// A flow started on a ticket or a merge request is its page's to start —
	// the project's default flow included.
	for flowName, want := range map[string]string{
		"":         `starts its runs on a ticket (operates_on: ticket): start it from the ticket's page`,
		"reviewer": `starts its runs on a merge request (operates_on: merge_request): start it from the merge request's page`,
	} {
		resp, data := x.do(t, "POST", start, map[string]any{"flow": flowName}, nil)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), want) {
			t.Errorf("flow %q = %d: %s", flowName, resp.StatusCode, data)
		}
	}
	if resp, data := x.do(t, "POST", start, map[string]any{"flow": "nope"}, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown flow = %d: %s", resp.StatusCode, data)
	}
	if resp, data := x.do(t, "POST", start, map[string]any{"flow": "sandbox", "branch": "bad..name"}, nil); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "source branch") {
		t.Errorf("bad branch = %d: %s", resp.StatusCode, data)
	}
	if resp, data := x.do(t, "POST", start, "not an object", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body = %d: %s", resp.StatusCode, data)
	}
	// Someone else's project is not there.
	bob := x.addUser(t, "bob", false)
	if resp, data := x.do(t, "POST", start, map[string]any{"flow": "sandbox"}, bob); resp.StatusCode != http.StatusNotFound {
		t.Errorf("someone else's project = %d: %s", resp.StatusCode, data)
	}
}

// Such a flow is not started on a ticket: the ticket's page refuses it.
func TestAFlowOnNothingIsNotStartedOnATicket(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	x.saveSandboxFlow(t, pid)
	resp, data := x.do(t, "POST", "/api/projects/"+pid+"/tickets", map[string]any{"title": "Add caching"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d: %s", resp.StatusCode, data)
	}
	key := decode[struct {
		Key string `json:"key"`
	}](t, data).Key
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/tickets/"+key+"/runs", map[string]any{"flow": "sandbox"}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "starts its runs on nothing (operates_on: nothing), not on a ticket") {
		t.Errorf("on a ticket = %d: %s", resp.StatusCode, data)
	}
}
