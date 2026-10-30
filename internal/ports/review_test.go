package ports

import (
	"strings"
	"testing"
	"time"
)

func TestDiffLines(t *testing.T) {
	patch := "@@ -1,4 +1,5 @@\n package a\n-old()\n+new()\n+more()\n context\n@@ -20 +21,2 @@\n line\n+added\n\\ No newline at end of file"
	got := DiffLines(patch)
	want := map[int]int{1: 1, 2: 0, 3: 0, 4: 3, 21: 20, 22: 0}
	if len(got) != len(want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	for n, old := range want {
		if o, ok := got[n]; !ok || o != old {
			t.Errorf("line %d = %d (%v), want %d", n, o, ok, old)
		}
	}
	if len(DiffLines("")) != 0 {
		t.Error("an empty patch has no lines")
	}
}

func TestFoldedNotesAndMarkers(t *testing.T) {
	if FoldedNotes(nil) != "" {
		t.Error("nothing folded, nothing said")
	}
	s := FoldedNotes([]ReviewNote{{Path: "a.go", Line: 3, Body: "rename\nthis"}})
	if !strings.Contains(s, "`a.go:3` — rename\n  this") {
		t.Errorf("folded = %q", s)
	}
	m := ReviewMarker("run/phase-1/post", "note-0")
	if !IsFactoryReply("nit\n\n"+m) || !IsFactoryReply(ReplyMarker("r", "t")) || IsFactoryReply("a reviewer's words") {
		t.Error("factory markers")
	}
	// Markers carry words: a note's kind, a reply's round, thread and did —
	// each read back on its own, whatever else the marker says.
	note := "name it\n\n" + NoteMarker("run/again-2/post", 3, "nit")
	if id, part, ok := ReviewMarkerOf(note); !ok || id != "run/again-2/post" || part != "note-3" || MarkerAttr(note, "kind") != "nit" {
		t.Errorf("review marker words: %q %q %v %q", id, part, ok, MarkerAttr(note, "kind"))
	}
	reply := "done\n\n" + ReplyMarker("run/review_fixes-1", "PRRT_kwDO", "did=fixed")
	if MarkerRound(reply) != "run/review_fixes-1" || MarkerThread(reply) != "PRRT_kwDO" || MarkerAttr(reply, "did") != "fixed" {
		t.Errorf("reply marker words: %q %q %q", MarkerRound(reply), MarkerThread(reply), MarkerAttr(reply, "did"))
	}
	if !IsReplyTo(reply, "run/review_fixes-1", "PRRT_kwDO") || IsReplyTo(reply, "run/review_fixes-2", "PRRT_kwDO") || IsReplyTo(reply, "run/review_fixes-1", "other") {
		t.Error("IsReplyTo")
	}
	if MarkerThread(ReplyMarker("r", "t")) != "t" || MarkerThread("no marker") != "" || MarkerAttr(note, "did") != "" {
		t.Error("bare markers and absent words")
	}
}

// A merge request is stacked on the open one whose branch it targets — never
// on a fork's (that branch lives elsewhere) nor on a closed one.
func TestStackedOn(t *testing.T) {
	open := []MRInfo{
		{Ref: MRRef{IID: 1, URL: "u1"}, Title: "base", Source: "feature/a", Target: "main", State: MROpen},
		{Ref: MRRef{IID: 2}, Source: "feature/f", Target: "main", State: MROpen, Fork: true},
		{Ref: MRRef{IID: 3}, Source: "feature/c", Target: "main", State: MRClosed},
	}
	if got := StackedOn("feature/a", open); got == nil || *got != (MRBase{IID: 1, URL: "u1", Title: "base", Branch: "feature/a"}) {
		t.Errorf("on feature/a = %+v", got)
	}
	for _, target := range []string{"main", "feature/f", "feature/c", ""} {
		if got := StackedOn(target, open); got != nil {
			t.Errorf("on %q = %+v, want none", target, got)
		}
	}
}

// A thread waits on the factory until our reply is its latest comment; the
// newest pending comment ignores factory replies.
func TestPendingThreads(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reply := "ok " + ReplyMarker("r1", "a")
	threads := []ReviewThread{
		{ID: "a", Comments: []ReviewComment{{ID: "a1", Body: "fix", CreatedAt: t0}, {ID: "a2", Body: reply, CreatedAt: t0.Add(time.Minute)}}},
		{ID: "b", Comments: []ReviewComment{{ID: "b1", Body: "why?", CreatedAt: t0.Add(2 * time.Minute)}}},
		{ID: "c", Resolved: true, Comments: []ReviewComment{{ID: "c1", Body: "nit", CreatedAt: t0.Add(3 * time.Minute)}}},
		{ID: "d"},
		{ID: "e", Comments: []ReviewComment{{ID: "e1", Body: "again", CreatedAt: t0}, {ID: "e2", Body: reply, CreatedAt: t0.Add(4 * time.Minute)}, {ID: "e3", Body: "still", CreatedAt: t0.Add(time.Minute)}}},
	}
	pending := PendingThreads(threads)
	if len(pending) != 2 || pending[0].ID != "b" || pending[1].ID != "e" {
		t.Fatalf("pending = %+v", pending)
	}
	if c, ok := NewestPending(pending); !ok || c.ID != "b1" {
		t.Fatalf("newest = %+v, %v", c, ok)
	}
	if _, ok := NewestPending(nil); ok {
		t.Fatal("newest of nothing found something")
	}
}
