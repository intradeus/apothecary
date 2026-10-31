package logins

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"apothecary/internal/account/providers"
)

// Signing in from the engine (docs/model-providers-plan.md): the engine runs
// the provider's own CLI login flow, the person approves on the provider's
// page, and the engine — not a laptop CLI — receives the tokens. Two shapes,
// both of which work with an engine the browser cannot reach:
//
//   - device code (ChatGPT, GitHub Copilot): the person opens a URL and types a
//     short code; the engine polls until the provider says yes.
//   - authorization code with a paste (Anthropic, Gemini CLI): the person
//     opens a URL and approves; the provider shows a code, or sends the
//     browser to a loopback address that fails to load — the person pastes
//     the code or that address back, and the engine exchanges it.
//
// A pending sign-in lives in memory for 15 minutes: an engine restart loses
// it, and the person starts again.

// signinEndpoints are the authorization endpoints, variables for tests.
var signinEndpoints = struct {
	OpenAIDeviceCode, OpenAIDeviceToken, OpenAIDeviceRedirect, OpenAIDeviceVerify string
	AnthropicAuthorize                                                            string
	GoogleAuthorize, GoogleUserinfo, CodeAssist                                   string
	GitHubDeviceCode, GitHubAccessToken                                           string
}{
	OpenAIDeviceCode:     "https://auth.openai.com/api/accounts/deviceauth/usercode",
	OpenAIDeviceToken:    "https://auth.openai.com/api/accounts/deviceauth/token",
	OpenAIDeviceRedirect: "https://auth.openai.com/deviceauth/callback",
	OpenAIDeviceVerify:   "https://auth.openai.com/codex/device",
	AnthropicAuthorize:   "https://claude.ai/oauth/authorize",
	GoogleAuthorize:      "https://accounts.google.com/o/oauth2/v2/auth",
	GoogleUserinfo:       "https://www.googleapis.com/oauth2/v1/userinfo?alt=json",
	CodeAssist:           "https://cloudcode-pa.googleapis.com",
	GitHubDeviceCode:     "https://github.com/login/device/code",
	GitHubAccessToken:    "https://github.com/login/oauth/access_token",
}

const (
	// anthropicRedirect / geminiRedirect are the loopback callbacks the CLIs
	// register; the engine never listens on them — the person pastes what the
	// browser landed on.
	anthropicRedirect = "http://localhost:54545/callback"
	geminiRedirect    = "http://127.0.0.1:8085/oauth2callback"
	// copilotClientID is the OpenCode GitHub app opencode and omp sign in
	// with (minimal read:user grant).
	copilotClientID = "Ov23li8tweQw6odWQebz"
	// geminiCLIVersion is the Gemini CLI omp identifies as to Code Assist.
	geminiCLIVersion = "0.46.0"
	signinTTL        = 15 * time.Minute
)

var anthropicScopes = []string{"org:create_api_key", "user:profile", "user:inference", "user:sessions:claude_code", "user:mcp_servers", "user:file_upload"}
var geminiScopes = []string{"https://www.googleapis.com/auth/cloud-platform", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"}

// ErrPending is a device sign-in the person has not approved yet: poll again
// after the pending sign-in's interval.
var ErrPending = errors.New("waiting for the sign-in to be approved")

// Pending is a sign-in in progress, as the person sees it.
type Pending struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// URL is the provider page to open.
	URL string `json:"url"`
	// UserCode is the code to type there (device flows).
	UserCode string `json:"user_code,omitempty"`
	// Paste: the person pastes back the code the provider shows, or the
	// address the browser was sent to.
	Paste bool `json:"paste"`
	// IntervalSeconds is how often to ask whether a device sign-in is done.
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type signin struct {
	Pending
	userID   string
	flow     providers.Flow
	verifier string
	state    string
	device   string // OpenAI device_auth_id / GitHub device_code
}

// SignIns runs sign-in flows.
type SignIns struct {
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	m      map[string]*signin
}

// NewSignIns builds the sign-in runner.
func NewSignIns() *SignIns {
	return &SignIns{client: &http.Client{Timeout: 30 * time.Second}, now: time.Now, m: map[string]*signin{}}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkce() (verifier, challenge string) {
	verifier = randomString(64)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// Start begins a sign-in to provider for userID.
func (s *SignIns) Start(ctx context.Context, userID, provider string) (Pending, error) {
	p, ok := providers.Get(provider)
	if !ok || p.Flow == "" {
		return Pending{}, fmt.Errorf("%s has no sign-in — use an API key", provider)
	}
	now := s.now()
	si := &signin{userID: userID, flow: p.Flow}
	si.ID, si.Provider, si.ExpiresAt = randomString(18), p.ID, now.Add(signinTTL)
	switch p.Flow {
	case providers.FlowChatGPT:
		var r struct {
			DeviceAuthID string `json:"device_auth_id"`
			UserCode     string `json:"user_code"`
			Interval     any    `json:"interval"`
		}
		if err := s.postJSON(ctx, signinEndpoints.OpenAIDeviceCode, map[string]string{"client_id": openAIClientID}, nil, &r); err != nil {
			return Pending{}, fmt.Errorf("OpenAI device sign-in: %w", err)
		}
		if r.DeviceAuthID == "" || r.UserCode == "" {
			return Pending{}, fmt.Errorf("OpenAI device sign-in: incomplete answer")
		}
		si.device, si.UserCode, si.URL = r.DeviceAuthID, r.UserCode, signinEndpoints.OpenAIDeviceVerify
		si.IntervalSeconds = intervalOf(r.Interval, 5) + 3
	case providers.FlowCopilot:
		var r struct {
			DeviceCode      string `json:"device_code"`
			UserCode        string `json:"user_code"`
			VerificationURI string `json:"verification_uri"`
			Interval        int    `json:"interval"`
			ExpiresIn       int    `json:"expires_in"`
		}
		if err := s.postForm(ctx, signinEndpoints.GitHubDeviceCode, map[string]string{"client_id": copilotClientID, "scope": "read:user"}, githubHeaders, &r); err != nil {
			return Pending{}, fmt.Errorf("GitHub device sign-in: %w", err)
		}
		if r.DeviceCode == "" || r.UserCode == "" {
			return Pending{}, fmt.Errorf("GitHub device sign-in: incomplete answer")
		}
		si.device, si.UserCode, si.URL = r.DeviceCode, r.UserCode, r.VerificationURI
		si.IntervalSeconds = max(r.Interval, 5) + 1
		if r.ExpiresIn > 0 && time.Duration(r.ExpiresIn)*time.Second < signinTTL {
			si.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
		}
	case providers.FlowAnthropic:
		verifier, challenge := pkce()
		si.verifier, si.state, si.Paste = verifier, randomString(24), true
		q := url.Values{
			"code": {"true"}, "client_id": {b64(anthropicB64)}, "response_type": {"code"},
			"redirect_uri": {anthropicRedirect}, "scope": {strings.Join(anthropicScopes, " ")},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {si.state},
		}
		si.URL = signinEndpoints.AnthropicAuthorize + "?" + q.Encode()
	case providers.FlowGeminiCLI:
		id, _ := googleClient(p.Flow)
		si.state, si.Paste = randomString(24), true
		q := url.Values{
			"client_id": {id}, "redirect_uri": {geminiRedirect}, "response_type": {"code"},
			"scope": {strings.Join(geminiScopes, " ")}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {si.state},
		}
		si.URL = signinEndpoints.GoogleAuthorize + "?" + q.Encode()
	default:
		return Pending{}, fmt.Errorf("signing in to %s from apothecary is not supported yet — import a login file instead", p.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, old := range s.m { // one at a time per person and provider; drop the expired
		if old.ExpiresAt.Before(now) || (old.userID == userID && old.Provider == p.ID) {
			delete(s.m, id)
		}
	}
	s.m[si.ID] = si
	return si.Pending, nil
}

// Complete finishes a sign-in: for a device flow it asks once whether the
// person approved (ErrPending while not); for a paste flow it exchanges what
// the person pasted. The credential it returns is the caller's to store.
func (s *SignIns) Complete(ctx context.Context, userID, id, pasted string) (providers.Credential, error) {
	s.mu.Lock()
	si, ok := s.m[id]
	s.mu.Unlock()
	if !ok || si.userID != userID {
		return providers.Credential{}, fmt.Errorf("no such sign-in (it expired, or the engine restarted) — start again")
	}
	if si.ExpiresAt.Before(s.now()) {
		s.drop(id)
		return providers.Credential{}, fmt.Errorf("the sign-in expired — start again")
	}
	var (
		c   providers.Credential
		err error
	)
	switch si.flow {
	case providers.FlowChatGPT:
		c, err = s.completeChatGPT(ctx, si)
	case providers.FlowCopilot:
		c, err = s.completeCopilot(ctx, si)
	case providers.FlowAnthropic:
		c, err = s.completeAnthropic(ctx, si, pasted)
	case providers.FlowGeminiCLI:
		c, err = s.completeGemini(ctx, si, pasted)
	default:
		err = fmt.Errorf("unsupported sign-in")
	}
	if errors.Is(err, ErrPending) {
		return c, err
	}
	s.drop(id) // done, or failed for good: either way it is over
	if err != nil {
		return c, err
	}
	c.Provider, c.Kind = si.Provider, providers.KindOAuth
	return c, nil
}

func (s *SignIns) drop(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

func (s *SignIns) completeChatGPT(ctx context.Context, si *signin) (providers.Credential, error) {
	body, _ := json.Marshal(map[string]string{"device_auth_id": si.device, "user_code": si.UserCode})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, signinEndpoints.OpenAIDeviceToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return providers.Credential{}, ErrPending // transient: ask again
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return providers.Credential{}, ErrPending
	}
	if resp.StatusCode/100 != 2 {
		return providers.Credential{}, fmt.Errorf("OpenAI device sign-in failed (%d)", resp.StatusCode)
	}
	var r struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if json.Unmarshal(raw, &r) != nil || r.AuthorizationCode == "" || r.CodeVerifier == "" {
		return providers.Credential{}, fmt.Errorf("OpenAI device sign-in: incomplete answer")
	}
	tok, err := postToken(ctx, s.client, endpoints.OpenAIToken, "form", nil, map[string]string{
		"grant_type": "authorization_code", "client_id": openAIClientID, "code": r.AuthorizationCode,
		"code_verifier": r.CodeVerifier, "redirect_uri": signinEndpoints.OpenAIDeviceRedirect,
	})
	if err != nil {
		return providers.Credential{}, err
	}
	if tok.RefreshToken == "" {
		return providers.Credential{}, fmt.Errorf("OpenAI returned no refresh token")
	}
	c, err := applyToken(providers.Credential{}, providers.FlowChatGPT, tok, s.now())
	if err != nil {
		return c, err
	}
	if c.Get(providers.ExtraAccountID) == "" && c.Get(providers.ExtraEmail) == "" {
		return c, fmt.Errorf("OpenAI's token names no ChatGPT account")
	}
	return c, nil
}

var githubHeaders = map[string]string{"Accept": "application/json", "User-Agent": "copilot-developer-action/0.0.1"}

func (s *SignIns) completeCopilot(ctx context.Context, si *signin) (providers.Credential, error) {
	var r struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	if err := s.postForm(ctx, signinEndpoints.GitHubAccessToken, map[string]string{
		"client_id": copilotClientID, "device_code": si.device, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
	}, githubHeaders, &r); err != nil {
		return providers.Credential{}, ErrPending
	}
	switch {
	case r.AccessToken != "":
		// The GitHub token is the Copilot bearer and does not expire: it is
		// both the access and the "refresh" side, and nothing rotates it.
		return providers.Credential{Access: r.AccessToken, Refresh: r.AccessToken}, nil
	case r.Error == "authorization_pending" || r.Error == "slow_down":
		return providers.Credential{}, ErrPending
	default:
		return providers.Credential{}, fmt.Errorf("GitHub sign-in failed: %s %s", r.Error, r.Desc)
	}
}

// pastedCode reads what the person pasted: the address the browser landed on
// (…?code=…&state=…), Anthropic's displayed code#state, or a bare code.
func pastedCode(pasted, wantState string) (string, error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return "", fmt.Errorf("paste the code the provider showed, or the address your browser was sent to")
	}
	code, state := pasted, ""
	if u, err := url.Parse(pasted); err == nil && u.Query().Get("code") != "" {
		code, state = u.Query().Get("code"), u.Query().Get("state")
	} else if c, st, ok := strings.Cut(pasted, "#"); ok {
		code, state = c, st
	}
	if state != "" && state != wantState {
		return "", fmt.Errorf("that code belongs to another sign-in — start again")
	}
	return code, nil
}

func (s *SignIns) completeAnthropic(ctx context.Context, si *signin, pasted string) (providers.Credential, error) {
	code, err := pastedCode(pasted, si.state)
	if err != nil {
		return providers.Credential{}, err
	}
	tok, err := postToken(ctx, s.client, endpoints.AnthropicToken, "json", nil, map[string]string{
		"grant_type": "authorization_code", "code": code, "state": si.state, "client_id": b64(anthropicB64),
		"redirect_uri": anthropicRedirect, "code_verifier": si.verifier,
	})
	if err != nil {
		return providers.Credential{}, err
	}
	if tok.RefreshToken == "" {
		return providers.Credential{}, fmt.Errorf("Anthropic returned no refresh token")
	}
	return applyToken(providers.Credential{}, providers.FlowAnthropic, tok, s.now())
}

func (s *SignIns) completeGemini(ctx context.Context, si *signin, pasted string) (providers.Credential, error) {
	code, err := pastedCode(pasted, si.state)
	if err != nil {
		return providers.Credential{}, err
	}
	id, secret := googleClient(si.flow)
	tok, err := postToken(ctx, s.client, endpoints.GoogleToken, "form", nil, map[string]string{
		"grant_type": "authorization_code", "code": code, "client_id": id, "client_secret": secret, "redirect_uri": geminiRedirect,
	})
	if err != nil {
		return providers.Credential{}, err
	}
	if tok.RefreshToken == "" {
		return providers.Credential{}, fmt.Errorf("Google returned no refresh token — sign in again and grant offline access")
	}
	c, err := applyToken(providers.Credential{}, si.flow, tok, s.now())
	if err != nil {
		return c, err
	}
	var info struct {
		Email string `json:"email"`
	}
	if s.getJSON(ctx, signinEndpoints.GoogleUserinfo, map[string]string{"Authorization": "Bearer " + c.Access}, &info) == nil {
		c.Set(providers.ExtraEmail, strings.ToLower(info.Email))
	}
	project, err := s.geminiProject(ctx, c.Access)
	if err != nil {
		return c, err
	}
	c.Set(providers.ExtraProjectID, project)
	return c, nil
}

// geminiProject finds the account's Cloud Code Assist project, provisioning
// the free tier for an account that has none — what gemini-cli does on its
// first login (omp's google-gemini-cli project hook).
func (s *SignIns) geminiProject(ctx context.Context, access string) (string, error) {
	h := map[string]string{
		"Authorization": "Bearer " + access,
		// Gemini CLI's own User-Agent shape names a model; this is the client
		// fingerprint omp sends, not a model apothecary runs. lint:models-ok
		"User-Agent":      "GeminiCLI/" + geminiCLIVersion + "/gemini-3.1-pro-preview (linux; x64; terminal)", // lint:models-ok
		"Client-Metadata": "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI",
	}
	meta := map[string]any{"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}
	var load struct {
		Project      string               `json:"cloudaicompanionProject"`
		CurrentTier  *struct{ ID string } `json:"currentTier"`
		AllowedTiers []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"allowedTiers"`
	}
	if err := s.postJSONAny(ctx, signinEndpoints.CodeAssist+"/v1internal:loadCodeAssist", map[string]any{"metadata": meta}, h, &load); err != nil {
		return "", fmt.Errorf("Gemini Code Assist: %w", err)
	}
	if load.CurrentTier != nil {
		if load.Project != "" {
			return load.Project, nil
		}
		return "", fmt.Errorf("this Google account needs a Cloud project for Gemini Code Assist (GOOGLE_CLOUD_PROJECT) — import a gemini-cli login that names project_id")
	}
	tier := "legacy-tier"
	for _, t := range load.AllowedTiers {
		if t.IsDefault {
			tier = t.ID
		}
	}
	if tier != "free-tier" {
		return "", fmt.Errorf("this Google account's Code Assist tier (%s) needs a Cloud project — import a gemini-cli login that names project_id", tier)
	}
	var op struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response *struct {
			Project *struct{ ID string } `json:"cloudaicompanionProject"`
		} `json:"response"`
	}
	if err := s.postJSONAny(ctx, signinEndpoints.CodeAssist+"/v1internal:onboardUser", map[string]any{"tierId": tier, "metadata": meta}, h, &op); err != nil {
		return "", fmt.Errorf("Gemini Code Assist onboarding: %w", err)
	}
	for i := 0; !op.Done && op.Name != "" && i < 24; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
		if err := s.getJSON(ctx, signinEndpoints.CodeAssist+"/v1internal/"+op.Name, h, &op); err != nil {
			return "", fmt.Errorf("Gemini Code Assist onboarding: %w", err)
		}
	}
	if op.Response != nil && op.Response.Project != nil && op.Response.Project.ID != "" {
		return op.Response.Project.ID, nil
	}
	return "", fmt.Errorf("Gemini Code Assist did not provision a project")
}

func intervalOf(v any, def int) int {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil && i > 0 {
			return i
		}
	}
	return def
}

func (s *SignIns) postJSON(ctx context.Context, u string, body map[string]string, headers map[string]string, out any) error {
	m := make(map[string]any, len(body))
	for k, v := range body {
		m[k] = v
	}
	return s.postJSONAny(ctx, u, m, headers, out)
}

func (s *SignIns) postJSONAny(ctx context.Context, u string, body map[string]any, headers map[string]string, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req, headers, out)
}

func (s *SignIns) postForm(ctx context.Context, u string, form map[string]string, headers map[string]string, out any) error {
	v := url.Values{}
	for k, val := range form {
		v.Set(k, val)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(req, headers, out)
}

func (s *SignIns) getJSON(ctx context.Context, u string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return s.do(req, headers, out)
}

func (s *SignIns) do(req *http.Request, headers map[string]string, out any) error {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s answered %d", req.URL.Host, resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}
