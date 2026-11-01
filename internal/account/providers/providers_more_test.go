package providers

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestImportRefusals(t *testing.T) {
	for name, c := range map[string]struct{ text, want string }{
		"nothing":             {"   \n", "nothing to import"},
		"broken array":        {"[1,", "not JSON"},
		"broken object":       {`{"a":`, "not JSON"},
		"empty object":        {"{}", "holds no credential"},
		"empty array":         {"[]", "holds no credential"},
		"only comments":       {"# nothing here\n\n", "holds no credential"},
		"not key=value":       {"just some words", "line 1"},
		"bad key name":        {"1BAD=x", "line 1"},
		"second line bad":     {"OPENAI_API_KEY=x\nnot a pair", "line 2"},
		"codex tokens scalar": {`{"tokens":"nope"}`, "tokens"},
		"codex half a login":  {`{"tokens":{"access_token":"a"}}`, "refresh_token"},
		"codex empty":         {`{"OPENAI_API_KEY":null,"tokens":null}`, "neither an API key"},
		"codex empty key":     {`{"OPENAI_API_KEY":""}`, "neither an API key"},
	} {
		creds, _, err := Import(c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) || creds != nil {
			t.Errorf("%s: creds %v, err %v, want it to say %q", name, creds, err, c.want)
		}
	}
}

func TestImportCodexIdentity(t *testing.T) {
	access := jwt(map[string]any{
		"exp":                         float64(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "from-claim"},
	})
	creds, _, err := Import(`{"OPENAI_API_KEY":"sk-x","tokens":{"access_token":"` + access + `","refresh_token":"r","account_id":"from-file","id_token":"` +
		jwt(map[string]any{"email": " Me@X.COM "}) + `"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 || creds[0].Provider != "openai" || creds[0].Key != "sk-x" {
		t.Fatalf("creds = %+v", creds)
	}
	login := creds[1]
	if login.Provider != "openai-chatgpt" || login.Get(ExtraAccountID) != "from-file" || login.Get(ExtraEmail) != "me@x.com" || login.Expires.Year() != 2030 {
		t.Errorf("login = %+v", login)
	}
}

func TestImportOpencodeLogins(t *testing.T) {
	access := jwt(map[string]any{
		"exp":                            float64(time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		"https://api.openai.com/profile": map[string]any{"email": "Pro@X.com"},
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "claimed"},
	})
	expires := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	creds, rejected, err := Import(`{
		"openai": {"type":"oauth","access":"` + access + `","refresh":"r"},
		"github-copilot": {"type":"oauth","access":"gho_x","expires":` + strconv.FormatInt(expires.UnixMilli(), 10) + `,"enterpriseUrl":"https://ghe.example"},
		"noisy": 5,
		"weird": {"type":"session"},
		"bespoke": {"type":"api","key":"k"},
		"also-bespoke": {"type":"oauth","access":"a","refresh":"r"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Credential{}
	for _, c := range creds {
		by[c.Provider] = c
	}
	if c := by["openai-chatgpt"]; c.Get(ExtraAccountID) != "claimed" || c.Get(ExtraEmail) != "pro@x.com" || c.Expires.Year() != 2031 {
		t.Errorf("chatgpt = %+v", c)
	}
	// A GitHub token is its own refresh token and never expires.
	if c := by["github-copilot"]; c.Refresh != "gho_x" || !c.Expires.IsZero() || c.Get(ExtraEnterprise) != "https://ghe.example" {
		t.Errorf("copilot = %+v", c)
	}
	if len(rejected) != 4 {
		t.Fatalf("rejected = %q", rejected)
	}
	joined := strings.Join(rejected, "\n")
	for _, want := range []string{"noisy: not an auth entry", `weird: entry type "session"`, "bespoke: an API key", "also-bespoke: a login apothecary cannot refresh"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rejected %q, want %q", joined, want)
		}
	}

	// A login with no refresh token cannot be kept alive; an account id in the
	// file wins over the token's claim.
	_, rejected, err = Import(`{"openai":{"type":"oauth","access":"a"}}`)
	if err != nil || len(rejected) != 1 || !strings.Contains(rejected[0], "no refresh token") {
		t.Errorf("no refresh = %q, %v", rejected, err)
	}
	creds, _, err = Import(`{"openai":{"type":"oauth","access":"a","refresh":"r","accountId":"file","expires":` + strconv.FormatInt(expires.UnixMilli(), 10) + `}}`)
	if err != nil || creds[0].Get(ExtraAccountID) != "file" || !creds[0].Expires.Equal(expires) {
		t.Errorf("identity from the file = %+v, %v", creds, err)
	}
}

func TestImportCLIProxyLogins(t *testing.T) {
	expiry := "2033-05-01T00:00:00Z"
	// One document, not an array, and the nested token shape.
	creds, rejected, err := Import(`{"type":"gemini-cli","project_id":"proj","email":" Me@X.com ","token":{"access_token":"a","refresh_token":"r","expiry":"` + expiry + `"}}`)
	if err != nil || len(rejected) != 0 || len(creds) != 1 {
		t.Fatalf("nested = %+v %q %v", creds, rejected, err)
	}
	c := creds[0]
	if c.Provider != "google-gemini-cli" || c.Access != "a" || c.Refresh != "r" || c.Expires.Year() != 2033 ||
		c.Get(ExtraProjectID) != "proj" || c.Get(ExtraEmail) != "me@x.com" {
		t.Errorf("nested login = %+v", c)
	}

	access := jwt(map[string]any{
		"exp":                            float64(time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		"https://api.openai.com/profile": map[string]any{"email": "Token@X.com"},
	})
	creds, rejected, err = Import(`[
		{"type":"codex","access_token":"` + access + `","refresh_token":"r","account_id":"acct","id_token":"idt","expired":"not a date"},
		"not an object",
		{"type":"nonsense"},
		{"type":"claude","access_token":"a"},
		{"type":"antigravity","access_token":"a","refresh_token":"r"},
		{"type":"CLAUDE","access_token":"a","refresh_token":"r"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("creds = %+v", creds)
	}
	cx := creds[0]
	if cx.Provider != "openai-chatgpt" || cx.Expires.Year() != 2034 || cx.Get(ExtraAccountID) != "acct" ||
		cx.Get(ExtraIDToken) != "idt" || cx.Get(ExtraEmail) != "token@x.com" {
		t.Errorf("codex login = %+v", cx)
	}
	if creds[1].Provider != "anthropic" {
		t.Errorf("type case = %+v", creds[1])
	}
	joined := strings.Join(rejected, "\n")
	for _, want := range []string{"login 2: not a credential object", `login 3: type "nonsense"`, "login 4 (claude): needs access_token", "login 5 (antigravity): needs project_id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rejected %q, want %q", joined, want)
		}
	}
}

func TestImportEnvLines(t *testing.T) {
	creds, rejected, err := Import("# a comment\n\nexport OPENAI_API_KEY=\"sk-quoted\"\nOPENROUTER_API_KEY='sk-single'\nSOMETHING_ELSE=x\nANTHROPIC_API_KEY=\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 || creds[0].Key != "sk-quoted" || creds[1].Key != "sk-single" {
		t.Errorf("creds = %+v", creds)
	}
	if len(rejected) != 2 || !strings.Contains(rejected[0], "SOMETHING_ELSE") || !strings.Contains(rejected[1], "line 6") {
		t.Errorf("rejected = %q", rejected)
	}
	// A rejected line names the variable and never its value.
	if strings.Contains(strings.Join(rejected, ""), "=x") {
		t.Errorf("a value leaked into a refusal: %q", rejected)
	}
}

func TestJWTParsing(t *testing.T) {
	for name, tok := range map[string]string{
		"not a jwt":  "plain",
		"two parts":  "a.b",
		"bad base64": "a.!!!.c",
		"not json":   "a." + base64.RawURLEncoding.EncodeToString([]byte("x")) + ".c",
	} {
		if got := JWTClaims(tok); got != nil {
			t.Errorf("%s = %v", name, got)
		}
		if got := JWTExpiry(tok); !got.IsZero() {
			t.Errorf("%s expiry = %v", name, got)
		}
	}
	if got := JWTExpiry(jwt(map[string]any{"sub": "x"})); !got.IsZero() {
		t.Errorf("no exp = %v", got)
	}
	if got := JWTExpiry(jwt(map[string]any{"exp": float64(0)})); !got.IsZero() {
		t.Errorf("zero exp = %v", got)
	}
	if got := JWTExpiry(jwt(map[string]any{"exp": float64(1893456000)})); got.Unix() != 1893456000 {
		t.Errorf("exp = %v", got)
	}
}

func TestChatGPTIdentityTakesTheFirstOfEach(t *testing.T) {
	a := jwt(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "first"}})
	b := jwt(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "second"}, "email": "Later@X.com"})
	account, email := ChatGPTIdentity(a, b, "not a token")
	if account != "first" || email != "later@x.com" {
		t.Errorf("identity = %q %q", account, email)
	}
}

func TestForListsAHarnessesProviders(t *testing.T) {
	got := For(Codex)
	if len(got) == 0 || len(got) >= len(All()) {
		t.Fatalf("For(codex) = %d of %d", len(got), len(All()))
	}
	for _, p := range got {
		if _, ok := p.Harnesses[Codex]; !ok {
			t.Errorf("%s listed for a harness it does not serve", p.ID)
		}
	}
	if got := For("no-such-harness"); len(got) != 0 {
		t.Errorf("unknown harness = %+v", got)
	}
}

func TestNativeRefusesWhatItCannotTake(t *testing.T) {
	openai, _ := Get("openai")
	if _, ok := openai.Native("no-such-harness", KindAPIKey); ok {
		t.Error("an unknown harness consumes openai")
	}
	if _, ok := openai.Native(Codex, Kind("carrier-pigeon")); ok {
		t.Error("an unknown credential kind is accepted")
	}
	if got := Sink("no-such-harness", openai, KindAPIKey); got != "" {
		t.Errorf("sink of an unknown harness = %q", got)
	}
	// omp keeps a login by its own id.
	anthropic, _ := Get("anthropic")
	if got := Sink(Omp, anthropic, KindOAuth); !strings.HasPrefix(got, "login:") {
		t.Errorf("omp login sink = %q", got)
	}
}

func TestSetAndGetExtras(t *testing.T) {
	var c Credential
	c.Set("x", "") // removing from nothing is fine
	if c.Extra != nil {
		t.Errorf("extra = %v", c.Extra)
	}
	c.Set("x", "1")
	c.Set("y", "2")
	c.Set("x", "")
	if c.Get("x") != "" || c.Get("y") != "2" {
		t.Errorf("extra = %v", c.Extra)
	}
	if (Credential{Extra: map[string]string{ExtraEmail: "a@x"}}).Label() != "a@x" {
		t.Error("label is not the email")
	}
}
