package wshome

import (
	"context"
	"errors"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		code int
		want string
	}{
		{"reports the workspace user's home", "/root\n", nil, 0, "/root"},
		{"trims surrounding whitespace", "  /home/vscode  ", nil, 0, "/home/vscode"},
		// Anything unexpected keeps the pipeline-image behavior rather than
		// writing config to a path the harness will never read.
		{"probe error falls back", "/root", errors.New("boom"), 0, "/fallback"},
		{"nonzero exit falls back", "/root", nil, 1, "/fallback"},
		{"empty output falls back", "", nil, 0, "/fallback"},
		{"relative path falls back", "relative/home", nil, 0, "/fallback"},
		{"whitespace-bearing path falls back", "/home/a b", nil, 0, "/fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envp := fake.NewEnvProvisioner()
			id, err := envp.Provision(context.Background(), envport.EnvSpec{RunID: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			envp.ExecFunc = func(*fake.Env, envport.Command) (envport.ExecResult, error) {
				return envport.ExecResult{Output: tc.out, ExitCode: tc.code}, tc.err
			}
			if got := Resolve(context.Background(), envp, id, "/fallback"); got != tc.want {
				t.Errorf("Resolve = %q, want %q", got, tc.want)
			}
		})
	}
}
