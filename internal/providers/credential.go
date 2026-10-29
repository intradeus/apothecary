package providers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Credential is one provider credential, opened: what the keeper refreshes
// and a harness renders. It never crosses the API (confstore keeps the
// secret half sealed; the API shows metadata only).
type Credential struct {
	ID       string
	Provider string
	Kind     Kind
	// Key is an API key (KindAPIKey).
	Key string
	// Access / Refresh / Expires are a login's tokens (KindOAuth). Refresh
	// never reaches a workspace; a zero Expires means the access token does
	// not expire (a GitHub token).
	Access  string
	Refresh string
	Expires time.Time
	// Extra holds the provider's identity fields: ExtraIDToken and
	// ExtraAccountID (ChatGPT), ExtraProjectID (Google), ExtraEmail, and
	// ExtraEnterprise / ExtraAPIEndpoint (Copilot).
	Extra map[string]string
}

// Extra keys.
const (
	ExtraIDToken     = "id_token"
	ExtraAccountID   = "account_id"
	ExtraProjectID   = "project_id"
	ExtraEmail       = "email"
	ExtraEnterprise  = "enterprise_url"
	ExtraAPIEndpoint = "api_endpoint"
)

// Secrets is the half of a credential that is sealed at rest: the key or the
// tokens and every extra. Expiry and identity labels live in metadata too, so
// a listing never needs the master key.
func (c Credential) Secrets() map[string]string {
	m := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("key", c.Key)
	set("access", c.Access)
	set("refresh", c.Refresh)
	for k, v := range c.Extra {
		set("extra."+k, v)
	}
	return m
}

// FromSecrets rebuilds a credential's secret half.
func FromSecrets(c *Credential, m map[string]string) {
	c.Key, c.Access, c.Refresh = m["key"], m["access"], m["refresh"]
	c.Extra = nil
	for k, v := range m {
		if name, ok := strings.CutPrefix(k, "extra."); ok {
			if c.Extra == nil {
				c.Extra = map[string]string{}
			}
			c.Extra[name] = v
		}
	}
}

// Get returns an extra field.
func (c Credential) Get(name string) string { return c.Extra[name] }

// Set stores an extra field ("" removes it).
func (c *Credential) Set(name, v string) {
	if v == "" {
		delete(c.Extra, name)
		return
	}
	if c.Extra == nil {
		c.Extra = map[string]string{}
	}
	c.Extra[name] = v
}

// Label is what the UI names an account by: the login's email, else none.
func (c Credential) Label() string { return c.Get(ExtraEmail) }

// SecretValues are the strings a workspace may be handed for this credential
// in some form — what the run's redactor must know. A refresh token is not
// among them because no workspace ever receives one.
func (c Credential) SecretValues() []string {
	var out []string
	for _, v := range []string{c.Key, c.Access, c.Get(ExtraIDToken)} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// JWTClaims decodes a JWT's payload without verifying it — enough to read
// the identity and expiry a provider stamped into a token it issued us. nil
// when the token is not a JWT.
func JWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

// JWTExpiry is a JWT's exp claim, zero when absent.
func JWTExpiry(token string) time.Time {
	if exp, ok := JWTClaims(token)["exp"].(float64); ok && exp > 0 {
		return time.Unix(int64(exp), 0).UTC()
	}
	return time.Time{}
}

// ChatGPTIdentity reads the ChatGPT account id and email OpenAI puts in its
// access and id tokens (claims https://api.openai.com/auth and /profile).
func ChatGPTIdentity(tokens ...string) (accountID, email string) {
	for _, t := range tokens {
		claims := JWTClaims(t)
		if a, ok := claims["https://api.openai.com/auth"].(map[string]any); ok && accountID == "" {
			accountID, _ = a["chatgpt_account_id"].(string)
		}
		if p, ok := claims["https://api.openai.com/profile"].(map[string]any); ok && email == "" {
			email, _ = p["email"].(string)
		}
		if email == "" {
			email, _ = claims["email"].(string)
		}
	}
	return accountID, strings.ToLower(strings.TrimSpace(email))
}
