package workspace

// The workspace's environment: what the engine adds to every process in a
// workspace, over the repository's own containerEnv and remoteEnv.
//
//   - Size hints, from the sandbox's limits as its pod was made (APO_CPUS,
//     the CPU limit rounded up to whole CPUs; APO_MEMORY_MIB): what a test
//     runner sizes its workers from when nproc reports the node's CPUs (a
//     sysbox sandbox sees all of them). A hint is unset when the sandbox has
//     no such limit. A VM runtime's hint is the limit too, not the guest's
//     vCPU count (which Kata pads with one of its own).
//   - The project's variables (WorkspaceConfig.Env), read at each exec, so a
//     change reaches the next process with no rebuild. ${APO_CPUS} and
//     ${APO_MEMORY_MIB} in a value expand to the hint; nothing else does.
//
// Last wins: repository < hints < project. Every exec rides `env K=V … cmd`
// inside `devcontainer exec` (inWorkspace) rather than --remote-env, which
// the CLI lays UNDER the configuration's remoteEnv. The sidecar is started
// without them and is told the set over its API instead (syncSidecarEnv):
// it lays them over what it starts (the harness, terminals, SSH sessions)
// at each start, so a long-lived sidecar never hands out a stale set, and a
// variable the project drops is gone, not left behind in its environment.

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/sidecar"
)

// The engine's size hints.
const (
	hintCPUs      = "APO_CPUS"
	hintMemoryMiB = "APO_MEMORY_MIB"
)

// envSourceFile records what an env's environment is made from: its project
// (whose variables are read at each exec) and its sandbox's limits.
const envSourceFile = "workspace-env.json"

type envSource struct {
	Project string              `json:"project"`
	Limits  envport.SandboxSize `json:"limits"`
}

// sizeHints are the hints for a sandbox's limits: whole CPUs, rounded up
// (1500 mCPU is 2: a pool of one would leave half a CPU idle), and MiB.
func sizeHints(limits envport.SandboxSize) map[string]string {
	h := map[string]string{}
	if limits.MilliCPU > 0 {
		h[hintCPUs] = strconv.Itoa(int(math.Ceil(float64(limits.MilliCPU) / 1000)))
	}
	if limits.MemoryMiB > 0 {
		h[hintMemoryMiB] = strconv.Itoa(limits.MemoryMiB)
	}
	return h
}

// expandHints replaces ${APO_CPUS} and ${APO_MEMORY_MIB} in v with the
// hint, "" when the sandbox has no such limit. Anything else stays as
// written: a value is never a shell word.
func expandHints(v string, hints map[string]string) string {
	if !strings.Contains(v, "${") {
		return v
	}
	return strings.NewReplacer(
		"${"+hintCPUs+"}", hints[hintCPUs],
		"${"+hintMemoryMiB+"}", hints[hintMemoryMiB],
	).Replace(v)
}

// composeEnv is the workspace's environment as NAME=value, hints first and
// the project's after them (the later wins in `env`), each sorted by name.
func composeEnv(hints, project map[string]string) []string {
	out := make([]string, 0, len(hints)+len(project))
	for _, k := range slices.Sorted(maps.Keys(hints)) {
		out = append(out, k+"="+hints[k])
	}
	for _, k := range slices.Sorted(maps.Keys(project)) {
		out = append(out, k+"="+expandHints(project[k], hints))
	}
	return out
}

// recordEnvSource keeps what env name's environment is made from, beside the
// engine's other markers, so an engine restart keeps the hints the sandbox
// was made with.
func (a *Adapter) recordEnvSource(name string, src envSource) {
	a.envMu.Lock()
	a.envSrc[name] = src
	a.envMu.Unlock()
	if b, err := json.Marshal(src); err == nil {
		_ = os.WriteFile(filepath.Join(a.folderFor(name), envSourceFile), b, 0o644)
	}
}

// envSourceFor is what recordEnvSource kept; zero for an env provisioned
// before it existed (no hints, no project variables until its next
// provision).
func (a *Adapter) envSourceFor(name string) envSource {
	a.envMu.Lock()
	defer a.envMu.Unlock()
	if s, ok := a.envSrc[name]; ok {
		return s
	}
	var s envSource
	if b, err := os.ReadFile(filepath.Join(a.folderFor(name), envSourceFile)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	a.envSrc[name] = s
	return s
}

// projectVars is the project's variables as stored now. A failed read keeps
// the last set this env had, rather than starting a process without them.
func (a *Adapter) projectVars(ctx context.Context, name, project string) map[string]string {
	if a.cfg.ProjectEnv == nil || project == "" {
		return nil
	}
	vars, err := a.cfg.ProjectEnv(ctx, project)
	a.envMu.Lock()
	defer a.envMu.Unlock()
	if err != nil {
		slog.Warn("workspace env: reading the project's variables failed; using the last ones read",
			"env", name, "project", project, "err", err)
		return a.lastVars[name]
	}
	a.lastVars[name] = vars
	return vars
}

// workspaceEnv is env name's environment now (composeEnv).
func (a *Adapter) workspaceEnv(ctx context.Context, name string) []string {
	src := a.envSourceFor(name)
	return composeEnv(sizeHints(src.Limits), a.projectVars(ctx, name, src.Project))
}

// logWorkspaceEnv says once, at provision, what the workspace's processes
// get. Values too: none is a secret.
func (a *Adapter) logWorkspaceEnv(ctx context.Context, name string, plog func(string, ...any)) {
	src := a.envSourceFor(name)
	hints := sizeHints(src.Limits)
	hintList := "none (no CPU or memory limit)"
	if len(hints) > 0 {
		hintList = strings.Join(composeEnv(hints, nil), " ")
	}
	plog("→ workspace environment: size hints %s\n", hintList)
	vars := a.projectVars(ctx, name, src.Project)
	if len(vars) == 0 {
		plog("→ workspace environment: no project variables\n")
		return
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		line := k + "=" + vars[k]
		if x := expandHints(vars[k], hints); x != vars[k] {
			line += " (→ " + x + ")"
		}
		plog("→ workspace environment: project %s\n", line)
	}
}

// syncSidecarEnv tells env name's sidecar the workspace's environment when
// it is not the set it was last told. A sidecar that predates the route is
// told nothing again (what it starts has the environment it was started
// with); any other failure is retried on the next dial.
func (a *Adapter) syncSidecarEnv(ctx context.Context, name string, conn envport.PortConn) {
	vars := a.workspaceEnv(ctx, name)
	key := strings.Join(vars, "\x00")
	a.envMu.Lock()
	told, ok := a.sidecarEnv[name]
	a.envMu.Unlock()
	if ok && told == key {
		return
	}
	m := make(map[string]string, len(vars))
	for _, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	// A sidecar that accepts and never answers must not hold up the dial.
	sctx, cancel := context.WithTimeout(ctx, a.sidecarEnvTimeout)
	defer cancel()
	err := sidecar.New(conn).SetEnv(sctx, m)
	if err != nil && !sidecar.IsNoRoute(err) {
		slog.Warn("workspace env: telling the sidecar failed; retried on its next use", "env", name, "err", err)
		return
	}
	if err != nil {
		slog.Info("workspace env: the sidecar predates the workspace environment; a rebuild brings it", "env", name)
	}
	a.envMu.Lock()
	a.sidecarEnv[name] = key
	a.envMu.Unlock()
}

// forgetSidecarEnv: the next sidecar of env name is a new one, told nothing.
func (a *Adapter) forgetSidecarEnv(name string) {
	a.envMu.Lock()
	delete(a.sidecarEnv, name)
	a.envMu.Unlock()
}

// podLimits are the sandbox container's limits in pod, as sandboxResources
// writes them ("4000m", "8192Mi"); a limit it cannot read is no limit.
func podLimits(pod kube.Pod) envport.SandboxSize {
	var s envport.SandboxSize
	for _, c := range pod.Spec.Containers {
		if c.Name != sandboxContainer || c.Resources == nil {
			continue
		}
		s.MilliCPU = parseMilliCPU(c.Resources.Limits["cpu"])
		s.MemoryMiB = parseMemoryMiB(c.Resources.Limits["memory"])
	}
	return s
}

// parseMilliCPU reads a CPU quantity: "1500m" or cores ("2", "0.5").
func parseMilliCPU(q string) int {
	if m, ok := strings.CutSuffix(q, "m"); ok {
		n, err := strconv.Atoi(m)
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	f, err := strconv.ParseFloat(q, 64)
	if err != nil || f < 0 {
		return 0
	}
	return int(math.Ceil(f * 1000))
}

// parseMemoryMiB reads a memory quantity in Mi, Gi or Ti; a plain byte
// count or another unit is not one sandboxResources writes.
func parseMemoryMiB(q string) int {
	for suffix, mul := range map[string]int{"Mi": 1, "Gi": 1 << 10, "Ti": 1 << 20} {
		if n, ok := strings.CutSuffix(q, suffix); ok {
			v, err := strconv.Atoi(n)
			if err != nil || v < 0 {
				return 0
			}
			return v * mul
		}
	}
	return 0
}

// envPrefix is the argv that sets vars over cmd's environment.
func envPrefix(vars []string) []string {
	if len(vars) == 0 {
		return nil
	}
	return append([]string{"env"}, vars...)
}
