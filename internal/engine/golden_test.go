package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/event"
)

// Golden event logs pin what every scenario test appends, so a refactor that
// changes the interpreter's behaviour shows up as a diff even when the test's
// own assertions still pass. Each harness registers itself; when the test ends
// (after every engine has shut down) the events of all its runs are rendered,
// one line per event, into testdata/golden/<test>.golden. GOLDEN=record writes
// the files; otherwise a test with a file is compared against it and a test
// without one is not checked. A golden file changes only in a commit whose
// message names the behaviour change that changed it.

var golden = struct {
	mu        sync.Mutex
	harnesses map[*testing.T][]*harness
}{harnesses: map[*testing.T][]*harness{}}

// registerGolden adds h to its test's golden log. It must be called before the
// harness registers its engine's Shutdown, so that (cleanups being LIFO) the
// log is rendered after every engine of the test has stopped appending.
func registerGolden(t *testing.T, h *harness) {
	// A log per crash point, none of them worth keeping: that test holds
	// every one of them to the clean run instead.
	if strings.HasPrefix(t.Name(), "TestCrashAtEveryEvent") {
		return
	}
	golden.mu.Lock()
	defer golden.mu.Unlock()
	first := len(golden.harnesses[t]) == 0
	golden.harnesses[t] = append(golden.harnesses[t], h)
	if !first {
		return
	}
	t.Cleanup(func() {
		golden.mu.Lock()
		hs := golden.harnesses[t]
		delete(golden.harnesses, t)
		golden.mu.Unlock()
		if t.Failed() || t.Skipped() {
			return
		}
		checkGolden(t, renderGolden(t, hs))
	})
}

// renderGolden renders every run of every distinct store the test's harnesses
// used (a restart test hands one store to a second harness).
func renderGolden(t *testing.T, hs []*harness) string {
	var b strings.Builder
	seen := map[*event.MemStore]bool{}
	n := 0
	for _, h := range hs {
		if seen[h.store] {
			continue
		}
		seen[h.store] = true
		ctx := context.Background()
		ids, err := h.store.RunIDs(ctx)
		if err != nil {
			t.Fatalf("golden: run ids: %v", err)
		}
		for _, id := range ids {
			evs, err := h.store.Events(ctx, id, 0)
			if err != nil {
				t.Fatalf("golden: events of %s: %v", id, err)
			}
			n++
			fmt.Fprintf(&b, "# run %d\n", n)
			for i := range evs {
				b.WriteString(goldenLine(&evs[i]))
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// goldenLine is an event's type plus the few fields that say where the run
// went; payload details (prompts, costs, ids, times) are left out so the log
// stays stable across runs.
func goldenLine(ev *event.Event) string {
	parts := []string{string(ev.Type)}
	pv := reflect.ValueOf(ev.Payload)
	if pv.Kind() == reflect.Pointer && !pv.IsNil() {
		pv = pv.Elem()
	}
	if pv.Kind() == reflect.Struct {
		if f := pv.FieldByName("StageID"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
			parts = append(parts, "stage="+f.String())
		}
	}
	switch p := ev.Payload.(type) {
	case *event.EdgeTaken:
		parts = append(parts, fmt.Sprintf("edge=%s:%s->%s", p.From, p.Edge, p.To))
	case *event.RunPaused:
		parts = append(parts, "reason="+p.Reason)
	case *event.PhaseStarted:
		parts = append(parts, "phase="+p.Phase)
	case *event.PhaseCompleted:
		parts = append(parts, "phase="+p.Phase)
	case *event.PhaseParked:
		parts = append(parts, "phase="+p.Phase)
	case *event.PhaseResumed:
		parts = append(parts, "phase="+p.Phase)
	case *event.PhaseDeclined:
		parts = append(parts, "phase="+p.Phase)
	case *event.PingRaised:
		parts = append(parts, "ping="+p.Type)
	}
	return strings.Join(parts, " ")
}

var unsafeGoldenName = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func goldenPath(t *testing.T) string {
	name := strings.ReplaceAll(t.Name(), "/", "__")
	return filepath.Join("testdata", "golden", unsafeGoldenName.ReplaceAllString(name, "_")+".golden")
}

func checkGolden(t *testing.T, got string) {
	path := goldenPath(t)
	if os.Getenv("GOLDEN") == "record" {
		if got == "" {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	if got == string(want) {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := 0; i < max(len(gl), len(wl)); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Errorf("golden %s differs at line %d:\n  want: %q\n  got:  %q\n(GOLDEN=record go test ./internal/engine/ -run '%s' rewrites it)", path, i+1, w, g, t.Name())
			return
		}
	}
}
