package resolve

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/confstore"
	"apothecary/internal/kinds"
	"apothecary/internal/ports/envport"
	"apothecary/internal/providers"
	"apothecary/internal/secrets"
)

// The model-providers security rules (docs/model-providers-plan.md), held as
// tests over a real resolution with the real harness kinds:
//
//   - rule 3: no refresh token reaches a workspace, whatever the harness — not
//     in a file it seeds, not in provider.env, not in what the run's redactor
//     is told to expect;
//   - rule 1: a harness integration can only ever reference its owner's
//     credentials, so a resolution holds no one else's.
func TestNoRefreshTokenReachesAWorkspace(t *testing.T) {
	key, _ := secrets.ParseKey(secrets.GenerateKey())
	svc := confstore.New(confstore.NewMem(), key)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	reg.RegisterHarness(kinds.Opencode{})
	reg.RegisterHarness(kinds.Codex{})
	reg.RegisterHarness(kinds.Omp{})
	svc.KindValid = reg.Valid
	ctx := context.Background()

	const refreshMark = "REFRESH-TOKEN-NEVER-IN-A-WORKSPACE"
	cred := func(c providers.Credential) string {
		c.Expires = time.Now().Add(6 * time.Hour)
		pc, err := svc.CreateCredential(ctx, "u1", c, "")
		if err != nil {
			t.Fatal(err)
		}
		return pc.ID
	}
	chatgpt := cred(providers.Credential{Provider: "openai-chatgpt", Kind: providers.KindOAuth,
		Access: "chatgpt-access", Refresh: refreshMark + "-chatgpt",
		Extra: map[string]string{providers.ExtraIDToken: "chatgpt-id-token", providers.ExtraAccountID: "acct"}})
	anthropic := cred(providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Access: "anthropic-access", Refresh: refreshMark + "-anthropic"})
	gemini := cred(providers.Credential{Provider: "google-gemini-cli", Kind: providers.KindOAuth, Access: "gemini-access", Refresh: refreshMark + "-gemini",
		Extra: map[string]string{providers.ExtraProjectID: "proj"}})
	openrouter := cred(providers.Credential{Provider: "openrouter", Kind: providers.KindAPIKey, Key: "sk-or-key"})
	openai := cred(providers.Credential{Provider: "openai", Kind: providers.KindAPIKey, Key: "sk-openai-key"})

	harness := func(kind, name string, ids ...string) string {
		refs := make([]any, len(ids))
		for i, id := range ids {
			refs[i] = id
		}
		in, err := svc.CreateIntegration(ctx, confstore.Integration{UserID: "u1", Port: confstore.PortHarness, Kind: kind, Name: name,
			Config: map[string]any{confstore.ConfigCredentialIDs: refs}})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	mk := func(port, name string) string {
		in, err := svc.CreateIntegration(ctx, confstore.Integration{UserID: "u1", Port: port, Kind: "fake", Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	p, err := svc.CreateProject(ctx, confstore.Project{
		UserID: "u1", Name: "p1",
		RepoAccountID: mk(confstore.PortRepository, "repo"), ProvisionerID: mk(confstore.PortProvisioner, "prov"),
		HarnessIDs: []string{
			harness("opencode", "oc", chatgpt, openrouter),
			harness("codex", "cx", chatgpt, openai),
			harness("omp", "omp", anthropic, gemini, chatgpt),
		},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.example.com/g/p.git", Locator: "1"},
		DefaultFlow: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}

	r := New(svc, reg, "", 8080, "")
	res, err := r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Release()
	env, err := res.Envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	var registered []string
	if err := res.SeedAuth(ctx, env, func(s string) { registered = append(registered, s) }); err != nil {
		t.Fatal(err)
	}

	files := res.Envp.(interface{ Env(envport.EnvID) *fake.Env }).Env(env).Files
	if len(files) < 2 {
		t.Fatalf("seeded only %d files: the harnesses wrote nothing", len(files))
	}
	for path, data := range files {
		if strings.Contains(string(data), refreshMark) {
			t.Errorf("a refresh token reached the workspace in %s: %s", path, data)
		}
	}
	for k, v := range res.ProviderEnv {
		if strings.Contains(v, refreshMark) {
			t.Errorf("a refresh token rides provider.env as %s", k)
		}
	}
	for _, s := range append(append([]string(nil), registered...), res.HarnessSecrets...) {
		if strings.Contains(s, refreshMark) {
			t.Errorf("a refresh token is something the workspace is expected to hold: %q", s)
		}
	}
	// What the workspace does hold, it was told about first.
	for _, want := range []string{"chatgpt-access", "chatgpt-id-token", "sk-or-key"} {
		found := false
		for _, s := range registered {
			found = found || s == want
		}
		if !found {
			t.Errorf("%s was written without being registered (registered %v)", want, registered)
		}
	}
	// omp's logins come from the broker, which hands out the sentinel in
	// place of a refresh token (kinds TestOmpBrokerWire); the resolution
	// offers it, and knows which credentials a refresh must be pushed for.
	if res.AuthBroker == nil {
		t.Error("omp holds logins but the resolution offers no broker")
	}
	for _, id := range []string{chatgpt, anthropic, gemini, openrouter, openai} {
		found := false
		for _, have := range res.CredentialIDs {
			found = found || have == id
		}
		if !found {
			t.Errorf("credential %s is used but a refresh of it would not be pushed", id)
		}
	}

	// Rule 1: another user's credential cannot be referenced at all.
	theirs, err := svc.CreateCredential(ctx, "u2", providers.Credential{Provider: "openrouter", Kind: providers.KindAPIKey, Key: "sk-theirs"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateIntegration(ctx, confstore.Integration{UserID: "u1", Port: confstore.PortHarness, Kind: "opencode", Name: "stolen",
		Config: map[string]any{confstore.ConfigCredentialIDs: []any{theirs.ID}}}); err == nil {
		t.Error("a harness integration referenced another user's credential")
	}
}
