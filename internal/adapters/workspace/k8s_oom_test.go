package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The memory limit's kills reach the run's exec log once each: the kernel's
// kills inside the sandbox, the sandbox container killed and restarted, and
// neither told twice across an engine restart nor lost to a new cgroup.
func TestK8sSampleTellsTheMemoryLimitsKills(t *testing.T) {
	ctx := context.Background()
	usage, oom := 1000, 0
	k := &kubeFake{exec: func([]string, []byte) (string, string, int) {
		return fmt.Sprintf("cpu %d\nmem 2048\nmax %d\noom %d\n", usage, 4096<<20, oom), "", 0
	}}
	a, h := k8sWith(t, k, false)
	pod := func(uid string, restarts int, reason string) string {
		last := ""
		if reason != "" {
			last = `,"lastState":{"terminated":{"exitCode":137,"reason":"` + reason + `"}}`
		}
		return fmt.Sprintf(`{"metadata":{"name":"w","uid":%q},"status":{"phase":"Running","containerStatuses":[{"name":"relay","restartCount":9},{"name":"sandbox","restartCount":%d%s}]}}`, uid, restarts, last)
	}
	log := func() string {
		b, _ := os.ReadFile(filepath.Join(a.folderFor("w"), "exec.log"))
		return string(b)
	}
	sample := func() {
		t.Helper()
		usage += 1000
		_, _ = h.sample(ctx, "w")
	}
	engineRestart := func() {
		h.mu.Lock()
		delete(h.stats, "w")
		h.mu.Unlock()
	}

	k.pod = pod("u1", 0, "")
	sample()
	sample()
	if log() != "" {
		t.Fatalf("nothing killed, yet: %q", log())
	}
	oom = 2
	sample()
	if got := log(); strings.Count(got, "the kernel killed 2 processes in the sandbox at its memory limit (4096 MiB)") != 1 {
		t.Fatalf("log = %q", got)
	}
	engineRestart()
	sample()
	sample()
	if got := log(); strings.Count(got, "killed") != 1 {
		t.Errorf("told again after an engine restart: %q", got)
	}

	// The sandbox container itself is killed and restarts: a new cgroup.
	k.pod = pod("u1", 1, "OOMKilled")
	usage, oom = 0, 0
	sample()
	if got := log(); !strings.Contains(got, "the sandbox container was killed at its memory limit (4096 MiB) and restarted") {
		t.Errorf("container OOM not told: %q", got)
	}
	oom = 1
	sample()
	if got := log(); strings.Count(got, "the kernel killed a process") != 1 {
		t.Errorf("the new cgroup's kill: %q", got)
	}
	// Another restart, not the memory limit's: nothing to tell, the count
	// starts again.
	k.pod = pod("u1", 2, "Error")
	usage, oom = 0, 0
	sample()
	before := log()
	// A new pod (a rebuild) while the engine was down: its count is its own.
	engineRestart()
	k.pod = pod("u2", 0, "")
	oom = 1
	sample()
	if got := strings.TrimPrefix(log(), before); strings.Count(got, "the kernel killed a process") != 1 || strings.Contains(got, "container was killed") {
		t.Errorf("new pod: %q", got)
	}
	// The pod cannot be read: the counts alone go on.
	k.pod = ""
	engineRestart()
	oom = 3
	sample()
	if got := log(); strings.Count(got, "the kernel killed 2 processes") != 2 {
		t.Errorf("unread pod: %q", got)
	}
	// A count that went back with no restart seen: a cgroup the log did not
	// see start, counted from nothing.
	oom = 1
	sample()
	if got := log(); strings.Count(got, "the kernel killed a process") != 3 {
		t.Errorf("count went back: %q", got)
	}
	if limitNote(cgroupSample{}) != "" {
		t.Error("an unlimited sandbox's note names a limit")
	}
}
