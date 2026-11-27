package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A privileged container's spec as docker writes it: a fresh sysfs at /sys
// without ro, the cgroup mount over it, and fields rewrite must not touch.
const privilegedSpec = `{
  "ociVersion": "1.2.0",
  "process": {"args": ["sh"], "cwd": "/"},
  "root": {"path": "/var/lib/docker/rootfs", "readonly": false},
  "hostname": "c1",
  "mounts": [
    {"destination": "/proc", "type": "proc", "source": "proc", "options": ["nosuid", "noexec", "nodev"]},
    {"destination": "/sys", "type": "sysfs", "source": "sysfs", "options": ["nosuid", "noexec", "nodev", "rw"]},
    {"destination": "/sys/fs/cgroup", "type": "cgroup", "source": "cgroup", "options": ["nosuid", "noexec", "nodev", "relatime", "rw"]},
    {"destination": "/etc/hosts", "type": "bind", "source": "/var/lib/docker/containers/c1/hosts", "options": ["rbind", "rprivate"]}
  ],
  "linux": {"namespaces": [{"type": "network"}, {"type": "mount"}], "maskedPaths": []}
}`

func mounts(t *testing.T, spec []byte) []map[string]any {
	t.Helper()
	var s struct {
		Mounts []map[string]any `json:"mounts"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		t.Fatal(err)
	}
	return s.Mounts
}

func options(m map[string]any) []string {
	var out []string
	for _, o := range m["options"].([]any) {
		out = append(out, o.(string))
	}
	return out
}

// Only the writable sysfs at /sys changes, to read-only; every other mount
// and every other field keeps its value (its whitespace aside).
func TestRewriteMakesAWritableSysfsReadOnly(t *testing.T) {
	out, changed, err := rewrite([]byte(privilegedSpec), nil)
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	got, want := mounts(t, out), mounts(t, []byte(privilegedSpec))
	if len(got) != len(want) {
		t.Fatalf("%d mounts, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i]["destination"] == "/sys" {
			if o := options(got[i]); !slices.Equal(o, []string{"nosuid", "noexec", "nodev", "ro"}) {
				t.Errorf("/sys options = %v", o)
			}
			if got[i]["type"] != "sysfs" || got[i]["source"] != "sysfs" {
				t.Errorf("/sys = %v", got[i])
			}
			continue
		}
		a, _ := json.Marshal(got[i])
		b, _ := json.Marshal(want[i])
		if !bytes.Equal(a, b) {
			t.Errorf("mount %d changed: %s, was %s", i, a, b)
		}
	}
	var gotTop, wantTop map[string]json.RawMessage
	_ = json.Unmarshal(out, &gotTop)
	_ = json.Unmarshal([]byte(privilegedSpec), &wantTop)
	for k, v := range wantTop {
		if k == "mounts" {
			continue
		}
		var was bytes.Buffer
		_ = json.Compact(&was, v)
		if !bytes.Equal(gotTop[k], was.Bytes()) {
			t.Errorf("%s = %s, was %s", k, gotTop[k], v)
		}
	}
}

// An unprivileged container's sysfs is read-only already, a bind of /sys is
// not a fresh sysfs, and a spec with no /sys or no mounts at all has nothing
// to change: each comes back byte for byte.
func TestRewriteLeavesTheRestAlone(t *testing.T) {
	for name, spec := range map[string]string{
		"read-only sysfs":  `{"mounts":[{"destination":"/sys","type":"sysfs","source":"sysfs","options":["nosuid","ro"]}]}`,
		"recursive ro":     `{"mounts":[{"destination":"/sys/","type":"sysfs","options":["rro"]}]}`,
		"a bind of /sys":   `{"mounts":[{"destination":"/sys","type":"bind","source":"/sys","options":["rbind","rw"]}]}`,
		"sysfs elsewhere":  `{"mounts":[{"destination":"/host/sys","type":"sysfs","options":["rw"]}]}`,
		"no /sys":          `{"mounts":[{"destination":"/proc","type":"proc"}]}`,
		"no mounts at all": `{"ociVersion":"1.2.0"}`,
	} {
		out, changed, err := rewrite([]byte(spec), nil)
		if err != nil || changed || string(out) != spec {
			t.Errorf("%s: changed %v, %v, %s", name, changed, err, out)
		}
	}
	// A sysfs with no options gets ro alone.
	out, changed, err := rewrite([]byte(`{"mounts":[{"destination":"/sys","type":"sysfs"}]}`), nil)
	if err != nil || !changed || !slices.Equal(options(mounts(t, out)[0]), []string{"ro"}) {
		t.Errorf("no options: %v, %v, %s", changed, err, out)
	}
}

func TestRewriteRefusesWhatItCannotRead(t *testing.T) {
	for name, spec := range map[string]string{
		"not json":                      `{`,
		"mounts not a list":             `{"mounts":{}}`,
		"a mount not an object":         `{"mounts":[1]}`,
		"a mount's options not strings": `{"mounts":[{"destination":"/sys","type":"sysfs","options":[1]}]}`,
	} {
		if out, changed, err := rewrite([]byte(spec), nil); err == nil || changed || string(out) != spec {
			t.Errorf("%s: changed %v, err %v", name, changed, err)
		}
	}
}

// subcommand skips runc's global flags, a value flag's value included;
// bundle reads each way runc takes it.
func TestArguments(t *testing.T) {
	for _, c := range []struct {
		args       []string
		sub, rest0 string
	}{
		{[]string{"--root", "/run/runc", "--log", "/l", "--log-format", "json", "--systemd-cgroup", "create", "--bundle", "/b", "id"}, "create", "--bundle"},
		{[]string{"--root=/run/runc", "-debug", "run", "id"}, "run", "id"},
		{[]string{"-rootless", "false", "--criu", "/c", "start", "id"}, "start", "id"},
		{[]string{"--version"}, "", ""},
		{nil, "", ""},
	} {
		sub, rest := subcommand(c.args)
		r0 := ""
		if len(rest) > 0 {
			r0 = rest[0]
		}
		if sub != c.sub || r0 != c.rest0 {
			t.Errorf("%v: %q, %v", c.args, sub, rest)
		}
	}
	for args, want := range map[string]string{
		"--bundle /b id":      "/b",
		"--bundle=/b id":      "/b",
		"-b /b id":            "/b",
		"-b=/b id":            "/b",
		"-bundle /b id":       "/b",
		"--pid-file /p id":    ".",
		"--console-socket /s": ".",
		"id --bundle":         ".",
	} {
		if got := bundle(strings.Fields(args)); got != want {
			t.Errorf("bundle(%s) = %q, want %q", args, got, want)
		}
	}
}

// exe writes an executable named name in dir.
func exe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// The real runc is $APO_RUNC, else the first runc on PATH that is not the
// wrapper itself, found by what the path resolves to.
func TestRealRunc(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	self := exe(t, a, "apo-runc")
	if err := os.Symlink(self, filepath.Join(a, "runc")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "runc"), []byte("x"), 0o644); err != nil { // not executable
		t.Fatal(err)
	}
	real := exe(t, c, "runc")
	path := strings.Join([]string{filepath.Join(a, "nope"), a, b, c}, string(os.PathListSeparator))
	if got, err := realRunc(env(map[string]string{"PATH": path}), self); err != nil || got != real {
		t.Errorf("PATH: %q, %v; want %q", got, err, real)
	}
	if got, err := realRunc(env(map[string]string{"PATH": path, realRuncEnv: "/opt/runc"}), self); err != nil || got != "/opt/runc" {
		t.Errorf("APO_RUNC: %q, %v", got, err)
	}
	if _, err := realRunc(env(map[string]string{"PATH": a + string(os.PathListSeparator) + b}), self); err == nil || !strings.Contains(err.Error(), realRuncEnv) {
		t.Errorf("only the wrapper: %v", err)
	}
	if err := os.Mkdir(filepath.Join(b, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := identity(filepath.Join(b, "dir")); err == nil {
		t.Error("a directory is not a runc")
	}
}

// run rewrites the bundle's spec on create, then execs the real runc with
// the arguments it was given; any other subcommand goes through untouched.
func TestRun(t *testing.T) {
	fakeCgroups(t, "0::/init\n", map[string]string{"cpu.max": "400000 100000\n", "init/cpu.max": "max 100000\n"})
	bin := t.TempDir()
	real := exe(t, bin, "runc")
	bundleDir := t.TempDir()
	cfg := filepath.Join(bundleDir, "config.json")
	if err := os.WriteFile(cfg, []byte(privilegedSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotPath string
	var gotArgv []string
	record := func(p string, argv, _ []string) error { gotPath, gotArgv = p, argv; return errors.New("recorded") }
	var stderr bytes.Buffer
	args := []string{"apo-runc", "--root", "/run/r", "create", "--bundle", bundleDir, "c1"}
	if code := run(args, env(map[string]string{"PATH": bin}), record, &stderr); code != 126 {
		t.Errorf("code %d", code)
	}
	if gotPath != real || !slices.Equal(gotArgv, append([]string{real}, args[1:]...)) {
		t.Errorf("exec %s %v", gotPath, gotArgv)
	}
	b, _ := os.ReadFile(cfg)
	for _, m := range mounts(t, b) {
		if m["destination"] == "/sys" && !slices.Contains(options(m), "ro") {
			t.Errorf("/sys still writable: %v", m)
		}
	}
	if q := quota(t, b); q == nil || *q != (cpuMax{400000, 100000}) {
		t.Errorf("CPU quota = %v, want the sandbox's", q)
	}
	if st, _ := os.Stat(cfg); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(bundleDir, "*.apo-runc")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}

	// Not a create: the spec is not read, the call goes through.
	if err := os.WriteFile(cfg, []byte(privilegedSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	run([]string{"apo-runc", "start", "c1"}, env(map[string]string{"PATH": bin}), record, &stderr)
	if b, _ := os.ReadFile(cfg); string(b) != privilegedSpec || !slices.Equal(gotArgv, []string{real, "start", "c1"}) {
		t.Errorf("start: argv %v, spec rewritten %v", gotArgv, string(b) != privilegedSpec)
	}

	// A spec it cannot read still reaches runc, which says what is wrong.
	stderr.Reset()
	run([]string{"apo-runc", "run", "-b", t.TempDir(), "c1"}, env(map[string]string{"PATH": bin}), record, &stderr)
	if !strings.Contains(stderr.String(), "left as it is") || gotArgv[1] != "run" {
		t.Errorf("unreadable: %q, %v", stderr.String(), gotArgv)
	}

	// No runc to exec.
	stderr.Reset()
	if code := run([]string{"apo-runc", "start", "c1"}, env(map[string]string{"PATH": t.TempDir()}), record, &stderr); code != 127 || !strings.Contains(stderr.String(), "no runc on PATH") {
		t.Errorf("no runc: %d %q", code, stderr.String())
	}
}

// A spec in a directory it cannot write is left as it is.
func TestRewriteFileFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(privilegedSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := rewriteFile(cfg, nil); err == nil {
		t.Error("wrote into a read-only directory")
	}
	if b, _ := os.ReadFile(cfg); string(b) != privilegedSpec {
		t.Error("the spec changed")
	}
	if err := rewriteFile(filepath.Join(dir, "missing.json"), nil); err == nil {
		t.Error("a missing spec")
	}
}

// fakeCgroups points sandboxCPU at a cgroup tree of the test's: self is
// /proc/self/cgroup's content, files the tree's (path → content).
func fakeCgroups(t *testing.T, self string, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	selfPath := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(selfPath, []byte(self), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldSelf := cgroupRoot, selfCgroup
	cgroupRoot, selfCgroup = root, selfPath
	t.Cleanup(func() { cgroupRoot, selfCgroup = oldRoot, oldSelf })
}

// quota is a spec's linux.resources.cpu quota and period; nil without one.
func quota(t *testing.T, spec []byte) *cpuMax {
	t.Helper()
	var s struct {
		Linux struct {
			Resources struct {
				CPU struct {
					Quota  *int64 `json:"quota"`
					Period *int64 `json:"period"`
				} `json:"cpu"`
			} `json:"resources"`
		} `json:"linux"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		t.Fatal(err)
	}
	c := s.Linux.Resources.CPU
	if c.Quota == nil || c.Period == nil {
		return nil
	}
	return &cpuMax{*c.Quota, *c.Period}
}

// A container with no CPU quota gets the sandbox's, beside whatever else
// its resources say; one with a quota of its own keeps it.
func TestRewriteCPU(t *testing.T) {
	limit := &cpuMax{200000, 100000}
	for name, spec := range map[string]string{
		"no linux":             `{"ociVersion":"1.2.0"}`,
		"no resources":         `{"linux":{"namespaces":[{"type":"network"}]}}`,
		"null resources":       `{"linux":{"resources":null}}`,
		"no cpu":               `{"linux":{"resources":{"memory":{"limit":1048576}}}}`,
		"shares alone":         `{"linux":{"resources":{"cpu":{"shares":1024}}}}`,
		"a period, no quota":   `{"linux":{"resources":{"cpu":{"period":50000}}}}`,
		"quota zero":           `{"linux":{"resources":{"cpu":{"quota":0,"period":100000}}}}`,
		"quota -1 (unlimited)": `{"linux":{"resources":{"cpu":{"quota":-1}}}}`,
		"quota null":           `{"linux":{"resources":{"cpu":{"quota":null}}}}`,
	} {
		out, changed, err := rewrite([]byte(spec), limit)
		if err != nil || !changed {
			t.Errorf("%s: changed %v, %v", name, changed, err)
			continue
		}
		if q := quota(t, out); q == nil || *q != *limit {
			t.Errorf("%s: quota %v in %s", name, q, out)
		}
	}
	// What was beside the quota stays.
	out, _, _ := rewrite([]byte(`{"linux":{"resources":{"memory":{"limit":1048576},"cpu":{"shares":1024,"cpus":"0-1"}},"namespaces":[{"type":"network"}]}}`), limit)
	want := `{"linux":{"namespaces":[{"type":"network"}],"resources":{"cpu":{"cpus":"0-1","period":100000,"quota":200000,"shares":1024},"memory":{"limit":1048576}}}}`
	if string(out) != want {
		t.Errorf("kept:\n%s\nwant\n%s", out, want)
	}
	// Both changes in one spec.
	out, changed, err := rewrite([]byte(privilegedSpec), limit)
	if err != nil || !changed || quota(t, out) == nil {
		t.Fatalf("privileged: %v %v %s", changed, err, out)
	}
	for _, m := range mounts(t, out) {
		if m["destination"] == "/sys" && !slices.Contains(options(m), "ro") {
			t.Errorf("/sys still writable: %v", m)
		}
	}
	// A quota of its own, even wider than the sandbox's, is kept; and with
	// no limit known nothing changes.
	for _, c := range []struct {
		spec  string
		limit *cpuMax
	}{
		{`{"linux":{"resources":{"cpu":{"quota":50000,"period":100000}}}}`, limit},
		{`{"linux":{"resources":{"cpu":{"quota":800000,"period":100000}}}}`, limit},
		{`{"linux":{}}`, nil},
	} {
		if out, changed, err := rewrite([]byte(c.spec), c.limit); err != nil || changed || string(out) != c.spec {
			t.Errorf("%s: changed %v, %v, %s", c.spec, changed, err, out)
		}
	}
	for name, spec := range map[string]string{
		"linux not an object":     `{"linux":[]}`,
		"resources not an object": `{"linux":{"resources":1}}`,
		"cpu not an object":       `{"linux":{"resources":{"cpu":"2"}}}`,
		"quota not a number":      `{"linux":{"resources":{"cpu":{"quota":"max"}}}}`,
	} {
		if out, changed, err := rewrite([]byte(spec), limit); err == nil || changed || string(out) != spec {
			t.Errorf("%s: changed %v, err %v", name, changed, err)
		}
	}
}

// sandboxCPU walks from the process's cgroup to its namespace's root and
// keeps the tightest limit; none, or a tree it cannot read, is nil.
func TestSandboxCPU(t *testing.T) {
	for _, c := range []struct {
		name  string
		self  string
		files map[string]string
		want  *cpuMax
	}{
		{"the sandbox's, at the root", "0::/init\n", map[string]string{"cpu.max": "200000 100000", "init/cpu.max": "max 100000"}, &cpuMax{200000, 100000}},
		{"the tightest on the way", "0::/a/b\n", map[string]string{"cpu.max": "400000 100000", "a/cpu.max": "75000 50000", "a/b/cpu.max": "300000 100000"}, &cpuMax{75000, 50000}},
		{"at the root itself", "0::/\n", map[string]string{"cpu.max": "150000 100000"}, &cpuMax{150000, 100000}},
		{"cgroup v1 lines first", "12:cpu,cpuacct:/x\n0::/init\n", map[string]string{"cpu.max": "100000 100000"}, &cpuMax{100000, 100000}},
		{"no limit anywhere", "0::/init\n", map[string]string{"cpu.max": "max 100000", "init/cpu.max": "max 100000"}, nil},
		{"no cpu.max files", "0::/init\n", map[string]string{"init/cgroup.procs": ""}, nil},
		{"unreadable values", "0::/\n", map[string]string{"cpu.max": "lots 100000"}, nil},
		{"one field", "0::/\n", map[string]string{"cpu.max": "200000"}, nil},
		{"a zero period", "0::/\n", map[string]string{"cpu.max": "200000 0"}, nil},
		{"outside the namespace", "0::/../..\n", map[string]string{"cpu.max": "200000 100000"}, nil},
		{"cgroup v1 only", "12:cpu,cpuacct:/x\n", map[string]string{"cpu.max": "200000 100000"}, nil},
	} {
		fakeCgroups(t, c.self, c.files)
		got := sandboxCPU()
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	selfCgroup = filepath.Join(t.TempDir(), "missing")
	if got := sandboxCPU(); got != nil {
		t.Errorf("no /proc/self/cgroup: %v", got)
	}
}
