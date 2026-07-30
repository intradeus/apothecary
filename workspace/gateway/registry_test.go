package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextBackoff(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{registryMinBackoff, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, registryMaxBackoff},
		{registryMaxBackoff, registryMaxBackoff}, // capped, never grows
		{0, registryMinBackoff},                  // never returns 0 (would busy-loop)
	}
	for _, c := range cases {
		if got := nextBackoff(c.in); got != c.want {
			t.Errorf("nextBackoff(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

// A child that exits immediately is a crash loop (report how long it ran so the
// supervisor keeps backing off); one that outlives registryHealthy resets it.
func TestRunRegistryReportsRuntime(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(cfg, []byte("version: 0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := registryBin
	registryBin = "/bin/true"
	defer func() { registryBin = old }()

	mod, _ := mtime(cfg)
	ranFor, cfgChanged := runRegistry(cfg, &mod)
	if cfgChanged {
		t.Error("config did not change; must not report a config restart")
	}
	if ranFor > registryHealthy {
		t.Errorf("instant exit reported as %s of healthy runtime", ranFor)
	}
}

// Rewriting the config must stop the child (the registry has no config
// hot-reload) and report it as a config restart, not a crash.
func TestRunRegistryRestartsOnConfigChange(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(cfg, []byte("version: 0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stand-in that outlives the test and ignores `serve <cfg>`, so only the
	// supervisor can end it.
	fake := filepath.Join(dir, "fake-registry")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := registryBin
	registryBin = fake
	defer func() { registryBin = old }()

	mod, _ := mtime(cfg)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.Chtimes(cfg, time.Now().Add(time.Second), time.Now().Add(time.Second))
	}()
	done := make(chan bool, 1)
	go func() {
		_, cfgChanged := runRegistry(cfg, &mod)
		done <- cfgChanged
	}()
	select {
	case cfgChanged := <-done:
		if !cfgChanged {
			t.Error("config change not reported (child would keep the old config)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("config change did not stop the child")
	}
}
