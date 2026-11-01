package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"apothecary/internal/engine"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// holdRun starts a run whose agent turn stays open and returns its id and
// run token.
func (x *env) holdRun(t *testing.T) (string, map[string]string) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	x.runner.RunFunc = blockUntil(release)
	id := x.createTicket(t, "T")
	var tok string
	waitFor(t, "the run token", func() bool { tok = x.e.RunToken(id); return tok != "" })
	return id, map[string]string{"Authorization": "Bearer " + tok}
}

// A shim's raise: answered in-band within the hold, with the composed text,
// and the single-file field is the legacy spelling of the list.
func TestRaisedQuestionAnsweredWithinTheHold(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, tok := x.holdRun(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, p := range x.e.PendingPings() {
				if err := x.e.AnswerPing(p.ID, engine.PingAnswer{Answer: "accept", Content: map[string]any{"answer": "postgres"}}); err != nil {
					t.Errorf("answer: %v", err)
				}
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("the question never reached the engine")
	}()
	res, body := x.do(t, "POST", "/api/runs/"+id+"/pings", RaisePingRequest{
		Type: "clarification", Question: "which db?", Artifact: "artifacts/dbs.md", HoldMS: 5000, Key: "k1",
	}, tok)
	<-done
	if res.StatusCode != http.StatusOK {
		t.Fatalf("raise = %d %s", res.StatusCode, body)
	}
	got := decode[PingHold](t, body)
	if got.Text != "postgres" || got.Answer != "accept" || res.Header.Get("X-Ping-ID") != got.PingID {
		t.Errorf("hold = %+v, header %q", got, res.Header.Get("X-Ping-ID"))
	}
}

func TestRaisedQuestionRefusals(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, tok := x.holdRun(t)
	path := "/api/runs/" + id + "/pings"
	if res, _ := x.bare(t, "POST", path, "{", tok); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	res, body := x.do(t, "POST", path, RaisePingRequest{Type: "not-a-type", Question: "q"}, tok)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad type = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "GET", path+"/nope", nil, tok); res.StatusCode != http.StatusNotFound {
		t.Errorf("repoll of nothing = %d", res.StatusCode)
	}
}

// Past the run, its token is no credential at all.
func TestRaiseOnAFinishedRun(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id := x.createTicket(t, "T")
	x.e.Wait(id)
	res, body := x.do(t, "POST", "/api/runs/"+id+"/pings", RaisePingRequest{Type: "clarification", Question: "q"}, map[string]string{"Authorization": "Bearer x"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("raise = %d %s", res.StatusCode, body)
	}
}

// Without a durable projection the inbox falls back to the live registry:
// pending only, owner's only, oldest first.
func TestInboxWithoutAProjection(t *testing.T) {
	x := newEnv(t, engine.Config{})
	other := x.addUser(t, "other", false)
	x.srv.Pings = nil
	id, _ := x.holdRun(t)
	first, err := x.e.RaisePing(id, "clarification", "first?", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := x.e.RaisePing(id, "blocker", "second?", "", nil, []string{"a.md"})
	if err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "GET", "/api/pings", nil, nil)
	list := decode[[]pingJSON](t, body)
	if res.StatusCode != http.StatusOK || len(list) != 2 || list[0].PingID != first.ID || list[1].PingID != second.ID {
		t.Fatalf("inbox = %d %s", res.StatusCode, body)
	}
	if list[1].Status != "pending" || list[1].RunID != id || list[1].PayloadRef != "a.md" {
		t.Errorf("row = %+v", list[1])
	}
	if res, body := x.do(t, "GET", "/api/pings", nil, other); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("someone else's inbox = %d %s", res.StatusCode, body)
	}

	// The detail route needs the projection; the ownership check does not.
	res, body = x.do(t, "GET", "/api/pings/"+first.ID, nil, nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("detail = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", "/api/pings/"+first.ID, nil, other); res.StatusCode != http.StatusNotFound {
		t.Errorf("someone else's ping = %d %s", res.StatusCode, body)
	}
	// Its files list works from the registry alone.
	res, body = x.do(t, "GET", "/api/pings/"+second.ID+"/files", nil, nil)
	got := decode[PingFiles](t, body)
	if res.StatusCode != http.StatusOK || len(got.Files) != 1 || got.Files[0].Path != "a.md" || got.Files[0].Captured {
		t.Errorf("files = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "GET", "/api/pings/nope/curation", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("curation of nothing = %d", res.StatusCode)
	}
}

// brokenPings fails the projection, past the guard.
type brokenPings struct {
	EventPingStore
	list, get error
}

func (b brokenPings) ListPings(ctx context.Context, u, s string, n int) ([]run.PingView, error) {
	if b.list != nil {
		return nil, b.list
	}
	return b.EventPingStore.ListPings(ctx, u, s, n)
}

func (b brokenPings) GetPing(ctx context.Context, id string) (run.PingView, error) {
	if b.get != nil {
		return run.PingView{}, b.get
	}
	return b.EventPingStore.GetPing(ctx, id)
}

func TestInboxQueries(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	x.raisePing(t, "r1", "p1")
	x.raisePing(t, "r1", "p2")
	if _, err := x.store.Append(context.Background(), "r1", &event.PingAnswered{PingID: "p1", Answer: "accept"}); err != nil {
		t.Fatal(err)
	}
	ids := func(q string) []string {
		t.Helper()
		res, body := x.do(t, "GET", "/api/pings"+q, nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d %s", q, res.StatusCode, body)
		}
		var out []string
		for _, v := range decode[[]run.PingView](t, body) {
			out = append(out, v.PingID)
		}
		return out
	}
	if got := ids(""); len(got) != 1 || got[0] != "p2" {
		t.Errorf("default is pending: %v", got)
	}
	if got := ids("?status=answered"); len(got) != 1 || got[0] != "p1" {
		t.Errorf("answered: %v", got)
	}
	if got := ids("?status=all"); len(got) != 2 {
		t.Errorf("all: %v", got)
	}
	if got := ids("?status=all&limit=1"); len(got) != 1 {
		t.Errorf("limit: %v", got)
	}

	x.srv.Pings = brokenPings{EventPingStore: EventPingStore{Store: x.store}, list: errors.New("db down")}
	if res, body := x.do(t, "GET", "/api/pings", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("outage = %d %s", res.StatusCode, body)
	}
	// An empty list is a list.
	x.srv.Pings = EventPingStore{Store: x.store}
	if res, body := x.do(t, "GET", "/api/pings?status=pending", nil, x.addUser(t, "u2", false)); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("empty = %d %s", res.StatusCode, body)
	}
}

func TestStoredPingFailures(t *testing.T) {
	x := newEnv(t, engine.Config{})
	pid := map[string]string{"pid": "p"}
	x.srv.Pings = brokenPings{EventPingStore: EventPingStore{Store: x.store}, get: errors.New("db down")}
	if w := call(x.srv.getStoredPing, "GET", "/", "", testUser, pid); w.Code != http.StatusInternalServerError {
		t.Errorf("outage = %d %s", w.Code, w.Body)
	}
	x.srv.Pings = brokenPings{EventPingStore: EventPingStore{Store: x.store}, get: engine.ErrPingNotFound}
	if w := call(x.srv.getStoredPing, "GET", "/", "", testUser, pid); w.Code != http.StatusNotFound {
		t.Errorf("not found = %d %s", w.Code, w.Body)
	}
}

// The projection over the event log: order, limits, withdrawn questions, and
// a store that fails.
func TestEventPingStore(t *testing.T) {
	ctx := context.Background()
	store := event.NewMemStore()
	s := EventPingStore{Store: store}
	if _, err := store.Append(ctx, "r1", &event.RunCreated{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "r2", &event.RunCreated{UserID: "u2"}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := store.Append(ctx, "r1", &event.PingRaised{PingID: "a" + strconv.Itoa(i), Type: "clarification", Question: "q"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Append(ctx, "r2", &event.PingRaised{PingID: "b", Type: "clarification", Question: "q"}); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListPings(ctx, "", "all", 0); err != nil || len(rows) != 4 {
		t.Errorf("everyone's = %d, %v", len(rows), err)
	}
	if rows, _ := s.ListPings(ctx, "u2", "", 0); len(rows) != 1 || rows[0].PingID != "b" {
		t.Errorf("one user's = %+v", rows)
	}
	if rows, _ := s.ListPings(ctx, "u1", "pending", 2); len(rows) != 2 {
		t.Errorf("limit = %+v", rows)
	}
	// An absurd limit falls back rather than serving the table.
	if rows, _ := s.ListPings(ctx, "", "all", 100000); len(rows) != 4 {
		t.Errorf("oversized limit = %d", len(rows))
	}
	if _, err := s.GetPing(ctx, "nope"); !errors.Is(err, engine.ErrPingNotFound) {
		t.Errorf("missing = %v", err)
	}
	if v, err := s.GetPing(ctx, "b"); err != nil || v.UserID != "u2" || v.RunID != "r2" {
		t.Errorf("get = %+v, %v", v, err)
	}

	boom := errors.New("store down")
	for name, broken := range map[string]EventPingStore{
		"ids":    {Store: brokenStore{Store: store, ids: boom}},
		"events": {Store: brokenStore{Store: store, events: boom}},
	} {
		if _, err := broken.ListPings(ctx, "", "", 0); !errors.Is(err, boom) {
			t.Errorf("%s = %v", name, err)
		}
		if _, err := broken.GetPing(ctx, "b"); !errors.Is(err, boom) {
			t.Errorf("%s get = %v", name, err)
		}
	}
}

// Files given with an answer are claimed for the ping, whether a loop holds
// it or the run is parked and only the projection knows it.
func TestAnswerClaimsItsAttachments(t *testing.T) {
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	ctx := context.Background()

	id, _ := x.holdRun(t)
	p, err := x.e.RaisePing(id, "clarification", "q", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := files.att.Save(ctx, testUser, "a.txt", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "POST", "/api/pings/"+p.ID+"/answer", AnswerPingRequest{
		Answer: "accept", Content: map[string]any{"answer": "see file"}, AttachmentIDs: []string{a.ID},
	}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer = %d %s", res.StatusCode, body)
	}
	got, err := files.att.Get(ctx, a.ID)
	if err != nil || got.RunID != id || got.PingID != p.ID {
		t.Errorf("attachment = %+v, %v", got, err)
	}
	// The same ping again: the answer stands.
	res, body = x.do(t, "POST", "/api/pings/"+p.ID+"/answer", AnswerPingRequest{Answer: "accept", Content: map[string]any{"answer": "x"}}, nil)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("second answer = %d %s", res.StatusCode, body)
	}

	// A parked run: nothing live holds the ping, the projection names its run.
	x.seedLiveRun(t, "parked")
	x.raisePing(t, "parked", "parked-ping")
	b, err := files.att.Save(ctx, testUser, "b.txt", "text/plain", strings.NewReader("y"))
	if err != nil {
		t.Fatal(err)
	}
	x.do(t, "POST", "/api/pings/parked-ping/answer", AnswerPingRequest{Answer: "accept", AttachmentIDs: []string{b.ID}}, nil)
	if got, err := files.att.Get(ctx, b.ID); err != nil || got.RunID != "parked" || got.PingID != "parked-ping" {
		t.Errorf("parked attachment = %+v, %v", got, err)
	}
	if res, _ := x.bare(t, "POST", "/api/pings/parked-ping/answer", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
}

// An answer goes into the run's workspace, so it carries only the answerer's
// own files: an id someone else uploaded stays theirs.
func TestAnswerCannotClaimAnotherUsersUpload(t *testing.T) {
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	ctx := context.Background()
	id, _ := x.holdRun(t)
	p, err := x.e.RaisePing(id, "clarification", "q", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := files.att.Save(ctx, "someone-else", "secret.txt", "text/plain", strings.NewReader("s"))
	if err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "POST", "/api/pings/"+p.ID+"/answer", AnswerPingRequest{
		Answer: "accept", Content: map[string]any{"answer": "see file"}, AttachmentIDs: []string{theirs.ID},
	}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer = %d %s", res.StatusCode, body)
	}
	if got, err := files.att.Get(ctx, theirs.ID); err != nil || !got.Unclaimed() {
		t.Errorf("their upload after the answer = %+v, %v", got, err)
	}
}

// An answer that does not fit what was asked is the answerer's to fix.
func TestAnswerRefusals(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, _ := x.holdRun(t)
	p, err := x.e.RaisePing(id, "clarification", "pick", "", []string{"a", "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "POST", "/api/pings/"+p.ID+"/answer", AnswerPingRequest{Answer: "maybe"}, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("not an answer = %d %s", res.StatusCode, body)
	}
	if w := call(x.srv.answerPing, "POST", "/", `{"answer":"accept"}`, testUser, map[string]string{"pid": "nope"}); w.Code != http.StatusNotFound {
		t.Errorf("no such ping = %d %s", w.Code, w.Body)
	}
}

// The owner check on a ping falls back to the live registry without a
// projection row, and refuses what neither knows.
func TestPingOwnerFallsBackToTheRegistry(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, _ := x.holdRun(t)
	p, err := x.e.RaisePing(id, "clarification", "q", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.srv.Pings = brokenPings{EventPingStore: EventPingStore{Store: x.store}, get: errors.New("db down")}
	ctx := context.Background()
	if owner, err := x.srv.ownerOf(ctx, ScopePing, p.ID); err != nil || owner != testUser {
		t.Errorf("owner = %q, %v", owner, err)
	}
	if _, err := x.srv.ownerOf(ctx, ScopePing, "nope"); err == nil {
		t.Error("an unknown ping has an owner")
	}
}

// Every scope with its store unwired has no owner: nothing is reachable by
// accident on a bare server.
func TestOwnerOfWithoutStores(t *testing.T) {
	x := newEnv(t, engine.Config{})
	s := &Server{Runs: x.e, Store: x.store, Registry: x.reg, EnvCheck: x.srv.EnvCheck}
	ctx := context.Background()
	for _, sc := range []Scope{ScopeProject, ScopeTicket, ScopeIntegration, ScopeCredential, ScopeAttachment, ScopeSnapshot, ScopeSkill, ScopeSkillset, ScopeChannel, ScopeEnvCheck, ScopePing, ScopeRun} {
		if owner, err := s.ownerOf(ctx, sc, "nope"); err == nil {
			t.Errorf("%s of nothing has owner %q", scopeName(sc), owner)
		}
	}
	if _, err := s.ownerOf(ctx, Scope(99), "x"); err == nil {
		t.Error("an unknown scope answered")
	}
	if _, err := s.ownerOf(ctx, ScopeNone, "x"); err == nil {
		t.Error("no scope answered")
	}
}

// viewStore answers ownership from the store's projection row.
type viewStore struct {
	event.Store
	view  run.View
	found bool
	err   error
}

func (v viewStore) RunView(context.Context, string) (run.View, bool, error) {
	return v.view, v.found, v.err
}

func TestRunOwnerPrefersTheProjection(t *testing.T) {
	ctx := context.Background()
	mem := event.NewMemStore()
	if _, err := mem.Append(ctx, "r1", &event.RunCreated{UserID: "from-log"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: viewStore{Store: mem, view: run.View{UserID: "from-row"}, found: true}}
	if owner, err := s.runOwner(ctx, "r1"); err != nil || owner != "from-row" {
		t.Errorf("row = %q, %v", owner, err)
	}
	s.Store = viewStore{Store: mem}
	if owner, err := s.runOwner(ctx, "r1"); err != nil || owner != "from-log" {
		t.Errorf("no row, the log = %q, %v", owner, err)
	}
	boom := errors.New("db down")
	s.Store = viewStore{Store: mem, err: boom}
	if _, err := s.runOwner(ctx, "r1"); !errors.Is(err, boom) {
		t.Errorf("outage = %v", err)
	}
	s.Store = viewStore{Store: brokenStore{Store: mem, events: boom}}
	if _, err := s.runOwner(ctx, "r1"); !errors.Is(err, boom) {
		t.Errorf("log outage = %v", err)
	}
}

// The body-named project check answers like the guard.
func TestOwnsProject(t *testing.T) {
	x := newEnv(t, engine.Config{})
	other := x.addUser(t, "other", false)
	for name, c := range map[string]struct {
		body map[string]any
		hdr  map[string]string
		want int
	}{
		"none named":      {map[string]any{"title": "t"}, nil, http.StatusBadRequest},
		"nobody's":        {map[string]any{"title": "t", "project_id": "nope"}, nil, http.StatusNotFound},
		"someone else's":  {map[string]any{"title": "t", "project_id": testProject}, other, http.StatusNotFound},
		"bad branch name": {map[string]any{"title": "t", "project_id": testProject, "branch": "a..b"}, nil, http.StatusBadRequest},
	} {
		if res, body := x.do(t, "POST", "/api/tickets", c.body, c.hdr); res.StatusCode != c.want {
			t.Errorf("%s = %d %s, want %d", name, res.StatusCode, body, c.want)
		}
	}
}
