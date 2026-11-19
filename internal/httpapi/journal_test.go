package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/run/event"
)

// The shim reads its own run's journal with the run's token, by reference,
// its # escaped: every record, one attempt, a decision. A reference the run
// has not is a 404, what is not a reference a 400 naming the grammar; another
// run's token — or none — reads nothing.
func TestJournalRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, tok := x.holdRun(t)
	if _, err := x.e.RaisePing(id, "clarification", "which db?", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range x.e.PendingPings() {
		if err := x.e.AnswerPing(p.ID, engine.PingAnswer{Answer: "accept", Content: map[string]any{"answer": "postgres"}}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/runs/" + id + "/journal"
	escape := func(r string) string { return strings.ReplaceAll(r, "#", "%23") }

	var all engine.JournalEntry
	decided := regexp.MustCompile(`\[(\S+/decision#1)\]`)
	waitFor(t, "the decision in the journal", func() bool {
		res, body := x.do(t, "GET", base, nil, tok)
		all = decode[engine.JournalEntry](t, body)
		return res.StatusCode == http.StatusOK && decided.MatchString(all.Text)
	})
	if all.Ref != "run" || all.File != "run.md" {
		t.Errorf("records = %+v", all)
	}
	attempt := regexp.MustCompile(`(?m)^### (\S+#\d+) — attempt `).FindStringSubmatch(all.Text)
	if attempt == nil {
		t.Fatalf("no attempt in the records:\n%s", all.Text)
	}
	res, body := x.do(t, "GET", base+"/"+escape(attempt[1]), nil, tok)
	if got := decode[engine.JournalEntry](t, body); res.StatusCode != http.StatusOK || got.Ref != attempt[1] || got.File != attempt[1]+".md" {
		t.Errorf("attempt = %d %+v", res.StatusCode, got)
	}
	decision := decided.FindStringSubmatch(all.Text)[1]
	res, body = x.do(t, "GET", base+"/"+escape(decision), nil, tok)
	if got := decode[engine.JournalEntry](t, body); res.StatusCode != http.StatusOK || got.Ref != decision || got.File != "" || !strings.Contains(got.Text, "which db?") {
		t.Errorf("decision %s = %d %+v", decision, res.StatusCode, got)
	}
	if res, body := x.do(t, "GET", base+"/"+escape(attempt[1])+"/decision%239", nil, tok); res.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "no such journal entry") {
		t.Errorf("unknown reference = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", base+"/decision-1", nil, tok); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "not a reference") {
		t.Errorf("not a reference = %d %s", res.StatusCode, body)
	}

	other, otherTok := x.holdRun(t)
	if res, _ := x.do(t, "GET", base, nil, otherTok); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("another run's token = %d", res.StatusCode)
	}
	if res, _ := x.do(t, "GET", "/api/runs/"+other+"/journal", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token = %d", res.StatusCode)
	}
}

// An attempt's output that is not text is listed beside its entry and
// served by name as bytes, with the run's token; a name it has not, or one
// whose record is gone, is a 404, one whose bytes retention swept a 410, a
// reference that is not an attempt a 400.
func TestJournalBesideFiles(t *testing.T) {
	x, files := newFilesEnv(t)
	id, tok := x.holdRun(t)
	ctx := context.Background()
	svg := `<svg xmlns="http://www.w3.org/2000/svg"/>`
	sn, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: id, StageID: "shoot", Attempt: 1, Kind: filestore.KindOutput,
		Name: "shot", Path: "artifacts/shot.svg", Filename: "shot.svg", Mime: "image/svg+xml"}, func(s string) string { return s }, []byte(svg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.store.Append(ctx, id,
		&event.StageStarted{StageID: "shoot", Attempt: 1},
		&event.StageCompleted{StageID: "shoot"},
		&event.SnapshotCaptured{StageID: "shoot", Attempt: 1, Kind: "output", Name: "shot", Path: "artifacts/shot.svg",
			SnapshotID: sn.ID, Filename: "shot.svg", Mime: "image/svg+xml", Size: int64(len(svg))},
		&event.SnapshotCaptured{StageID: "shoot", Attempt: 1, Kind: "output", Name: "gone", Path: "artifacts/gone.png",
			SnapshotID: "swept", Filename: "gone.png", Mime: "image/png", Size: 3},
	); err != nil {
		t.Fatal(err)
	}
	base := "/api/runs/" + id + "/journal/shoot%231"
	res, body := x.do(t, "GET", base, nil, tok)
	if got := decode[engine.JournalEntry](t, body); res.StatusCode != http.StatusOK || len(got.Beside) != 1 || got.Beside[0].Name != "shot.svg" {
		t.Fatalf("attempt = %d %+v", res.StatusCode, got)
	}
	res, body = x.do(t, "GET", base+"?file=shot.svg", nil, tok)
	if res.StatusCode != http.StatusOK || string(body) != svg || res.Header.Get("Content-Type") != "application/octet-stream" ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("file = %d %s %q", res.StatusCode, res.Header, body)
	}
	for q, want := range map[string]int{
		base + "?file=nothing.png":                           http.StatusNotFound,
		base + "?file=gone.png":                              http.StatusNotFound,
		"/api/runs/" + id + "/journal/run?file=shot.svg":     http.StatusBadRequest,
		"/api/runs/" + id + "/journal/shoot-1?file=shot.svg": http.StatusBadRequest,
	} {
		if res, body := x.do(t, "GET", q, nil, tok); res.StatusCode != want {
			t.Errorf("%s = %d %s, want %d", q, res.StatusCode, body, want)
		}
	}
	// The record kept, its bytes swept; no snapshot store at all.
	x.srv.Snapshots = filestore.NewSnapshots(files.mem, filestore.NewBlobs(t.TempDir()), 64)
	if res, _ := x.do(t, "GET", base+"?file=shot.svg", nil, tok); res.StatusCode != http.StatusGone {
		t.Errorf("swept bytes = %d", res.StatusCode)
	}
	x.srv.Snapshots = nil
	if res, _ := x.do(t, "GET", base+"?file=shot.svg", nil, tok); res.StatusCode != http.StatusGone {
		t.Errorf("no store = %d", res.StatusCode)
	}
	other, _ := x.holdRun(t)
	if res, _ := x.do(t, "GET", "/api/runs/"+other+"/journal/shoot%231?file=shot.svg", nil, tok); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("another run's file with this token = %d", res.StatusCode)
	}
}

// A person reads a run's journal through its reader's route and the
// read_run_journal tool, as its agents do with the run's token.
func TestRunHistoryForAPerson(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, _ := x.holdRun(t)
	res := x.mcpTool(t, nil, "read_run_journal", map[string]any{"id": id})
	if res.IsError || !strings.Contains(res.Content[0].Text, "RUN JOURNAL") {
		t.Errorf("the whole journal = %+v", res)
	}
	x.expect(t, "a bad ref", nil, "GET", "/api/runs/"+id+"/history?ref=%25%25", nil, 400)
	x.expect(t, "no such entry", nil, "GET", "/api/runs/"+id+"/history?ref="+url.QueryEscape("review#9"), nil, 404)
	x.expect(t, "no such run", nil, "GET", "/api/runs/nope/history", nil, 404)
}
