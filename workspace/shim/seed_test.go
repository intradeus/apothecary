package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedCopiesTheBinaryAndMarkers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shim-dir")
	old := sourceHash
	sourceHash = "testhash123"
	t.Cleanup(func() { sourceHash = old })

	if err := seed(dir); err != nil {
		t.Fatal(err)
	}
	// The binary is this very test binary, copied executable.
	self, _ := os.Executable()
	want, _ := os.ReadFile(self)
	got, err := os.ReadFile(filepath.Join(dir, "shim"))
	if err != nil || len(got) != len(want) {
		t.Fatalf("shim copy: %d bytes, want %d (%v)", len(got), len(want), err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "shim")); info.Mode().Perm()&0o100 == 0 {
		t.Errorf("shim is not executable: %v", info.Mode())
	}
	// The apothecary-shim alias is a symlink to "shim".
	if target, err := os.Readlink(filepath.Join(dir, "apothecary-shim")); err != nil || target != "shim" {
		t.Errorf("alias → %q, %v", target, err)
	}
	// shim.hash carries the source hash.
	if h, _ := os.ReadFile(filepath.Join(dir, "shim.hash")); string(h) != "testhash123\n" {
		t.Errorf("shim.hash = %q", h)
	}

	// Idempotent: a second seed over the same dir (alias already right) works.
	if err := seed(dir); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	// A stale alias pointing elsewhere is replaced.
	alias := filepath.Join(dir, "apothecary-shim")
	_ = os.Remove(alias)
	if err := os.Symlink("wrong-target", alias); err != nil {
		t.Fatal(err)
	}
	if err := seed(dir); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(alias); target != "shim" {
		t.Errorf("stale alias not fixed: %q", target)
	}
}

func TestSeedFailsOnAnUnwritableDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seed(filepath.Join(blocker, "sub")); err == nil {
		t.Error("seed into a path under a file must fail")
	}
}

// The init container's whole job, as it runs: a binary that runs, under both
// names, with the hash the engine checks.
func TestRunSeed(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "shim")
	if r := shim(t, "", "seed", dir); r.code != 0 || r.stdout != "" {
		t.Fatalf("seed = %+v", r)
	}
	if h, _ := os.ReadFile(filepath.Join(dir, "shim.hash")); string(h) != sourceHash+"\n" {
		t.Errorf("shim.hash = %q", h)
	}
	for _, name := range []string{"shim", "apothecary-shim"} {
		cmd := shimCmd(t, "version")
		cmd.Path = filepath.Join(dir, name)
		if r := runShim(t, "", cmd); r.code != 0 || r.stdout != sourceHash+"\n" {
			t.Errorf("%s version = %+v", name, r)
		}
	}
}

func TestRunSeedRefuses(t *testing.T) {
	if r := shim(t, "", "seed"); r.code != 2 || !strings.Contains(r.stderr, "usage: shim seed <dir>") {
		t.Errorf("no dir = %+v", r)
	}
	if r := shim(t, "", "seed", "a", "b"); r.code != 2 {
		t.Errorf("two dirs = %+v", r)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := shim(t, "", "seed", filepath.Join(file, "sub")); r.code != 1 || !strings.HasPrefix(r.stderr, "seed: ") {
		t.Errorf("under a file = %+v", r)
	}
}

// Whatever stands in the way of one of the files fails the seed, rather than
// leaving a shim dir half done and looking fine.
func TestSeedFailsOnWhatBlocksItsFiles(t *testing.T) {
	for _, name := range []string{"shim.tmp", "shim", "apothecary-shim", "shim.hash"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, name, "occupied"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := seed(dir); err == nil {
			t.Errorf("seed past a directory at %s succeeded", name)
		}
	}
}
