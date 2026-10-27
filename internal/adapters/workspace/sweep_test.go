package workspace

import (
	"context"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

// A marked command gets the marker in its environment and the sweep ahead of
// its script; an unmarked one gets neither.
func TestExecMarkerSweepsAndTags(t *testing.T) {
	r := &scriptRunner{}
	a := newTestAdapter(t, r)
	ctx := context.Background()
	if _, err := a.Exec(ctx, "apo-x-1", ports.Command{Script: "make test", Dir: "repo", Marker: "run1:test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, "apo-x-1", ports.Command{Script: "git status", Dir: "repo"}); err != nil {
		t.Fatal(err)
	}
	var execs []call
	for _, c := range r.calls {
		if len(c.args) > 0 && c.args[0] == "exec" {
			execs = append(execs, c)
		}
	}
	if len(execs) != 2 {
		t.Fatalf("exec calls = %d: %+v", len(execs), r.calls)
	}
	marked, plain := execs[0], execs[1]
	if i := slices.Index(marked.args, "--remote-env"); i < 0 || marked.args[i+1] != "APOTHECARY_CMD=run1:test" {
		t.Errorf("marked exec args = %v, want --remote-env APOTHECARY_CMD=run1:test", marked.args)
	}
	script := string(marked.stdin)
	if !strings.Contains(script, "__apo_sweep") || !strings.Contains(script, "'APOTHECARY_CMD=run1:test'") {
		t.Errorf("marked script lacks the sweep:\n%s", script)
	}
	if !strings.HasSuffix(strings.TrimSpace(script), "make test") {
		t.Errorf("the command must run after the sweep:\n%s", script)
	}
	if slices.Contains(plain.args, "--remote-env") || strings.Contains(string(plain.stdin), "__apo_sweep") {
		t.Errorf("unmarked exec must not sweep or tag: %v\n%s", plain.args, plain.stdin)
	}
}
