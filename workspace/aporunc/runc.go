package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// realRuncEnv names the runc to exec, for a node whose runc is not the first
// other runc on PATH.
const realRuncEnv = "APO_RUNC"

// execFunc replaces the process (syscall.Exec); a test's records the call.
type execFunc func(path string, argv, env []string) error

// run is the whole command: rewrite the bundle's spec when the subcommand
// makes a container, then exec the real runc with the arguments as given. It
// returns only when nothing could be exec'd.
func run(args []string, getenv func(string) string, exec execFunc, stderr io.Writer) int {
	sub, rest := subcommand(args[1:])
	if sub == "create" || sub == "run" {
		cfg := filepath.Join(bundle(rest), "config.json")
		if err := rewriteFile(cfg, sandboxCPU()); err != nil {
			// runc reads the spec itself and says what is wrong with it.
			fmt.Fprintf(stderr, "apo-runc: %s left as it is: %v\n", cfg, err)
		}
	}
	self, _ := os.Executable()
	real, err := realRunc(getenv, self)
	if err != nil {
		fmt.Fprintf(stderr, "apo-runc: %v\n", err)
		return 127
	}
	err = exec(real, append([]string{real}, args[1:]...), os.Environ())
	fmt.Fprintf(stderr, "apo-runc: exec %s: %v\n", real, err)
	return 126
}

// globalValueFlags are runc's global flags that take a value as the next
// argument (when not written --flag=value). Any other flag is a switch.
var globalValueFlags = []string{"log", "log-format", "root", "criu", "rootless"}

// subcommand is runc's subcommand, past its global flags, and the arguments
// after it; "" when there is none (runc --version).
func subcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a, args[i+1:]
		}
		name := strings.TrimLeft(a, "-")
		if !strings.Contains(name, "=") && slices.Contains(globalValueFlags, name) {
			i++
		}
	}
	return "", nil
}

// bundle is the --bundle (-b) of a create or run, else the working
// directory, as runc reads it.
func bundle(args []string) string {
	for i, a := range args {
		name, value, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || (name != "bundle" && name != "b") {
			continue
		}
		if inline {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return "."
}

// rewriteFile rewrites the spec at path in place when rewrite changes it:
// a file beside it with its mode, renamed over it.
func rewriteFile(path string, cpu *cpuMax) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed, err := rewrite(b, cpu)
	if err != nil || !changed {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp := path + ".apo-runc"
	if err := os.WriteFile(tmp, out, st.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// mount is what rewrite reads of a spec's mount.
type mount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Options     []string `json:"options"`
}

// rewrite makes a writable sysfs at /sys read-only and, when cpu is the
// sandbox's limit, gives a container with no CPU quota that one. changed is
// false, and spec is returned as it came, when neither applies; so it is
// when the spec cannot be read. Every other field and mount keeps its
// value, compacted.
func rewrite(spec []byte, cpu *cpuMax) (out []byte, changed bool, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(spec, &top); err != nil {
		return spec, false, err
	}
	sysfs, err := rewriteSysfs(top)
	if err != nil {
		return spec, false, err
	}
	quota, err := rewriteCPU(top, cpu)
	if err != nil {
		return spec, false, err
	}
	if !sysfs && !quota {
		return spec, false, nil
	}
	out, _ = json.Marshal(top)
	return out, true, nil
}

// rewriteSysfs adds ro to every writable sysfs at /sys in top's mounts.
func rewriteSysfs(top map[string]json.RawMessage) (changed bool, err error) {
	raw, ok := top["mounts"]
	if !ok {
		return false, nil
	}
	var mounts []json.RawMessage
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return false, fmt.Errorf("mounts: %w", err)
	}
	for i, m := range mounts {
		var mt mount
		if err := json.Unmarshal(m, &mt); err != nil {
			return false, fmt.Errorf("mount %d: %w", i, err)
		}
		if !writableSysfs(mt) {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(m, &fields); err != nil {
			return false, fmt.Errorf("mount %d: %w", i, err)
		}
		opts := slices.DeleteFunc(slices.Clone(mt.Options), func(o string) bool { return o == "rw" })
		fields["options"], _ = json.Marshal(append(opts, "ro"))
		mounts[i], _ = json.Marshal(fields)
		changed = true
	}
	if changed {
		top["mounts"], _ = json.Marshal(mounts)
	}
	return changed, nil
}

// writableSysfs: a fresh sysfs (not a bind of one) at /sys without ro.
func writableSysfs(m mount) bool {
	if m.Type != "sysfs" || filepath.Clean(m.Destination) != "/sys" {
		return false
	}
	return !slices.ContainsFunc(m.Options, func(o string) bool { return o == "ro" || o == "rro" })
}

// cpuMax is a cgroup's CPU limit, cgroup v2's cpu.max: quota microseconds
// of CPU time in every period of microseconds.
type cpuMax struct{ quota, period int64 }

// rewriteCPU gives a container with no CPU quota (none in its spec, or zero
// or less: unlimited) the sandbox's limit, in linux.resources.cpu. The
// sandbox's limit sits on a cgroup outside the container's cgroup namespace,
// so without one of its own the container reads "max" in its cpu.max, and
// Go's GOMAXPROCS and libuv's available parallelism (Node) count the node's
// CPUs. The quota changes nothing the kernel enforces: the sandbox's cgroup
// is the container's ancestor and holds it to the same limit already. A
// container that has a quota keeps it, even a wider one. There is no
// cpuset: pinning cores is a cost the quota does not have, so nproc and
// /proc/cpuinfo still count the node's CPUs.
func rewriteCPU(top map[string]json.RawMessage, limit *cpuMax) (changed bool, err error) {
	if limit == nil {
		return false, nil
	}
	linux, err := object(top, "linux", "linux")
	if err != nil {
		return false, err
	}
	resources, err := object(linux, "resources", "linux.resources")
	if err != nil {
		return false, err
	}
	cpu, err := object(resources, "cpu", "linux.resources.cpu")
	if err != nil {
		return false, err
	}
	if raw, ok := cpu["quota"]; ok && string(raw) != "null" {
		var q int64
		if err := json.Unmarshal(raw, &q); err != nil {
			return false, fmt.Errorf("linux.resources.cpu.quota: %w", err)
		}
		if q > 0 {
			return false, nil
		}
	}
	cpu["quota"], _ = json.Marshal(limit.quota)
	cpu["period"], _ = json.Marshal(limit.period)
	resources["cpu"], _ = json.Marshal(cpu)
	linux["resources"], _ = json.Marshal(resources)
	top["linux"], _ = json.Marshal(linux)
	return true, nil
}

// object is the JSON object under key in m, empty when there is none (or it
// is null); path names it in an error.
func object(m map[string]json.RawMessage, key, path string) (map[string]json.RawMessage, error) {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// Where sandboxCPU reads; a test points them at a tree of its own.
var (
	cgroupRoot = "/sys/fs/cgroup"
	selfCgroup = "/proc/self/cgroup"
)

// sandboxCPU is the tightest CPU limit on this process's cgroup and its
// ancestors up to the root of its cgroup namespace; nil when none has one,
// or the cgroup v2 tree cannot be read. Run by containerd's shim, the
// wrapper sits in dockerd's cgroup (the dind script's /init, no limit), so
// the limit found is the sandbox's own, on the namespace's root: the pod's
// container cgroup, which the kubelet gives the sandbox's CPU limit. On a
// k3s node nested in the sandbox, whose runc this wrapper is too, it is the
// node container's quota, which this wrapper gave it.
func sandboxCPU() *cpuMax {
	b, err := os.ReadFile(selfCgroup)
	if err != nil {
		return nil
	}
	rel, found := "", false
	for line := range strings.Lines(string(b)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			rel, found = p, true
			break
		}
	}
	if !found {
		return nil
	}
	root := filepath.Clean(cgroupRoot)
	dir := filepath.Join(root, rel)
	if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return nil // a cgroup outside this namespace ("/../..")
	}
	var best *cpuMax
	for {
		if m, ok := readCPUMax(filepath.Join(dir, "cpu.max")); ok &&
			(best == nil || float64(m.quota)/float64(m.period) < float64(best.quota)/float64(best.period)) {
			best = &m
		}
		if dir == root {
			return best
		}
		dir = filepath.Dir(dir)
	}
}

// readCPUMax reads a cpu.max file; false for "max" (no limit) or anything
// it cannot read.
func readCPUMax(path string) (cpuMax, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cpuMax{}, false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return cpuMax{}, false
	}
	q, err1 := strconv.ParseInt(f[0], 10, 64)
	p, err2 := strconv.ParseInt(f[1], 10, 64)
	if err1 != nil || err2 != nil || q <= 0 || p <= 0 {
		return cpuMax{}, false
	}
	return cpuMax{q, p}, true
}

// realRunc is the runc to exec: $APO_RUNC, else the first runc on PATH that
// is not this program (it may be installed as runc).
func realRunc(getenv func(string) string, self string) (string, error) {
	if p := getenv(realRuncEnv); p != "" {
		return p, nil
	}
	selfID, _ := identity(self)
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		p := filepath.Join(dir, "runc")
		id, err := identity(p)
		if err != nil || id == selfID {
			continue
		}
		return p, nil
	}
	return "", errors.New("no runc on PATH other than this wrapper (set " + realRuncEnv + ")")
}

// identity names an executable file by the path it resolves to; an error
// for one that is missing or not executable.
func identity(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(r)
	if err != nil {
		return "", err
	}
	if st.IsDir() || st.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable", p)
	}
	return r, nil
}
