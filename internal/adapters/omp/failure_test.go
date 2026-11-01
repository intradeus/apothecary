package omp

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/envport"
)

// failWrites fails the writes whose path contains match.
type failWrites struct {
	*fake.EnvProvisioner
	match string
}

func (f failWrites) WriteFile(ctx context.Context, id envport.EnvID, path string, data []byte, mode fs.FileMode) error {
	if strings.Contains(path, f.match) {
		return errors.New("disk full")
	}
	return f.EnvProvisioner.WriteFile(ctx, id, path, data, mode)
}

func TestPrepareEnvFailures(t *testing.T) {
	ctx := context.Background()
	envp, id := envWith(t)
	specs := []*agentspec.Spec{{Name: "planner", Prompt: "p"}}
	err := New(failWrites{envp, ".role"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write role planner") {
		t.Errorf("role = %v", err)
	}
	err = New(failWrites{envp, "config.yml"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write config.yml") {
		t.Errorf("config = %v", err)
	}
}

// The store holding the logins is what the end-of-run scrub removes.
func TestAuthFilePathIsTheAgentDB(t *testing.T) {
	envp, id := envWith(t)
	if p := New(envp, Config{}).AuthFilePath(context.Background(), id); !strings.HasSuffix(p, "/agent.db") {
		t.Errorf("path = %q", p)
	}
}
