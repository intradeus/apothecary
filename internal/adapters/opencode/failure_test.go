package opencode

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
	envp, id := envWith(t, nil)
	specs := []*agentspec.Spec{{Name: "planner", Prompt: "p"}}
	err := New(failWrites{envp, "planner.md"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write agent config planner") {
		t.Errorf("agent = %v", err)
	}
	err = New(failWrites{envp, "opencode.json"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write opencode.json") {
		t.Errorf("config = %v", err)
	}
}

func TestAuthFileSeeding(t *testing.T) {
	ctx := context.Background()
	envp, id := envWith(t, nil)
	a := New(envp, Config{})
	p := a.AuthFilePath(ctx, id)
	if !strings.HasSuffix(p, "/auth.json") {
		t.Fatalf("path = %q", p)
	}
	if err := a.SeedAuthJSON(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := envp.Env(id).Files[p]; ok {
		t.Error("an empty credential was written")
	}
	if err := a.SeedAuthJSON(ctx, id, []byte(`{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	if string(envp.Env(id).Files[p]) != `{"k":1}` {
		t.Errorf("files = %v", envp.Env(id).Files)
	}
	err := New(failWrites{envp, "auth.json"}, Config{}).SeedAuthJSON(ctx, id, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "seed auth.json") {
		t.Errorf("err = %v", err)
	}
}
