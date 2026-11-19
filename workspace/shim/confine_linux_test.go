//go:build linux

package main

// confine, against this kernel: a confined process reads what its spec
// lets it read and nothing beside it, writes only its state, runs only the
// programs listed (no shell), cannot widen its rules from inside, and
// cannot signal a process outside them. The confined program is this test
// binary (confineProbe), so the test needs nothing of the host's userland
// but its dynamic loader.

import (
	"debug/elf"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// confineWorld is a spec around the test binary: its loader and libraries
// readable and runnable, two repositories and two state folders.
type confineWorld struct {
	exe                    string
	repo, sibling          string
	state, otherState      string
	base                   confineSpec
	coverDir, coverScratch string
}

func newConfineWorld(t *testing.T) *confineWorld {
	t.Helper()
	if landlockABI() < minLandlockABI {
		t.Skip("this kernel's Landlock is under the ABI sessions need")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w := &confineWorld{exe: exe}
	for _, d := range []*string{&w.repo, &w.sibling, &w.state, &w.otherState} {
		*d, err = os.MkdirTemp(root, "d")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(*d, "f"), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.base = confineSpec{
		Read:    append(libraryDirs(), w.repo),
		Write:   []string{w.state},
		Exec:    append([]string{exe}, interpreter(t, exe)...),
		Devices: []string{"/dev/null"},
	}
	// A covered run writes its counters on exit: let it, or it warns.
	for _, d := range []string{os.Getenv("APOTHECARY_COVERDIR"), os.Getenv("GOCOVERDIR")} {
		if d != "" {
			w.base.Write = append(w.base.Write, d)
		}
	}
	if testing.CoverMode() != "" {
		w.base.Write = append(w.base.Write, coverScratch())
	}
	return w
}

// libraryDirs are where a dynamic test binary's libraries live.
func libraryDirs() []string {
	var out []string
	for _, d := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64", "/etc/ld.so.cache"} {
		if p, err := filepath.EvalSymlinks(d); err == nil && !containsPath(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func containsPath(list []string, p string) bool {
	for _, q := range list {
		if q == p {
			return true
		}
	}
	return false
}

// interpreter is the binary's ELF interpreter, which execve opens for
// execution too; none for a static binary.
func interpreter(t *testing.T, exe string) []string {
	t.Helper()
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			data := make([]byte, p.Filesz)
			if _, err := p.ReadAt(data, 0); err != nil {
				t.Fatal(err)
			}
			path, err := filepath.EvalSymlinks(strings.TrimRight(string(data), "\x00"))
			if err != nil {
				t.Fatal(err)
			}
			return []string{path}
		}
	}
	return nil
}

// run confines spec and runs the probe op under it.
func (w *confineWorld) run(t *testing.T, spec confineSpec, op string) shimResult {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "confine", "--spec", string(raw), "--", w.exe)
	cmd.Env = append(cmd.Env, confineProbeEnv+"="+op)
	return runShim(t, "", cmd)
}

func (w *confineWorld) want(t *testing.T, op, want string) {
	t.Helper()
	r := w.run(t, w.base, op)
	if r.code != 0 || !strings.HasPrefix(strings.TrimSpace(r.stdout), want) {
		t.Errorf("%s = %q (exit %d, stderr %q), want %s", op, r.stdout, r.code, r.stderr, want)
	}
}

func TestConfineReadsItsOwnAndNothingBeside(t *testing.T) {
	w := newConfineWorld(t)
	w.want(t, "read:"+filepath.Join(w.repo, "f"), "ok")
	w.want(t, "list:"+w.repo, "ok")
	w.want(t, "read:"+filepath.Join(w.sibling, "f"), "denied")
	w.want(t, "list:"+filepath.Dir(w.repo), "denied")
	w.want(t, "read:"+filepath.Join(w.otherState, "f"), "denied")
	w.want(t, "read:/etc/hostname", "denied")
	// Reading is not writing.
	w.want(t, "write:"+filepath.Join(w.repo, "new"), "denied")
	w.want(t, "write:"+filepath.Join(w.repo, "f"), "denied")
}

func TestConfineWritesOnlyItsState(t *testing.T) {
	w := newConfineWorld(t)
	w.want(t, "write:"+filepath.Join(w.state, "new"), "ok")
	w.want(t, "read:"+filepath.Join(w.state, "f"), "ok")
	w.want(t, "write:"+filepath.Join(w.otherState, "new"), "denied")
	w.want(t, "write:"+filepath.Join(os.TempDir(), "escaped"), "denied")
}

func TestConfineRunsNoShell(t *testing.T) {
	w := newConfineWorld(t)
	for _, sh := range []string{"/bin/sh", "/usr/bin/sh", "/bin/bash"} {
		if _, err := os.Stat(sh); err == nil {
			w.want(t, "exec:"+sh, "denied")
		}
	}
}

func TestConfineCannotWidenFromInside(t *testing.T) {
	w := newConfineWorld(t)
	wider := w.base
	wider.Read = append(append([]string{}, wider.Read...), w.sibling)
	raw, err := json.Marshal(wider)
	if err != nil {
		t.Fatal(err)
	}
	r := w.run(t, w.base, "confine:"+string(raw)+"\x1fread:"+filepath.Join(w.sibling, "f"))
	if !strings.Contains(r.stdout, "denied") {
		t.Errorf("a nested, wider confine read the sibling: %q %q", r.stdout, r.stderr)
	}
}

func TestConfineCannotSignalOutside(t *testing.T) {
	w := newConfineWorld(t)
	w.want(t, "kill:"+strconv.Itoa(os.Getpid()), "denied")
}

func TestConfineRefusesABadSpec(t *testing.T) {
	w := newConfineWorld(t)
	for name, tc := range map[string]struct {
		args []string
		code int
		msg  string
	}{
		"no spec":            {[]string{"--", w.exe}, 2, "--spec is required"},
		"no program":         {[]string{"--spec", "{}", "--"}, 2, "no program"},
		"unknown field":      {[]string{"--spec", `{"net":[]}`, "--", w.exe}, 2, "unknown field"},
		"relative path":      {[]string{"--spec", `{"read":["etc"]}`, "--", w.exe}, 2, "clean absolute"},
		"unclean path":       {[]string{"--spec", `{"read":["/etc/../root"]}`, "--", w.exe}, 2, "clean absolute"},
		"relative program":   {[]string{"--spec", `{"exec":["sh"]}`, "--", "sh"}, 2, "absolute path"},
		"program not listed": {[]string{"--spec", `{}`, "--", w.exe}, 2, "not on the spec's exec list"},
		"a directory to run": {[]string{"--spec", `{"exec":["/usr/bin","` + w.exe + `"]}`, "--", w.exe}, exitNoConfine, "is a directory"},
		"a missing path":     {[]string{"--spec", `{"read":["/no/such/path"],"exec":["` + w.exe + `"]}`, "--", w.exe}, exitNoConfine, "no such file"},
		"stray argument":     {[]string{"--wat"}, 2, "unexpected argument"},
		"nothing":            {nil, 2, "usage"},
	} {
		r := shim(t, "", append([]string{"confine"}, tc.args...)...)
		if r.code != tc.code || !strings.Contains(r.stderr, tc.msg) {
			t.Errorf("%s: exit %d, stderr %q; want %d, %q", name, r.code, r.stderr, tc.code, tc.msg)
		}
	}
}

func TestConfineCheck(t *testing.T) {
	r := shim(t, "", "confine", "--check")
	if landlockABI() < minLandlockABI {
		if r.code != exitNoConfine {
			t.Errorf("--check without Landlock = %+v", r)
		}
		return
	}
	if r.code != 0 || !strings.HasPrefix(r.stdout, "landlock: abi ") {
		t.Errorf("--check = %+v", r)
	}
}

func TestHandledRightsFollowTheABI(t *testing.T) {
	if handledFS(1)&(llRefer|llTruncate|llIoctlDev) != 0 {
		t.Error("ABI 1 handles a right it does not know")
	}
	if handledFS(3)&llTruncate == 0 || handledFS(3)&llIoctlDev != 0 {
		t.Error("ABI 3 truncation, not device ioctls")
	}
	if handledFS(5)&llIoctlDev == 0 || handledFS(8)&llResolveUnix != 0 || handledFS(9)&llResolveUnix == 0 {
		t.Error("ABI 5 handles device ioctls, 9 unix sockets")
	}
	for _, r := range []uint64{rightsRead, rightsWrite, rightsExec, rightsDevices} {
		if r&llResolveUnix != 0 {
			t.Error("a rule grants connecting to a unix socket")
		}
	}
	// A shell is never granted by a read or write rule.
	if rightsRead&llExecute != 0 || rightsWrite&llExecute != 0 || rightsDevices&llExecute != 0 {
		t.Error("only the exec list runs programs")
	}
	if !errors.Is(errNoLandlock, errNoLandlock) || !strings.Contains(errNoLandlock.Error(), "Landlock") {
		t.Error("the refusal names Landlock")
	}
}

// A confined process reaches no unix socket outside its rules: the agent
// host's sits in the same container, as the same user (ABI 9; below it the
// agent host's token is what holds).
func TestConfineReachesNoSocket(t *testing.T) {
	w := newConfineWorld(t)
	if landlockABI() < 9 {
		t.Skip("unix socket connects are Landlock's from ABI 9")
	}
	sock := filepath.Join(w.sibling, "host.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	w.want(t, "dial:"+sock, "denied")
}
