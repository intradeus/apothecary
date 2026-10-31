package providers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Import reads a credential file a harness wrote — the manual route beside
// signing in from the engine — into credentials. Recognised: codex's
// auth.json, opencode's auth.json, CLIProxyAPI login JSON (one object or an
// array; what omp imports), and KEY=VALUE lines (omp's provider keys).
// Everything the file holds that is not a provider of this table, or not a
// credential kind it accepts, is reported in rejected rather than kept: the
// closed set is the contract.
func Import(text string) (creds []Credential, rejected []string, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil, fmt.Errorf("nothing to import")
	}
	if !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
		return importEnv(text)
	}
	if strings.HasPrefix(text, "[") {
		var docs []json.RawMessage
		if err := json.Unmarshal([]byte(text), &docs); err != nil {
			return nil, nil, fmt.Errorf("not JSON: %w", err)
		}
		return importLogins(docs)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return nil, nil, fmt.Errorf("not JSON: %w", err)
	}
	_, hasTokens := obj["tokens"]
	_, hasKey := obj["OPENAI_API_KEY"]
	_, hasType := obj["type"]
	switch {
	case hasTokens || hasKey:
		return importCodex(obj)
	case hasType:
		return importLogins([]json.RawMessage{json.RawMessage(text)})
	default:
		return importOpencode(obj)
	}
}

// importCodex reads codex's auth.json: an OpenAI API key and/or a ChatGPT
// login.
func importCodex(obj map[string]json.RawMessage) ([]Credential, []string, error) {
	var out []Credential
	var key *string
	if raw, ok := obj["OPENAI_API_KEY"]; ok {
		_ = json.Unmarshal(raw, &key)
	}
	if key != nil && *key != "" {
		out = append(out, Credential{Provider: "openai", Kind: KindAPIKey, Key: *key})
	}
	if raw, ok := obj["tokens"]; ok && string(raw) != "null" {
		var t struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, nil, fmt.Errorf("codex auth.json: tokens: %w", err)
		}
		if t.AccessToken == "" || t.RefreshToken == "" {
			return nil, nil, fmt.Errorf("codex auth.json: the ChatGPT login needs tokens.access_token and tokens.refresh_token")
		}
		c := Credential{Provider: "openai-chatgpt", Kind: KindOAuth, Access: t.AccessToken, Refresh: t.RefreshToken,
			Expires: JWTExpiry(t.AccessToken)}
		account, email := ChatGPTIdentity(t.AccessToken, t.IDToken)
		if t.AccountID != "" {
			account = t.AccountID
		}
		c.Set(ExtraIDToken, t.IDToken)
		c.Set(ExtraAccountID, account)
		c.Set(ExtraEmail, email)
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("codex auth.json holds neither an API key nor a ChatGPT login")
	}
	return out, nil, nil
}

// importOpencode reads opencode's auth.json: one entry per opencode provider
// id, an API key or a login.
func importOpencode(obj map[string]json.RawMessage) ([]Credential, []string, error) {
	var out []Credential
	var rejected []string
	ids := make([]string, 0, len(obj))
	for id := range obj {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var e struct {
			Type          string `json:"type"`
			Key           string `json:"key"`
			Access        string `json:"access"`
			Refresh       string `json:"refresh"`
			Expires       int64  `json:"expires"`
			AccountID     string `json:"accountId"`
			EnterpriseURL string `json:"enterpriseUrl"`
		}
		if err := json.Unmarshal(obj[id], &e); err != nil {
			rejected = append(rejected, fmt.Sprintf("%s: not an auth entry", id))
			continue
		}
		switch e.Type {
		case "api":
			p, ok := ByNative(Opencode, id, KindAPIKey)
			if !ok || e.Key == "" {
				rejected = append(rejected, fmt.Sprintf("%s: an API key for a provider apothecary does not manage", id))
				continue
			}
			out = append(out, Credential{Provider: p.ID, Kind: KindAPIKey, Key: e.Key})
		case "oauth":
			p, ok := ByNative(Opencode, id, KindOAuth)
			if !ok || e.Access == "" {
				rejected = append(rejected, fmt.Sprintf("%s: a login apothecary cannot refresh — use an API key for this provider", id))
				continue
			}
			c := Credential{Provider: p.ID, Kind: KindOAuth, Access: e.Access, Refresh: e.Refresh}
			if e.Expires > 0 {
				c.Expires = time.UnixMilli(e.Expires).UTC()
			}
			if p.Flow == FlowCopilot && c.Refresh == "" {
				c.Refresh = c.Access // the GitHub token is both
			}
			if p.Flow == FlowCopilot {
				c.Expires = time.Time{}
			}
			if p.Flow == FlowChatGPT {
				account, email := ChatGPTIdentity(e.Access)
				if e.AccountID != "" {
					account = e.AccountID
				}
				c.Set(ExtraAccountID, account)
				c.Set(ExtraEmail, email)
				if c.Expires.IsZero() {
					c.Expires = JWTExpiry(e.Access)
				}
			}
			c.Set(ExtraEnterprise, e.EnterpriseURL)
			if c.Refresh == "" {
				rejected = append(rejected, fmt.Sprintf("%s: the login has no refresh token", id))
				continue
			}
			out = append(out, c)
		default:
			rejected = append(rejected, fmt.Sprintf("%s: entry type %q is not a key or a login", id, e.Type))
		}
	}
	if len(out) == 0 && len(rejected) == 0 {
		return nil, nil, fmt.Errorf("the file holds no credential")
	}
	return out, rejected, nil
}

// loginTypes maps a CLIProxyAPI credential `type` (omp's importer's
// CLIPROXY_TYPE_TO_PROVIDER) to the provider it logs into.
var loginTypes = map[string]string{
	"claude":      "anthropic",
	"codex":       "openai-chatgpt",
	"gemini":      "google-gemini-cli",
	"gemini-cli":  "google-gemini-cli",
	"antigravity": "google-antigravity",
}

// importLogins reads CLIProxyAPI credential documents.
func importLogins(docs []json.RawMessage) ([]Credential, []string, error) {
	var out []Credential
	var rejected []string
	for i, d := range docs {
		var l struct {
			Type         string `json:"type"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			Expired      string `json:"expired"`
			Email        string `json:"email"`
			AccountID    string `json:"account_id"`
			ProjectID    string `json:"project_id"`
			Token        *struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				Expiry       string `json:"expiry"`
			} `json:"token"`
		}
		if err := json.Unmarshal(d, &l); err != nil {
			rejected = append(rejected, fmt.Sprintf("login %d: not a credential object", i+1))
			continue
		}
		if l.Token != nil { // gemini-cli's nested token shape
			if l.AccessToken == "" {
				l.AccessToken = l.Token.AccessToken
			}
			if l.RefreshToken == "" {
				l.RefreshToken = l.Token.RefreshToken
			}
			if l.Expired == "" {
				l.Expired = l.Token.Expiry
			}
		}
		pid, ok := loginTypes[strings.ToLower(l.Type)]
		if !ok {
			rejected = append(rejected, fmt.Sprintf("login %d: type %q is not one apothecary refreshes (claude, codex, gemini, antigravity)", i+1, l.Type))
			continue
		}
		if l.AccessToken == "" || l.RefreshToken == "" {
			rejected = append(rejected, fmt.Sprintf("login %d (%s): needs access_token and refresh_token", i+1, l.Type))
			continue
		}
		c := Credential{Provider: pid, Kind: KindOAuth, Access: l.AccessToken, Refresh: l.RefreshToken}
		if l.Expired != "" {
			if t, err := time.Parse(time.RFC3339, l.Expired); err == nil {
				c.Expires = t.UTC()
			}
		}
		if c.Expires.IsZero() {
			c.Expires = JWTExpiry(l.AccessToken)
		}
		c.Set(ExtraEmail, strings.ToLower(strings.TrimSpace(l.Email)))
		c.Set(ExtraProjectID, l.ProjectID)
		if pid == "openai-chatgpt" {
			account, email := ChatGPTIdentity(l.AccessToken, l.IDToken)
			if l.AccountID != "" {
				account = l.AccountID
			}
			c.Set(ExtraIDToken, l.IDToken)
			c.Set(ExtraAccountID, account)
			if c.Get(ExtraEmail) == "" {
				c.Set(ExtraEmail, email)
			}
		}
		if (pid == "google-gemini-cli" || pid == "google-antigravity") && c.Get(ExtraProjectID) == "" {
			rejected = append(rejected, fmt.Sprintf("login %d (%s): needs project_id — sign in from apothecary instead", i+1, l.Type))
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 && len(rejected) == 0 {
		return nil, nil, fmt.Errorf("the file holds no credential")
	}
	return out, rejected, nil
}

var envLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// importEnv reads dotenv-style KEY=VALUE lines: blank lines and # comments
// skipped, an `export ` prefix and surrounding quotes tolerated.
func importEnv(text string) ([]Credential, []string, error) {
	var out []Credential
	var rejected []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !envLine.MatchString(k) {
			return nil, nil, fmt.Errorf("line %d: want KEY=VALUE", i+1)
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		p, ok := ByEnv(Omp, k)
		if !ok {
			p, ok = ByEnv(Codex, k)
		}
		if !ok || v == "" {
			rejected = append(rejected, fmt.Sprintf("line %d: %s is not a provider key apothecary manages", i+1, k))
			continue
		}
		out = append(out, Credential{Provider: p.ID, Kind: KindAPIKey, Key: v})
	}
	if len(out) == 0 && len(rejected) == 0 {
		return nil, nil, fmt.Errorf("the file holds no credential")
	}
	return out, rejected, nil
}
