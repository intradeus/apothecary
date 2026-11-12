package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// sourceHash is the hash of the shim's sources, set at build time
// (-X main.sourceHash=…, see workspace/images/shim/Dockerfile). The engine
// compares it with its own (workspace.ShimSourceHash) before trusting a shim
// it did not build itself.
var sourceHash = "dev"

// runSeed is the Kubernetes init container's job: put this very binary into
// the run's shim dir — the directory the workspace container mounts at
// /opt/apothecary — as a sandbox pod's shim-seed init container needs it: tmp+rename
// (a live relay may be executing the old one), the apothecary-shim alias, and
// shim.hash for the engine's check.
func runSeed(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: shim seed <dir>")
		os.Exit(2)
	}
	if err := seed(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func seed(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "shim.tmp")
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "shim")); err != nil {
		return err
	}
	alias := filepath.Join(dir, "apothecary-shim")
	if target, err := os.Readlink(alias); err != nil || target != "shim" {
		_ = os.Remove(alias)
		if err := os.Symlink("shim", alias); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "shim.hash"), []byte(sourceHash+"\n"), 0o644)
}
