package codex

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
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
	err = New(failWrites{envp, "config.toml"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write config.toml") {
		t.Errorf("config = %v", err)
	}
}

func TestAuthFileSeeding(t *testing.T) {
	ctx := context.Background()
	envp, id := envWith(t)
	a := New(envp, Config{})
	p := a.AuthFilePath(ctx, id)
	if !strings.HasSuffix(p, "/auth.json") {
		t.Fatalf("path = %q", p)
	}
	// Nothing to seed: API-key auth rides provider.env.
	if err := a.SeedAuthJSON(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := envp.Env(id).Files[p]; ok {
		t.Error("an empty credential was written")
	}
	if err := a.SeedAuthJSON(ctx, id, []byte(`{"t":1}`)); err != nil {
		t.Fatal(err)
	}
	if string(envp.Env(id).Files[p]) != `{"t":1}` {
		t.Errorf("files = %v", envp.Env(id).Files)
	}
	err := New(failWrites{envp, "auth.json"}, Config{}).SeedAuthJSON(ctx, id, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "seed auth.json") {
		t.Errorf("err = %v", err)
	}
}

// A read-only session is codex-acp's read-only mode, and no agent means no
// preamble to look up.
func TestProfileReadOnlyAndNoAgent(t *testing.T) {
	envp, id := envWith(t)
	p := New(envp, Config{}).Profile()
	if p.ReadOnlyBy == "" || p.ReadOnlyMode != "read-only" {
		t.Errorf("profile = %+v", p)
	}
	if pre, err := p.Preamble(context.Background(), id, agentport.AgentSpec{}); err != nil || pre != "" {
		t.Errorf("no agent = %q, %v", pre, err)
	}
}
