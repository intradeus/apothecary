package main

import (
	"os"
	"path/filepath"
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
