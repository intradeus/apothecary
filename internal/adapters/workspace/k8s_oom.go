package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

// A sandbox reaching its memory limit is told in the run's exec log (the
// run page's workspace log), which outlives the engine and the pod: the
// kernel killing processes inside it (the cgroup's oom_kill count, read by
// every resource sample), and the sandbox container itself killed and
// restarted (its status's last state, OOMKilled). What the log has already
// told is kept beside it (oomSeenFile), so an engine restart neither tells
// it twice nor loses what happened while it was down.

// oomSeenFile, in the run folder: "<pod uid> <restarts> <kills>", the pod
// the counts are of, its sandbox container's restarts and the cgroup's
// kills the log has told.
const oomSeenFile = "oom-seen"

type oomSeen struct {
	pod      string
	restarts int
	kills    int
}

func (h *k8sHost) readOOMSeen(name string) oomSeen {
	var s oomSeen
	b, err := os.ReadFile(filepath.Join(h.a.folderFor(name), oomSeenFile))
	if err == nil {
		_, _ = fmt.Sscan(strings.TrimSpace(string(b)), &s.pod, &s.restarts, &s.kills)
	}
	return s
}

func (h *k8sHost) writeOOMSeen(name string, s oomSeen) {
	_ = os.MkdirAll(h.a.folderFor(name), 0o755)
	_ = os.WriteFile(filepath.Join(h.a.folderFor(name), oomSeenFile), fmt.Appendf(nil, "%s %d %d\n", s.pod, s.restarts, s.kills), 0o600)
}

// noteOOM tells the exec log what the sample cur shows of the memory limit
// since it last told. fresh is a reading with no live predecessor (the
// engine's first of this pod, or the container restarted under it): the pod
// is read then, to learn which pod and container run this cgroup.
func (h *k8sHost) noteOOM(ctx context.Context, name string, cur cgroupSample, fresh bool) {
	seen := h.readOOMSeen(name)
	was := seen
	if fresh {
		var pod kube.Pod
		if found, err := h.cfg.Kube.Get(ctx, "pod", name, &pod); err == nil && found {
			if pod.Metadata.UID != seen.pod {
				seen = oomSeen{pod: pod.Metadata.UID}
			}
			for _, c := range pod.Status.ContainerStatuses {
				if c.Name != sandboxContainer || c.RestartCount <= seen.restarts {
					continue
				}
				if t := c.LastState.Terminated; t != nil && t.Reason == "OOMKilled" {
					h.a.execNote(name, "the sandbox container was killed at its memory limit"+limitNote(cur)+" and restarted: its processes started again; a larger sandbox size avoids it")
				}
				// A new container is a new cgroup: its count starts again.
				seen.restarts, seen.kills = c.RestartCount, 0
			}
		}
	}
	if cur.oomKills < seen.kills {
		seen.kills = 0 // a cgroup the log has not seen start
	}
	if n := cur.oomKills - seen.kills; n > 0 {
		what := "a process"
		if n > 1 {
			what = fmt.Sprintf("%d processes", n)
		}
		h.a.execNote(name, "the kernel killed "+what+" in the sandbox at its memory limit"+limitNote(cur)+"; a larger sandbox size avoids it")
		seen.kills = cur.oomKills
	}
	if seen != was {
		h.writeOOMSeen(name, seen)
	}
}

func limitNote(s cgroupSample) string {
	if s.memLimit <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d MiB)", s.memLimit>>20)
}

// execNote appends a line of the engine's own to the env's exec log.
func (a *Adapter) execNote(name, line string) {
	f := a.openExecLog(envport.EnvID(name), envport.Command{Script: "# apothecary"})
	if f == nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n", line)
}
