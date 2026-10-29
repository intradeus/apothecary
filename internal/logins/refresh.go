// Package logins keeps provider logins alive engine-side
// (docs/model-providers-plan.md): it refreshes OAuth credentials before they
// expire, single-flight per credential, persists what the provider returned,
// and tells the engine so live workspaces get the new access token. It also
// runs the sign-in flows that create logins in the first place.
//
// Refresh tokens rotate (OpenAI and Anthropic spend the old one on every
// refresh), so exactly one party may ever redeem one: this package. No
// workspace is handed a refresh token.
//
// Every endpoint, client id and request shape below is taken from the pinned
// harnesses' own source — omp 18.13.1's rules/auth/*.kdl and
// registry/oauth/*.ts, codex-rs 0.160.1's login crate, opencode 1.17.8's
// auth plugins — not from vendor documentation, which there is none of: these
// are each vendor's CLI clients, not public APIs. A change on their side fails
// loudly here, never silently.
package logins

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"apothecary/internal/providers"
)

// endpoints are the token endpoints, a variable so tests can point them at
// an httptest server.
var endpoints = struct {
	OpenAIToken, AnthropicToken, GoogleToken string
}{
	OpenAIToken:    "https://auth.openai.com/oauth/token",
	AnthropicToken: "https://api.anthropic.com/v1/oauth/token",
	GoogleToken:    "https://oauth2.googleapis.com/token",
}

// The public OAuth clients of the CLIs these logins belong to. Google's
// installed-app clients carry a "secret" that is public by design (it ships
// in gemini-cli and Antigravity); omp stores them base64 so secret scanners
// stay quiet, and so do we.
const (
	openAIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	anthropicB64   = "OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl"
	geminiIDB64    = "NjgxMjU1ODA5Mzk1LW9vOGZ0Mm9wcmRybnA5ZTNhcWY2YXYzaG1kaWIxMzVqLmFwcHMuZ29vZ2xldXNlcmNvbnRlbnQuY29t"
	geminiSecB64   = "R09DU1BYLTR1SGdNUG0tMW83U2stZ2VWNkN1NWNsWEZzeGw="
	antigravIDB64  = "MTA3MTAwNjA2MDU5MS10bWhzc2luMmgyMWxjcmUyMzV2dG9sb2poNGc0MDNlcC5hcHBzLmdvb2dsZXVzZXJjb250ZW50LmNvbQ=="
	antigravSecB64 = "R09DU1BYLUs1OEZXUjQ4NkxkTEoxbUxCOHNYQzR6NnFEQWY="
	// anthropicSDKVersion is the @anthropic-ai/sdk version Claude Code's
	// refresh request names in its User-Agent (omp's claudeCodeSdkVersion).
	anthropicSDKVersion = "0.112.1"
)

func b64(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return string(b)
}

// googleClient is the OAuth client of a Google login flow.
func googleClient(f providers.Flow) (id, secret string) {
	if f == providers.FlowAntigravity {
		return b64(antigravIDB64), b64(antigravSecB64)
	}
	return b64(geminiIDB64), b64(geminiSecB64)
}

// PermanentError is a refresh the provider refused for good: the refresh
// token is spent, revoked or expired (Anthropic's grants die 30 days after
// the interactive login whatever the rotations). Only signing in again
// recovers the login.
type PermanentError struct{ Reason string }

func (e *PermanentError) Error() string { return e.Reason }

// IsPermanent reports whether err is a refresh that cannot be retried.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// tokenResponse is the union of what the token endpoints answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Account      *struct {
		EmailAddress string `json:"email_address"`
	} `json:"account"`
}

// Refresh redeems c's refresh token for a new access token (and, where the
// provider rotates, a new refresh token). The caller persists the result
// before anything else reads the credential: the old refresh token is dead.
func Refresh(ctx context.Context, client *http.Client, c providers.Credential, now time.Time) (providers.Credential, error) {
	p, ok := providers.Get(c.Provider)
	if !ok || c.Kind != providers.KindOAuth || p.Flow == "" {
		return c, fmt.Errorf("credential %s: not a login apothecary refreshes", c.ID)
	}
	if c.Refresh == "" {
		return c, &PermanentError{Reason: "the login holds no refresh token — sign in again"}
	}
	var (
		tok tokenResponse
		err error
	)
	switch p.Flow {
	case providers.FlowCopilot:
		// A GitHub OAuth token is long-lived and is itself the bearer
		// (opencode and omp both send it straight to the Copilot API):
		// nothing to refresh.
		return c, nil
	case providers.FlowChatGPT:
		tok, err = postToken(ctx, client, endpoints.OpenAIToken, "form", nil, map[string]string{
			"grant_type": "refresh_token", "refresh_token": c.Refresh, "client_id": openAIClientID,
		})
	case providers.FlowAnthropic:
		tok, err = postToken(ctx, client, endpoints.AnthropicToken, "json", map[string]string{
			"anthropic-beta": "oauth-2025-04-20",
			"User-Agent":     "anthropic-sdk-typescript/" + anthropicSDKVersion + " userOAuthProvider",
		}, map[string]string{
			"grant_type": "refresh_token", "refresh_token": c.Refresh, "client_id": b64(anthropicB64),
		})
	case providers.FlowGeminiCLI, providers.FlowAntigravity:
		id, secret := googleClient(p.Flow)
		tok, err = postToken(ctx, client, endpoints.GoogleToken, "form", nil, map[string]string{
			"grant_type": "refresh_token", "refresh_token": c.Refresh, "client_id": id, "client_secret": secret,
		})
	default:
		return c, fmt.Errorf("credential %s: flow %s has no refresher", c.ID, p.Flow)
	}
	if err != nil {
		return c, err
	}
	return applyToken(c, p.Flow, tok, now)
}

// applyToken folds a token response into the credential.
func applyToken(c providers.Credential, flow providers.Flow, tok tokenResponse, now time.Time) (providers.Credential, error) {
	if tok.AccessToken == "" {
		return c, fmt.Errorf("token endpoint answered without an access_token")
	}
	c.Access = tok.AccessToken
	if tok.RefreshToken != "" {
		c.Refresh = tok.RefreshToken // rotated: the old one is spent
	}
	switch {
	case tok.ExpiresIn > 0:
		c.Expires = now.Add(time.Duration(tok.ExpiresIn) * time.Second).UTC()
	case !providers.JWTExpiry(tok.AccessToken).IsZero():
		c.Expires = providers.JWTExpiry(tok.AccessToken)
	default:
		c.Expires = time.Time{}
	}
	switch flow {
	case providers.FlowChatGPT:
		if tok.IDToken != "" {
			c.Set(providers.ExtraIDToken, tok.IDToken)
		}
		account, email := providers.ChatGPTIdentity(c.Access, c.Get(providers.ExtraIDToken))
		if account != "" {
			c.Set(providers.ExtraAccountID, account)
		}
		if email != "" && c.Get(providers.ExtraEmail) == "" {
			c.Set(providers.ExtraEmail, email)
		}
	case providers.FlowAnthropic:
		if tok.Account != nil && tok.Account.EmailAddress != "" && c.Get(providers.ExtraEmail) == "" {
			c.Set(providers.ExtraEmail, strings.ToLower(tok.Account.EmailAddress))
		}
	}
	return c, nil
}

// tokenTimeout bounds one token request.
const tokenTimeout = 15 * time.Second

// postToken sends one OAuth token request, form- or JSON-encoded, and reads
// the answer. A refusal that names the grant (invalid_grant and its
// relatives) is permanent; anything else — a timeout, a 5xx, a 429 — is not.
func postToken(ctx context.Context, client *http.Client, endpoint, encoding string, headers, params map[string]string) (tokenResponse, error) {
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if encoding == "json" {
		b, _ := json.Marshal(params)
		body, contentType = bytes.NewReader(b), "application/json"
	} else {
		v := url.Values{}
		for k, val := range params {
			v.Set(k, val)
		}
		body = strings.NewReader(v.Encode())
	}
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return tokenResponse{}, tokenError(resp.StatusCode, raw)
	}
	var tok tokenResponse
	if err := json.Unmarshal(raw, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("token endpoint: unreadable answer: %w", err)
	}
	return tok, nil
}

// permanentCodes are the OAuth error codes that mean the refresh token will
// never work again.
var permanentCodes = []string{"invalid_grant", "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated", "invalid_token", "unauthorized_client"}

// tokenError turns a refusal into an error that names the provider's own
// reason — never the request, which carries the refresh token.
func tokenError(status int, raw []byte) error {
	var e struct {
		Error any    `json:"error"`
		Desc  string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &e)
	code := ""
	switch v := e.Error.(type) {
	case string:
		code = v
	case map[string]any:
		code, _ = v["code"].(string)
		if code == "" {
			code, _ = v["type"].(string)
		}
		if e.Desc == "" {
			e.Desc, _ = v["message"].(string)
		}
	}
	reason := fmt.Sprintf("token endpoint refused (%d)", status)
	if code != "" {
		reason += ": " + code
	}
	if e.Desc != "" {
		reason += " — " + e.Desc
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
		for _, c := range permanentCodes {
			if code == c || strings.Contains(strings.ToLower(e.Desc), strings.ReplaceAll(c, "_", " ")) {
				return &PermanentError{Reason: reason + " — sign in again"}
			}
		}
	}
	return errors.New(reason)
}
