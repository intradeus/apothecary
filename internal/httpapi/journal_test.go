package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"apothecary/internal/engine"
)

// The shim reads its own run's journal with the run's token: every record,
// one pass, a decision; an id the run has not is a 404, and another run's
// token — or none — reads nothing.
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

	var all engine.JournalEntry
	waitFor(t, "the decision in the journal", func() bool {
		res, body := x.do(t, "GET", base, nil, tok)
		all = decode[engine.JournalEntry](t, body)
		return res.StatusCode == http.StatusOK && strings.Contains(all.Text, "[decision-1]")
	})
	if all.ID != "records" || all.File != "records.md" {
		t.Errorf("records = %+v", all)
	}
	pass := regexp.MustCompile(`(?m)^### (\S+) — pass `).FindStringSubmatch(all.Text)
	if pass == nil {
		t.Fatalf("no pass in the records:\n%s", all.Text)
	}
	res, body := x.do(t, "GET", base+"/"+pass[1], nil, tok)
	if got := decode[engine.JournalEntry](t, body); res.StatusCode != http.StatusOK || got.File != pass[1]+".md" {
		t.Errorf("pass = %d %+v", res.StatusCode, got)
	}
	res, body = x.do(t, "GET", base+"/decision-1", nil, tok)
	if got := decode[engine.JournalEntry](t, body); res.StatusCode != http.StatusOK || got.File != "" || !strings.Contains(got.Text, "which db?") {
		t.Errorf("decision = %d %+v", res.StatusCode, got)
	}
	if res, body := x.do(t, "GET", base+"/decision-9", nil, tok); res.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "no such journal entry") {
		t.Errorf("unknown id = %d %s", res.StatusCode, body)
	}

	other, otherTok := x.holdRun(t)
	if res, _ := x.do(t, "GET", base, nil, otherTok); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("another run's token = %d", res.StatusCode)
	}
	if res, _ := x.do(t, "GET", "/api/runs/"+other+"/journal", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token = %d", res.StatusCode)
	}
}
