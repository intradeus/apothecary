package confstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/providers"
)

func TestCredentialLifecycle(t *testing.T) {
	svc, mem := newSvc(t)
	ctx := context.Background()

	if _, err := svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "bespoke", Kind: providers.KindAPIKey, Key: "k"}, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "openai-chatgpt", Kind: providers.KindAPIKey, Key: "k"}, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("a key for a login-only provider: %v", err)
	}
	if _, err := svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "openrouter", Kind: providers.KindOAuth, Access: "a", Refresh: "r"}, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("a login for a key-only provider: %v", err)
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	login := providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Access: "acc-1", Refresh: "ref-1", Expires: exp}
	login.Set(providers.ExtraEmail, "me@example.com")
	pc, err := svc.CreateCredential(ctx, "u1", login, "")
	if err != nil {
		t.Fatal(err)
	}
	if pc.Name != "Anthropic · me@example.com" || pc.Meta.Account != "me@example.com" || !pc.Meta.ExpiresAt.Equal(exp) || pc.Meta.Status != CredentialOK || pc.Sealed != nil {
		t.Errorf("created = %+v", pc)
	}
	stored, _ := mem.GetCredential(ctx, pc.ID)
	if bytes.Contains(stored.Sealed, []byte("ref-1")) {
		t.Error("the refresh token is stored in clear")
	}
	c, _, err := svc.OpenCredential(ctx, pc.ID)
	if err != nil || c.Access != "acc-1" || c.Refresh != "ref-1" || !c.Expires.Equal(exp) || c.Label() != "me@example.com" {
		t.Fatalf("opened = %+v, %v", c, err)
	}

	c.Access, c.Refresh, c.Expires = "acc-2", "ref-2", exp.Add(time.Hour)
	if _, err := svc.SaveRefreshed(ctx, c); err != nil {
		t.Fatal(err)
	}
	c2, meta, _ := svc.OpenCredential(ctx, pc.ID)
	if c2.Refresh != "ref-2" || !meta.Meta.ExpiresAt.Equal(exp.Add(time.Hour)) || meta.Meta.LastRefresh.IsZero() {
		t.Errorf("after refresh = %+v %+v", c2, meta.Meta)
	}
	if err := svc.DisableCredential(ctx, pc.ID, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.GetCredential(ctx, pc.ID); got.Meta.Status != CredentialDisabled || got.Meta.Error != "invalid_grant" {
		t.Errorf("disabled = %+v", got.Meta)
	}
	if list, _ := svc.ListCredentials(ctx, "u2"); len(list) != 0 {
		t.Errorf("u2 sees %d of u1's credentials", len(list))
	}
}

// A harness integration references only its owner's credentials, of
// providers its kind takes, never two that land in the same place.
func TestHarnessReferencesCredentials(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	mk := func(user, provider string, kind providers.Kind) string {
		c := providers.Credential{Provider: provider, Kind: kind, Key: "k", Access: "a", Refresh: "r"}
		if kind == providers.KindAPIKey {
			c.Access, c.Refresh = "", ""
		} else {
			c.Key = ""
		}
		pc, err := svc.CreateCredential(ctx, user, c, "")
		if err != nil {
			t.Fatal(err)
		}
		return pc.ID
	}
	openai := mk("u1", "openai", providers.KindAPIKey)
	chatgpt := mk("u1", "openai-chatgpt", providers.KindOAuth)
	gemini := mk("u1", "google-gemini-cli", providers.KindOAuth)
	theirs := mk("u2", "openrouter", providers.KindAPIKey)

	harness := func(kind string, ids ...string) Integration {
		refs := make([]any, len(ids))
		for i, id := range ids {
			refs[i] = id
		}
		return Integration{UserID: "u1", Port: PortHarness, Kind: kind, Name: kind + "-" + strings.Join(ids, ","),
			Config: map[string]any{ConfigCredentialIDs: refs}}
	}
	if in, err := svc.CreateIntegration(ctx, harness("codex", openai, chatgpt, openai)); err != nil {
		t.Errorf("codex key + login: %v", err)
	} else if got := CredentialRefIDs(in.Config); len(got) != 2 {
		t.Errorf("refs normalised to %v", got)
	}
	for _, tc := range []struct {
		name string
		in   Integration
		want string
	}{
		{"another user's", harness("opencode", theirs), "not found"},
		{"a provider the harness does not take", harness("codex", gemini), "cannot use"},
		{"two in one place", harness("opencode", openai, chatgpt), "in the same place"},
		{"secrets on a harness", Integration{UserID: "u1", Port: PortHarness, Kind: "codex", Name: "x", Secrets: map[string]string{"api_key": "k"}}, "holds no secrets"},
	} {
		if _, err := svc.CreateIntegration(ctx, tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := svc.DeleteCredential(ctx, chatgpt); !errors.Is(err, ErrReferenced) {
		t.Errorf("deleting a referenced credential: %v", err)
	}
	if ids, _ := svc.IntegrationsUsing(ctx, chatgpt); len(ids) != 1 {
		t.Errorf("integrations using = %v", ids)
	}
	if err := svc.DeleteCredential(ctx, gemini); err != nil {
		t.Errorf("deleting an unreferenced credential: %v", err)
	}
}

func TestInstanceSecretIsStable(t *testing.T) {
	svc, mem := newSvc(t)
	ctx := context.Background()
	a, err := svc.InstanceSecret(ctx, "auth-broker")
	if err != nil || len(a) != 32 {
		t.Fatalf("secret = %d bytes, %v", len(a), err)
	}
	b, _ := svc.InstanceSecret(ctx, "auth-broker")
	if !bytes.Equal(a, b) {
		t.Error("a second read minted a new secret")
	}
	again := New(mem, svc.key) // a restart: same store, same master key
	c, _ := again.InstanceSecret(ctx, "auth-broker")
	if !bytes.Equal(a, c) {
		t.Error("the secret did not survive a restart")
	}
	other, _ := svc.InstanceSecret(ctx, "other")
	if bytes.Equal(a, other) {
		t.Error("two names share a secret")
	}
}
