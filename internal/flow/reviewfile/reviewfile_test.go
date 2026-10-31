package reviewfile

import (
	"strings"
	"testing"
)

const review = `summary: |
  Reads well; two things.
notes:
  - path: ./a.go
    line: 3
    kind: blocking
    body: "This error drops the line number."
    rationale: the caller formats it into the message
  - path: b.go
    line: 9
    kind: nit
    body: name it
    rationale: style
replies:
  - thread: t1
    body: Addressed in 3f2a1c, thanks.
    resolve: true
`

func TestParseReview(t *testing.T) {
	r, err := ParseReview("artifacts/review.yaml", []byte(review))
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary != "Reads well; two things." || len(r.Notes) != 2 || r.Notes[0].Path != "a.go" || len(r.Replies) != 1 || !r.Replies[0].Resolve {
		t.Fatalf("review = %+v", r)
	}
	if got := r.Notes[0].Posted(); got != "[blocking] This error drops the line number." {
		t.Errorf("posted = %q", got)
	}
	// Empty is an empty review; JSON by extension.
	if r, err := ParseReview("artifacts/review.yaml", []byte("  \n")); err != nil || !r.Empty() {
		t.Errorf("empty: %+v %v", r, err)
	}
	if r, err := ParseReview("artifacts/review.json", []byte(`{"summary":"ok"}`)); err != nil || r.Summary != "ok" {
		t.Errorf("json: %+v %v", r, err)
	}
	refused := []struct{ name, doc, want string }{
		{"unknown key", "threads: []\n", "not a review"},
		{"absolute path", "notes:\n  - { path: /etc/passwd, line: 1, kind: nit, body: x, rationale: y }\n", "must be a file of the repository"},
		{"climbing path", "notes:\n  - { path: ../x.go, line: 1, kind: nit, body: x, rationale: y }\n", "must be a file of the repository"},
		{"line 0", "notes:\n  - { path: a.go, line: 0, kind: nit, body: x, rationale: y }\n", "line must be 1 or more"},
		{"bad kind", "notes:\n  - { path: a.go, line: 1, kind: major, body: x, rationale: y }\n", "kind must be one of"},
		{"empty body", "notes:\n  - { path: a.go, line: 1, kind: nit, body: ' ', rationale: y }\n", "body is empty"},
		{"no rationale", "notes:\n  - { path: a.go, line: 1, kind: nit, body: x }\n", "rationale is required"},
		{"reply without thread", "replies:\n  - { body: x }\n", "thread is required"},
		{"reply twice", "replies:\n  - { thread: t, body: x }\n  - { thread: t, body: y }\n", "answered twice"},
		{"empty reply", "replies:\n  - { thread: t, body: '' }\n", "body is empty"},
	}
	for _, c := range refused {
		_, err := ParseReview("artifacts/review.yaml", []byte(c.doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	var many strings.Builder
	many.WriteString("notes:\n")
	for i := 0; i <= MaxItems; i++ {
		many.WriteString("  - { path: a.go, line: 1, kind: nit, body: x, rationale: y }\n")
	}
	if _, err := ParseReview("r.yaml", []byte(many.String())); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many: %v", err)
	}
}

func TestParseReplies(t *testing.T) {
	doc := "- { thread: d1, did: fixed, reply: Renamed it. }\n- { thread: d2, did: declined, reply: Left as is., rationale: the test covers it }\n"
	rs, err := ParseReplies("artifacts/replies.yaml", []byte(doc))
	if err != nil || len(rs) != 2 || rs[1].Did != DidDeclined {
		t.Fatalf("replies = %+v, %v", rs, err)
	}
	if err := CheckReplies(rs, []string{"d1", "d2"}); err != nil {
		t.Error(err)
	}
	if err := CheckReplies(rs, []string{"d1", "d2", "d3"}); err == nil || !strings.Contains(err.Error(), "no reply for thread(s) d3") {
		t.Errorf("missing: %v", err)
	}
	if err := CheckReplies(rs, []string{"d1"}); err == nil || !strings.Contains(err.Error(), `"d2" is not one of this round's threads`) {
		t.Errorf("unknown: %v", err)
	}
	refused := []struct{ name, doc, want string }{
		{"no did", "- { thread: d1, reply: x }\n", "did must be one of"},
		{"empty reply", "- { thread: d1, did: fixed, reply: ' ' }\n", "is empty"},
		{"declined resolves", "- { thread: d1, did: declined, reply: x, resolve: true, rationale: r }\n", "is not resolved"},
		{"declined without why", "- { thread: d1, did: declined, reply: x }\n", "needs a rationale"},
		{"twice", "- { thread: d1, did: fixed, reply: x }\n- { thread: d1, did: fixed, reply: y }\n", "answered twice"},
		{"unknown key", "- { thread: d1, did: fixed, reply: x, note: y }\n", "not a list"},
	}
	for _, c := range refused {
		_, err := ParseReplies("artifacts/replies.yaml", []byte(c.doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestThreadsRoundTrip(t *testing.T) {
	in := &Threads{MergeRequest: "https://x/mr/1", Threads: []Thread{
		{ID: "t1", Path: "a.go", Line: 3, Ours: true, Kind: KindNit, Hunk: "@@ -1 +1 @@\n-a\n+b", Comments: []Comment{
			{ID: "c1", Author: "factory", Ours: true, Body: "[nit] name it\n\n<!-- apothecary review=r part=note-0 -->"},
			{ID: "c2", Author: "alice", New: true, Body: "done"},
		}},
	}, Dropped: []Note{{Path: "b.go", Line: 1, Kind: KindNit, Body: "x", Rationale: "y"}}}
	data, err := Encode("artifacts/threads.yaml", in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hunk: |-") {
		t.Errorf("multi-line text is not a block scalar:\n%s", data)
	}
	out, err := ParseThreads("artifacts/threads.yaml", data)
	if err != nil {
		t.Fatal(err)
	}
	if out.Thread("t1") == nil || !out.Thread("t1").Comments[1].New || len(out.Dropped) != 1 || strings.Join(out.IDs(), ",") != "t1" {
		t.Errorf("threads = %+v", out)
	}
	if _, err := ParseThreads("t.yaml", []byte("threads:\n  - { id: a, resolved: false, ours: false, comments: [] }\n  - { id: a, resolved: false, ours: false, comments: [] }\n")); err == nil {
		t.Error("a thread listed twice parses")
	}
}

func TestNormalizeBody(t *testing.T) {
	a := NormalizeBody("[Blocking]  This   error drops\nthe line.\n\n<!-- apothecary review=r/x part=note-1 kind=blocking -->")
	b := NormalizeBody("this error drops the line.")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
	if StripMarkers("hi <!-- apothecary run=r thread=t --> there") != "hi  there" {
		t.Errorf("strip = %q", StripMarkers("hi <!-- apothecary run=r thread=t --> there"))
	}
}

func TestCurationOfAReview(t *testing.T) {
	doc, err := ParseReview("r.yaml", []byte(review))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	refused := []struct {
		name string
		c    Curation
		want string
	}{
		{"bad key", Curation{Items: []CuratedItem{{Item: "x/1", Keep: true}}}, "not notes/<i>"},
		{"out of range", Curation{Items: []CuratedItem{{Item: "notes/2", Keep: true}}}, "no such item"},
		{"twice", Curation{Items: []CuratedItem{{Item: "notes/0", Keep: true}, {Item: "notes/0", Keep: false}}}, "decided twice"},
		{"resolve a note", Curation{Items: []CuratedItem{{Item: "notes/0", Keep: true, Resolve: &yes}}}, "only a reply resolves"},
		{"too long", Curation{Items: []CuratedItem{{Item: "notes/0", Keep: true, Body: strings.Repeat("x", MaxRewriteBytes+1)}}}, "longer than"},
	}
	for _, c := range refused {
		if err := ValidateReview(doc, &c.c); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// Tidied: a text equal to the original is no rewrite; the summary too.
	same := "Reads well; two things."
	no := false
	c := Curation{Summary: &same, Items: []CuratedItem{
		{Item: "notes/0", Keep: true, Body: " This error drops the line number. "},
		{Item: "notes/1", Keep: false},
		{Item: "replies/0", Keep: true, Body: "Fixed, thanks.", Resolve: &no},
	}}
	if err := ValidateReview(doc, &c); err != nil {
		t.Fatal(err)
	}
	if c.Summary != nil || c.Items[0].Body != "" || c.Items[2].Body != "Fixed, thanks." || c.Items[2].Resolve == nil {
		t.Errorf("tidied = %+v", c)
	}
	out, ap := ApplyReview(*doc, c)
	if len(out.Notes) != 1 || out.Notes[0].Path != "a.go" || len(out.Replies) != 1 || out.Replies[0].Body != "Fixed, thanks." || out.Replies[0].Resolve {
		t.Errorf("applied = %+v", out)
	}
	if len(ap.Dropped) != 1 || ap.Dropped[0].Path != "b.go" || strings.Join(ap.Rewritten, ",") != "replies/0" || len(ap.DroppedReplies) != 0 {
		t.Errorf("applied report = %+v", ap)
	}
	// A new summary is a rewrite.
	s := "Shorter."
	out, ap = ApplyReview(*doc, Curation{Summary: &s})
	if out.Summary != "Shorter." || strings.Join(ap.Rewritten, ",") != "summary" || len(out.Notes) != 2 {
		t.Errorf("summary rewrite = %+v %+v", out, ap)
	}
}

func TestCurationOfReplies(t *testing.T) {
	list, err := ParseReplies("r.yaml", []byte("- { thread: d1, did: fixed, reply: one }\n- { thread: d2, did: answered, reply: two }\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := "x"
	if err := ValidateReplies(list, &Curation{Summary: &s}); err == nil {
		t.Error("a summary on replies")
	}
	yes := true
	c := Curation{Items: []CuratedItem{{Item: "replies/0", Keep: false}, {Item: "replies/1", Keep: true, Body: "two, really", Resolve: &yes}}}
	if err := ValidateReplies(list, &c); err != nil {
		t.Fatal(err)
	}
	out, ap := ApplyReplies(list, c)
	if len(out) != 1 || out[0].Thread != "d2" || out[0].Reply != "two, really" || !out[0].Resolve {
		t.Errorf("applied = %+v", out)
	}
	if strings.Join(ap.DroppedReplies, ",") != "d1" || strings.Join(ap.Rewritten, ",") != "replies/1" {
		t.Errorf("report = %+v", ap)
	}
}

func TestEncodeByExtension(t *testing.T) {
	r := Review{Summary: "a\nb", Notes: []Note{{Path: "a.go", Line: 1, Kind: KindNit, Body: "x", Rationale: "y"}}}
	y, err := Encode("r.yaml", r)
	if err != nil || !strings.HasPrefix(string(y), "summary: |-\n  a\n  b\n") {
		t.Errorf("yaml = %q %v", y, err)
	}
	j, err := Encode("r.json", r)
	if err != nil || !strings.HasPrefix(string(j), "{\n  \"summary\"") {
		t.Errorf("json = %q %v", j, err)
	}
	for _, p := range []string{"r.yaml", "r.json"} {
		data, _ := Encode(p, r)
		back, err := ParseReview(p, data)
		if err != nil || back.Summary != "a\nb" || len(back.Notes) != 1 {
			t.Errorf("%s round trip: %+v %v", p, back, err)
		}
	}
}
