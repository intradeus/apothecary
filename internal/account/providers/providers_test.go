package providers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Every provider says how each harness it lists consumes it, and nothing is
// listed that no harness takes.
func TestTableIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		if seen[p.ID] {
			t.Errorf("%s listed twice", p.ID)
		}
		seen[p.ID] = true
		if len(p.Kinds()) == 0 || len(p.Harnesses) == 0 {
			t.Errorf("%s: no credential kind or no harness", p.ID)
		}
		if len(p.Domains) == 0 {
			t.Errorf("%s: no egress domain", p.ID)
		}
		for h, n := range p.Harnesses {
			if n.ID == "" || (!n.Key && !n.OAuth) {
				t.Errorf("%s on %s: %+v", p.ID, h, n)
			}
			if n.Key && !p.Key || n.OAuth && p.Flow == "" {
				t.Errorf("%s on %s takes a kind the provider does not offer", p.ID, h)
			}
			if n.Key && h != Opencode && n.Env == "" {
				t.Errorf("%s on %s: an API key with no env var", p.ID, h)
			}
		}
	}
}

func TestSinkConflicts(t *testing.T) {
	openai, _ := Get("openai")
	chatgpt, _ := Get("openai-chatgpt")
	if Sink(Opencode, openai, KindAPIKey) != Sink(Opencode, chatgpt, KindOAuth) {
		t.Error("opencode keeps an OpenAI key and a ChatGPT login in the same auth.json entry")
	}
	if Sink(Codex, openai, KindAPIKey) == Sink(Codex, chatgpt, KindOAuth) {
		t.Error("codex reads a key from env and a login from auth.json: no conflict")
	}
	zen, _ := Get("opencode-zen")
	gogw, _ := Get("opencode-go")
	if Sink(Omp, zen, KindAPIKey) != Sink(Omp, gogw, KindAPIKey) {
		t.Error("omp reads both OpenCode gateways from OPENCODE_API_KEY")
	}
	if Sink(Opencode, zen, KindAPIKey) == Sink(Opencode, gogw, KindAPIKey) {
		t.Error("opencode keeps the two gateways apart")
	}
	if Sink(Codex, zen, KindAPIKey) != "" {
		t.Error("codex does not take OpenCode")
	}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestImportCodex(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	access := jwt(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}})
	id := jwt(map[string]any{"email": "Me@Example.com"})
	doc := `{"OPENAI_API_KEY":"sk-test","tokens":{"id_token":"` + id + `","access_token":"` + access + `","refresh_token":"rt-1","account_id":null},"last_refresh":"2026-10-01T00:00:00Z"}`
	creds, rejected, err := Import(doc)
	if err != nil || len(rejected) != 0 || len(creds) != 2 {
		t.Fatalf("creds %+v rejected %v err %v", creds, rejected, err)
	}
	if creds[0].Provider != "openai" || creds[0].Key != "sk-test" {
		t.Errorf("key = %+v", creds[0])
	}
	l := creds[1]
	if l.Provider != "openai-chatgpt" || l.Refresh != "rt-1" || !l.Expires.Equal(exp) ||
		l.Get(ExtraAccountID) != "acct-1" || l.Get(ExtraEmail) != "me@example.com" || l.Get(ExtraIDToken) != id {
		t.Errorf("login = %+v", l)
	}
}

func TestImportOpencode(t *testing.T) {
	doc := `{
		"openrouter": {"type":"api","key":"sk-or"},
		"github-copilot": {"type":"oauth","refresh":"gho_x","access":"gho_x","expires":0},
		"anthropic": {"type":"oauth","refresh":"r","access":"a","expires":1},
		"bespoke": {"type":"api","key":"k"}
	}`
	creds, rejected, err := Import(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 || creds[0].Provider != "github-copilot" || creds[1].Provider != "openrouter" {
		t.Fatalf("creds = %+v", creds)
	}
	if !creds[0].Expires.IsZero() || creds[0].Refresh != "gho_x" {
		t.Errorf("copilot = %+v: the GitHub token does not expire", creds[0])
	}
	got := strings.Join(rejected, "\n")
	for _, want := range []string{"anthropic: a login apothecary cannot refresh", "bespoke: an API key for a provider apothecary does not manage"} {
		if !strings.Contains(got, want) {
			t.Errorf("rejected %q missing %q", got, want)
		}
	}
}

func TestImportLoginsAndEnv(t *testing.T) {
	creds, rejected, err := Import(`[{"type":"claude","access_token":"a","refresh_token":"r","expired":"2026-11-01T00:00:00Z","email":"x@y"},
		{"type":"gemini","access_token":"a","refresh_token":"r","expired":"2026-11-01T00:00:00Z"},
		{"type":"qwen","access_token":"a","refresh_token":"r"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].Provider != "anthropic" || creds[0].Label() != "x@y" || creds[0].Expires.Year() != 2026 {
		t.Errorf("creds = %+v", creds)
	}
	if len(rejected) != 2 || !strings.Contains(rejected[0], "needs project_id") || !strings.Contains(rejected[1], `"qwen"`) {
		t.Errorf("rejected = %v", rejected)
	}
	creds, rejected, err = Import("# keys\nexport ANTHROPIC_API_KEY=\"sk-ant\"\nOPENCODE_API_KEY='oc'\nWHATEVER=1\n")
	if err != nil || len(creds) != 2 || creds[0].Provider != "anthropic" || creds[0].Key != "sk-ant" || len(rejected) != 1 {
		t.Errorf("env: creds %+v rejected %v err %v", creds, rejected, err)
	}
}

func TestSecretsRoundTripAndRedactionSet(t *testing.T) {
	c := Credential{Kind: KindOAuth, Access: "acc", Refresh: "ref"}
	c.Set(ExtraIDToken, "idt")
	c.Set(ExtraEmail, "e@x")
	var back Credential
	FromSecrets(&back, c.Secrets())
	if back.Access != "acc" || back.Refresh != "ref" || back.Get(ExtraIDToken) != "idt" || back.Label() != "e@x" {
		t.Errorf("round trip = %+v", back)
	}
	for _, v := range c.SecretValues() {
		if v == "ref" {
			t.Error("a refresh token is never handed to a workspace, so it is not a workspace secret")
		}
	}
}
