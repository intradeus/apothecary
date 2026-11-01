package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	ranFor, cfgChanged := runRegistry("registry", cfg, &mod)
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
		_, cfgChanged := runRegistry("registry", cfg, &mod)
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

// The supervisor restarts its child on any mtime bump, so an unchanged config
// must not be rewritten.
func TestWriteIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := writeIfChanged(path, "a"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := writeIfChanged(path, "a"); err != nil {
		t.Fatal(err)
	}
	if mt, _ := mtime(path); !mt.Equal(old) {
		t.Fatalf("an unchanged write moved the mtime to %s", mt)
	}
	if err := writeIfChanged(path, "b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "b" {
		t.Fatalf("content = %q", got)
	}
	if mt, _ := mtime(path); !mt.After(old) {
		t.Fatal("a changed write must bump the mtime")
	}
	if err := writeIfChanged(filepath.Join(path, "child"), "x"); err == nil {
		t.Fatal("an unwritable path must be an error")
	}
}

// A proxy-mode registry refuses pushes, and pushing is the build cache's job.
func TestBuildCacheConfigIsWritableAndRooted(t *testing.T) {
	cfg := buildCacheRegistryConfig("/var/lib/buildcache")
	if strings.Contains(cfg, "proxy:") {
		t.Errorf("the build cache must not be a proxy:\n%s", cfg)
	}
	for _, want := range []string{"rootdirectory: /var/lib/buildcache\n", "addr: :5001\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if !strings.Contains(mirrorRegistryConfig, "proxy:") || !strings.Contains(mirrorRegistryConfig, "addr: :5000") {
		t.Errorf("mirror config:\n%s", mirrorRegistryConfig)
	}
}

func TestRunRegistryReportsAStartFailure(t *testing.T) {
	old := registryBin
	registryBin = filepath.Join(t.TempDir(), "no-such-binary")
	defer func() { registryBin = old }()
	var mod time.Time
	if ranFor, changed := runRegistry("registry", "/nope", &mod); ranFor != 0 || changed {
		t.Fatalf("runRegistry = %s, %v", ranFor, changed)
	}
}

func TestRelayLinesPrefixesChildOutput(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	relayLines("mirror", strings.NewReader("one\ntwo\n"))
	got := buf.String()
	if !strings.Contains(got, "mirror: one") || !strings.Contains(got, "mirror: two") {
		t.Fatalf("log = %q", got)
	}
}
