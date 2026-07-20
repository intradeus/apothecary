package kinds

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"factory/internal/adapters/fake"
	"factory/internal/ports"
)

func TestRegistryValidAndDescriptors(t *testing.T) {
	r := NewRegistry()
	r.RegisterForge(GitLab{})
	r.RegisterProvisioner(Coder{})
	r.RegisterHarness(Opencode{})
	for _, pk := range ProviderKinds() {
		r.RegisterProvider(pk)
	}
	for _, ok := range [][2]string{
		{"forge", "gitlab"}, {"provisioner", "coder"}, {"harness", "opencode"},
		{"provider", "anthropic"}, {"provider", "openai"}, {"provider", "openrouter"},
	} {
		if !r.Valid(ok[0], ok[1]) {
			t.Errorf("%s/%s not valid", ok[0], ok[1])
		}
	}
	if r.Valid("forge", "github") || r.Valid("harness", "codex") {
		t.Error("unregistered kinds reported valid (they arrive in M10)")
	}
	ds := r.Descriptors()
	if len(ds) != 6 {
		t.Fatalf("descriptors = %d, want 6", len(ds))
	}
	// Secret fields are flagged so the UI renders write-only inputs.
	for _, d := range ds {
		if d.Kind == "gitlab" {
			found := false
			for _, f := range d.Fields {
				if f.Name == "token" && f.Secret {
					found = true
				}
			}
			if !found {
				t.Error("gitlab token field not marked secret")
			}
		}
	}
}

func TestOpencodeHarnessCapabilities(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Opencode{}.New(Config{
		Secrets: map[string]string{"auth_json": `{"opencode":{"type":"oauth"}}`},
	}, envp)
	if err != nil {
		t.Fatal(err)
	}
	if h.Binary() != "opencode" {
		t.Errorf("binary = %q", h.Binary())
	}
	layer := strings.Join(h.InstallLayer(), "\n")
	if !strings.Contains(layer, "npm install -g opencode-ai@"+OpencodeVersion) {
		t.Errorf("install layer = %q", layer)
	}

	// SeedAuth composes the stored auth_json with openrouter provider creds
	// in opencode's native shape.
	err = h.SeedAuth(context.Background(), id, []ProviderCreds{
		{Kind: "openrouter", Secrets: map[string]string{"api_key": "sk-or-123"}},
		{Kind: "anthropic", Secrets: map[string]string{"api_key": "sk-ant"}}, // env-var provider: no auth.json entry
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := envp.ReadFile(context.Background(), id, "/home/factory/.local/share/opencode/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]map[string]any
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	if auth["openrouter"]["key"] != "sk-or-123" || auth["openrouter"]["type"] != "api" {
		t.Errorf("auth.json = %s", data)
	}
	if _, ok := auth["opencode"]; !ok {
		t.Error("stored auth_json base lost")
	}
	if _, ok := auth["anthropic"]; ok {
		t.Error("env-var providers must not land in auth.json")
	}

	// Bad auth_json fails Test statically.
	bad, _ := Opencode{}.New(Config{Secrets: map[string]string{"auth_json": "{nope"}}, envp)
	if err := bad.Test(context.Background()); err == nil {
		t.Error("invalid auth_json passed Test")
	}
}

func TestProviderEnvVarsAndDomains(t *testing.T) {
	for _, pk := range ProviderKinds() {
		p, err := pk.New(Config{Secrets: map[string]string{"api_key": "sk-x"}})
		if err != nil {
			t.Fatal(err)
		}
		env := p.EnvVars()
		if len(env) != 1 {
			t.Errorf("%s env = %v", pk.Descriptor().Kind, env)
		}
		for name, v := range env {
			if !strings.HasSuffix(name, "_API_KEY") || v != "sk-x" {
				t.Errorf("%s -> %s=%s", pk.Descriptor().Kind, name, v)
			}
		}
		if len(p.EgressDomains()) == 0 {
			t.Errorf("%s declares no egress domains", pk.Descriptor().Kind)
		}
	}
}

func TestGitLabForgeKind(t *testing.T) {
	gl := GitLab{}
	acc, err := gl.Account(Config{
		Values:  map[string]any{"base_url": "https://gitlab.example.com"},
		Secrets: map[string]string{"token": "glpat-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	user, pass := acc.GitCredential()
	if user != "oauth2" || pass != "glpat-x" {
		t.Errorf("credential = %s:%s", user, pass)
	}
	if d := acc.EgressDomains(); len(d) != 1 || d[0] != "gitlab.example.com" {
		t.Errorf("egress = %v", d)
	}
	if _, err := gl.Forge(Config{}, ports.RepoRef{Locator: "not-a-number"}); err == nil {
		t.Error("non-numeric gitlab locator accepted")
	}
	if _, err := gl.Forge(Config{}, ports.RepoRef{Locator: "42"}); err != nil {
		t.Errorf("numeric locator refused: %v", err)
	}
}
