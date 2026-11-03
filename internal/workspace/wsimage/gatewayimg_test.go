package wsimage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gwFakeDocker answers image inspects from a set, records builds and checks
// the context handed to each. A build can be made to fail.
type gwFakeDocker struct {
	have      map[string]bool
	builds    []string
	ctx       []string
	buildExit int
	buildErr  error
}

func (f *gwFakeDocker) Run(_ context.Context, dir string, args ...string) (string, int, error) {
	switch args[0] {
	case "image":
		if f.have[args[len(args)-1]] {
			return "[]", 0, nil
		}
		return "", 1, nil
	case "build":
		f.builds = append(f.builds, args[2])
		for _, p := range []string{"Dockerfile", "gateway/main.go", "gateway/policy.go"} {
			if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
				f.ctx = append(f.ctx, p)
			}
		}
		if f.buildErr != nil || f.buildExit != 0 {
			return "build failed: boom", f.buildExit, f.buildErr
		}
		f.have[args[2]] = true
	}
	return "", 0, nil
}

func TestEnsureGatewayImageBuildsOnce(t *testing.T) {
	f := &gwFakeDocker{have: map[string]bool{}}
	runs := t.TempDir()
	b := NewWithRunner(f, runs)
	tag, err := b.EnsureGatewayImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tag, "apothecary/egress-gw:") {
		t.Errorf("tag = %q", tag)
	}
	if len(f.builds) != 1 || strings.Join(f.ctx, " ") != "Dockerfile gateway/main.go gateway/policy.go" {
		t.Fatalf("builds = %v, context = %v (the gateway source must be in the context)", f.builds, f.ctx)
	}
	// The build log is persisted for the operator.
	name := strings.ReplaceAll(strings.ReplaceAll(tag, "/", "-"), ":", "-")
	if _, err := os.Stat(filepath.Join(runs, "builds", name+".log")); err != nil {
		t.Errorf("build log not written: %v", err)
	}
	// Second call: a cache hit, no second build.
	tag2, err := b.EnsureGatewayImage(context.Background())
	if err != nil || tag2 != tag || len(f.builds) != 1 {
		t.Errorf("cache hit: tag=%q builds=%v err=%v", tag2, f.builds, err)
	}
}

func TestGatewayHashIsStableAndDistinct(t *testing.T) {
	if gatewayHash() != gatewayHash() {
		t.Error("hash is not stable")
	}
	if len(gatewayHash()) != 16 {
		t.Errorf("hash length = %d", len(gatewayHash()))
	}
	if gatewayHash() == shimHash() {
		t.Error("the gateway hash must differ from the shim's")
	}
}

func TestEnsureGatewayImageBuildFailures(t *testing.T) {
	t.Run("non-zero exit", func(t *testing.T) {
		f := &gwFakeDocker{have: map[string]bool{}, buildExit: 1}
		if _, err := NewWithRunner(f, t.TempDir()).EnsureGatewayImage(context.Background()); err == nil || !strings.Contains(err.Error(), "exit 1") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("runner error", func(t *testing.T) {
		f := &gwFakeDocker{have: map[string]bool{}, buildErr: errors.New("docker down")}
		if _, err := NewWithRunner(f, t.TempDir()).EnsureGatewayImage(context.Background()); err == nil || !strings.Contains(err.Error(), "docker down") {
			t.Fatalf("err = %v", err)
		}
	})
}
