package ports

import (
	"strings"
	"testing"
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
}
