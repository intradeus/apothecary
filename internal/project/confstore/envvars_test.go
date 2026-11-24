package confstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestProjectWorkspaceEnv(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)

	p.Workspace.Env = map[string]string{
		"JEST_MAX_WORKERS": "2",
		" NX_PARALLEL ":    "${APO_CPUS}",
		"_lower_ok":        "",
		"MULTI":            "a b\nc",
	}
	got, err := svc.UpdateProject(ctx, p.ID, p)
	if err != nil {
		t.Fatalf("valid env refused: %v", err)
	}
	want := map[string]string{"JEST_MAX_WORKERS": "2", "NX_PARALLEL": "${APO_CPUS}", "_lower_ok": "", "MULTI": "a b\nc"}
	if fmt.Sprint(got.Workspace.Env) != fmt.Sprint(want) {
		t.Errorf("saved env = %v, want %v (names trimmed, values as written)", got.Workspace.Env, want)
	}
	back, err := svc.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(back.Workspace.Env) != fmt.Sprint(want) {
		t.Errorf("read back env = %v, want %v", back.Workspace.Env, want)
	}

	p.Workspace.Env = map[string]string{}
	if got, err := svc.UpdateProject(ctx, p.ID, p); err != nil || got.Workspace.Env != nil {
		t.Errorf("an emptied env = %v, %v; want none stored", got.Workspace.Env, err)
	}

	many := map[string]string{}
	for i := range MaxEnvVars + 1 {
		many[fmt.Sprintf("V%d", i)] = "x"
	}
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"empty name", map[string]string{" ": "x"}, "not a variable name"},
		{"leading digit", map[string]string{"1X": "x"}, "not a variable name"},
		{"dash", map[string]string{"MY-VAR": "x"}, "not a variable name"},
		{"engine prefix", map[string]string{"APOTHECARY_RUN_ID": "x"}, "the engine's own"},
		{"hint prefix", map[string]string{"APO_CPUS": "8"}, "the engine's own"},
		{"hint prefix, any case", map[string]string{"apo_cpus": "8"}, "the engine's own"},
		{"PATH", map[string]string{"PATH": "/bin"}, "set by the engine"},
		{"HOME", map[string]string{"HOME": "/root"}, "set by the engine"},
		{"provider key", map[string]string{"ANTHROPIC_API_KEY": "k"}, "set by the engine"},
		{"omp provider key", map[string]string{"GROQ_API_KEY": "k"}, "set by the engine"},
		{"broker", map[string]string{"OMP_AUTH_BROKER_TOKEN": "t"}, "set by the engine"},
		{"too many", many, fmt.Sprintf("at most %d variables", MaxEnvVars)},
		{"value too long", map[string]string{"BIG": strings.Repeat("x", MaxEnvValueLen+1)}, "at most 4096 bytes"},
		{"NUL", map[string]string{"N": "a\x00b"}, "NUL"},
		{"twice once trimmed", map[string]string{"A": "1", " A": "2"}, "set twice"},
	} {
		p.Workspace.Env = c.env
		if _, err := svc.UpdateProject(ctx, p.ID, p); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}

	p.Workspace.Env = map[string]string{"BIG": strings.Repeat("x", MaxEnvValueLen)}
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Errorf("a value at the bound refused: %v", err)
	}
}
