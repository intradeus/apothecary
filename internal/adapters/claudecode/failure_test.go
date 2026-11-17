package claudecode

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

// modes records the mode of every write (the fake's WriteFile records none).
type modes struct {
	*fake.EnvProvisioner
	got map[string]fs.FileMode
}

func (m modes) WriteFile(ctx context.Context, id envport.EnvID, path string, data []byte, mode fs.FileMode) error {
	m.got[path] = mode
	return m.EnvProvisioner.WriteFile(ctx, id, path, data, mode)
}

func TestPrepareEnvFailures(t *testing.T) {
	ctx := context.Background()
	envp, id := envWith(t)
	specs := []*agentspec.Spec{{Name: "planner", Prompt: "p"}}
	err := New(failWrites{envp, ".role"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write role planner") {
		t.Errorf("role = %v", err)
	}
	err = New(failWrites{envp, "settings.json"}, Config{}).PrepareEnv(ctx, id, specs)
	if err == nil || !strings.Contains(err.Error(), "write settings.json") {
		t.Errorf("settings = %v", err)
	}
}

func TestSeedCredentialsFailure(t *testing.T) {
	envp, id := envWith(t)
	err := New(failWrites{envp, ".credentials.json"}, Config{}).SeedCredentials(context.Background(), id, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "seed .credentials.json") {
		t.Errorf("err = %v", err)
	}
}

// The login file holds a live access token: readable by its user alone.
func TestSeedCredentialsIsPrivate(t *testing.T) {
	envp, id := envWith(t)
	ctx := context.Background()
	rec := modes{envp, map[string]fs.FileMode{}}
	a := New(rec, Config{})
	if err := a.SeedCredentials(ctx, id, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if m, ok := rec.got[a.AuthFilePath(ctx, id)]; !ok || m != 0o600 {
		t.Errorf("modes = %v", rec.got)
	}
}
