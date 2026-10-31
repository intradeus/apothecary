package httpapi

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/logins"
	"apothecary/internal/account/providers"
	"apothecary/internal/authsink"
	"apothecary/internal/confstore"
	"apothecary/internal/kinds"
)

// Model provider credentials over the API (docs/model-providers-plan.md):
// a person's API keys and logins, shown as metadata only — no route returns
// a key or a token. A login is created by signing in from the engine (or by
// importing a file a CLI wrote); the keeper refreshes it from then on.

// ProviderInfo is one provider of the closed set, as a form offers it.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Key: it takes an API key. Login: it has a login apothecary keeps
	// (SignIn: one it can start from here; else import a file).
	Key    bool `json:"key"`
	Login  bool `json:"login"`
	SignIn bool `json:"sign_in"`
	// Harnesses are the harness kinds that use it.
	Harnesses []string `json:"harnesses"`
}

// CredentialRequest creates an API-key credential.
type CredentialRequest struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Name     string `json:"name,omitempty"`
}

// CredentialImportRequest is a credential file a harness CLI wrote.
type CredentialImportRequest struct {
	Text string `json:"text"`
}

// CredentialImportResult is what an import kept and what it refused.
type CredentialImportResult struct {
	Created  []confstore.ProviderCredential `json:"created"`
	Rejected []string                       `json:"rejected"`
}

// CredentialRename renames a credential.
type CredentialRename struct {
	Name string `json:"name"`
}

// SignInRequest starts a sign-in.
type SignInRequest struct {
	Provider string `json:"provider"`
}

// SignInComplete finishes one: what the person pasted (a paste flow), and an
// optional name for the credential.
type SignInComplete struct {
	Pasted string `json:"pasted,omitempty"`
	Name   string `json:"name,omitempty"`
}

// SignInResult is a finished sign-in's credential, or pending: true while a
// device sign-in waits for approval (poll again after its interval).
type SignInResult struct {
	Pending    bool                          `json:"pending"`
	Credential *confstore.ProviderCredential `json:"credential,omitempty"`
}

// signInFlows are the logins apothecary can start itself.
func signInFlow(f providers.Flow) bool {
	switch f {
	case providers.FlowChatGPT, providers.FlowAnthropic, providers.FlowGeminiCLI, providers.FlowCopilot:
		return true
	}
	return false
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	harness := r.URL.Query().Get("harness")
	list := providers.All()
	if harness != "" {
		list = providers.For(harness)
	}
	out := make([]ProviderInfo, 0, len(list))
	for _, p := range list {
		info := ProviderInfo{ID: p.ID, Name: p.Name, Key: p.Key, Login: p.Flow != "", SignIn: signInFlow(p.Flow), Harnesses: []string{}}
		for _, h := range []string{providers.Codex, providers.Omp, providers.Opencode} {
			if _, ok := p.Harnesses[h]; ok {
				info.Harnesses = append(info.Harnesses, h)
			}
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Conf.ListCredentials(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		confErr(w, err)
		return
	}
	if rows == nil {
		rows = []confstore.ProviderCredential{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var body CredentialRequest
	if !readJSON(w, r, &body) {
		return
	}
	pc, err := s.Conf.CreateCredential(r.Context(), auth.UserID(r.Context()),
		providers.Credential{Provider: body.Provider, Kind: providers.KindAPIKey, Key: strings.TrimSpace(body.Key)}, body.Name)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pc)
}

func (s *Server) importCredentials(w http.ResponseWriter, r *http.Request) {
	var body CredentialImportRequest
	if !readJSON(w, r, &body) {
		return
	}
	creds, rejected, err := providers.Import(body.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := CredentialImportResult{Created: []confstore.ProviderCredential{}, Rejected: rejected}
	if out.Rejected == nil {
		out.Rejected = []string{}
	}
	for _, c := range creds {
		pc, err := s.Conf.CreateCredential(r.Context(), auth.UserID(r.Context()), c, "")
		if err != nil {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: %v", c.Provider, err))
			continue
		}
		out.Created = append(out.Created, pc)
	}
	// Nothing kept is still an answer: the refusals say why.
	status := http.StatusCreated
	if len(out.Created) == 0 {
		status = http.StatusOK
	}
	writeJSON(w, status, out)
}

func (s *Server) getCredential(w http.ResponseWriter, r *http.Request) {
	pc, err := s.Conf.GetCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pc)
}

func (s *Server) renameCredential(w http.ResponseWriter, r *http.Request) {
	var body CredentialRename
	if !readJSON(w, r, &body) {
		return
	}
	pc, err := s.Conf.RenameCredential(r.Context(), r.PathValue("id"), body.Name)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pc)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteCredential(r.Context(), r.PathValue("id")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testCredential checks a credential against its provider: a login by
// refreshing it if it is due (the keeper's single-flight Fresh) and, for a
// ChatGPT login, asking its account for its models; an API key with its
// provider's zero-token probe.
func (s *Server) testCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var (
		c   providers.Credential
		err error
	)
	if s.Keeper != nil {
		c, err = s.Keeper.Fresh(r.Context(), id)
	} else {
		c, _, err = s.Conf.OpenCredential(r.Context(), id)
	}
	if err != nil {
		var dis *logins.DisabledError
		if errors.As(err, &dis) {
			writeJSON(w, http.StatusOK, TestResult{Error: err.Error()})
			return
		}
		confErr(w, err)
		return
	}
	unverified, err := kinds.CheckCredential(r.Context(), c)
	res := TestResult{OK: err == nil}
	if err != nil {
		res.Error = err.Error()
	}
	if unverified != "" {
		res.Warnings = []string{unverified}
	}
	if res.OK && c.Kind == providers.KindOAuth && !c.Expires.IsZero() {
		res.Warnings = append(res.Warnings, "login valid until "+c.Expires.Local().Format(time.RFC1123))
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) startSignIn(w http.ResponseWriter, r *http.Request) {
	if s.SignIns == nil {
		writeErr(w, http.StatusServiceUnavailable, "signing in is not wired")
		return
	}
	var body SignInRequest
	if !readJSON(w, r, &body) {
		return
	}
	p, err := s.SignIns.Start(r.Context(), auth.UserID(r.Context()), body.Provider)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) completeSignIn(w http.ResponseWriter, r *http.Request) {
	if s.SignIns == nil {
		writeErr(w, http.StatusServiceUnavailable, "signing in is not wired")
		return
	}
	var body SignInComplete
	if !readJSON(w, r, &body) {
		return
	}
	user := auth.UserID(r.Context())
	c, err := s.SignIns.Complete(r.Context(), user, r.PathValue("sid"), body.Pasted)
	if errors.Is(err, logins.ErrPending) {
		writeJSON(w, http.StatusAccepted, SignInResult{Pending: true})
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pc, err := s.Conf.CreateCredential(r.Context(), user, c, body.Name)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, SignInResult{Credential: &pc})
}

// authBroker serves a run's omp auth broker (kinds/ompbroker.go), under the
// run's broker token — derived per run from an instance secret, so it keeps
// working across engine restarts, and worthless for any other run.
func (s *Server) authBroker(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	h, release, err := s.AuthSink.RunAuthBroker(r.Context(), runID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	defer release()
	inner := r.Clone(r.Context())
	inner.URL.Path = strings.TrimPrefix(r.URL.Path, authsink.AuthBrokerPath(runID))
	inner.URL.RawPath = ""
	h.ServeHTTP(w, inner)
}

// authBrokerToken checks a broker request's bearer.
func (s *Server) authBrokerToken(w http.ResponseWriter, r *http.Request, runID string) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := s.AuthSink.BrokerToken(runID)
	if !ok || want == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		writeErr(w, http.StatusUnauthorized, "missing or invalid broker token")
		return false
	}
	return true
}

// authBrokerRoutes is the auth broker omp reads its logins from (kinds/ompbroker.go).
func (s *Server) authBrokerRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/runs/{id}/auth-broker/{rest...}", ID: "authBrokerGet", Tag: tagPings,
			Summary: "omp's auth broker for a run: snapshot, health",
			Doc:     "omp's auth-broker protocol, served per run (docs/model-providers-plan.md): access tokens only, the refresh sentinel in place of every refresh token.",
			Handler: s.authBroker, Auth: AccessBrokerToken},
		{Method: "POST", Path: "/api/runs/{id}/auth-broker/{rest...}", ID: "authBrokerPost", Tag: tagPings,
			Summary: "omp's auth broker for a run: refresh, acknowledgements",
			Handler: s.authBroker, Auth: AccessBrokerToken},
		{Method: "DELETE", Path: "/api/runs/{id}/auth-broker/{rest...}", ID: "authBrokerDelete", Tag: tagPings,
			Summary: "omp's auth broker for a run: acknowledgements",
			Handler: s.authBroker, Auth: AccessBrokerToken},
	}
}

// credentialRoutes is model provider credentials and sign-ins.
func (s *Server) credentialRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/providers", ID: "listProviders", Tag: tagIntegrations,
			Summary: "The model providers apothecary manages credentials for",
			Doc:     "The closed set: what each takes (an API key, a login) and which harnesses use it. harness narrows it to one harness kind.",
			Query:   []Param{{Name: "harness", Doc: "only the providers this harness kind uses"}},
			Handler: s.listProviders, Returns: []ProviderInfo{}, Enabled: haveConf},
		{Method: "GET", Path: "/api/credentials", ID: "listCredentials", Tag: tagIntegrations,
			Summary: "One's model provider credentials (metadata only, never a key or a token)",
			Handler: s.listCredentials, Returns: []confstore.ProviderCredential{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/credentials", ID: "createCredential", Tag: tagIntegrations,
			Summary: "Add an API key for a provider",
			Handler: s.createCredential, Body: CredentialRequest{}, Returns: confstore.ProviderCredential{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "POST", Path: "/api/credentials/import", ID: "importCredentials", Tag: tagIntegrations,
			Summary: "Import a credential file a harness CLI wrote",
			Doc:     "codex's or opencode's auth.json, CLIProxyAPI login JSON, or KEY=VALUE lines. Every entry that is not a provider apothecary manages is refused and listed.",
			Handler: s.importCredentials, Body: CredentialImportRequest{}, Returns: CredentialImportResult{},
			Status: http.StatusCreated, Enabled: haveConf},
		{Method: "POST", Path: "/api/credentials/signin", ID: "startSignIn", Tag: tagIntegrations,
			Summary: "Start signing in to a provider from apothecary",
			Doc:     "Returns the provider page to open and, for a device sign-in, the code to type there. The tokens come back to the engine, never to a laptop CLI.",
			Handler: s.startSignIn, Body: SignInRequest{}, Returns: logins.Pending{}, Enabled: haveConf},
		{Method: "POST", Path: "/api/credentials/signin/{sid}/complete", ID: "completeSignIn", Tag: tagIntegrations,
			Summary: "Finish a sign-in",
			Doc:     "A device sign-in answers 202 {pending: true} until the person approves; a paste sign-in exchanges what was pasted. 201 with the new credential when done.",
			Handler: s.completeSignIn, Body: SignInComplete{}, Returns: SignInResult{}, Status: http.StatusCreated, Enabled: haveConf},
		{Method: "GET", Path: "/api/credentials/{id}", ID: "getCredential", Tag: tagIntegrations,
			Summary: "One credential (metadata only)",
			Handler: s.getCredential, Returns: confstore.ProviderCredential{}, Enabled: haveConf, Scope: ScopeCredential},
		{Method: "PUT", Path: "/api/credentials/{id}", ID: "renameCredential", Tag: tagIntegrations,
			Summary: "Rename a credential",
			Handler: s.renameCredential, Body: CredentialRename{}, Returns: confstore.ProviderCredential{}, Enabled: haveConf, Scope: ScopeCredential},
		{Method: "DELETE", Path: "/api/credentials/{id}", ID: "deleteCredential", Tag: tagIntegrations,
			Summary: "Delete a credential no harness integration uses",
			Handler: s.deleteCredential, Status: http.StatusNoContent, Enabled: haveConf, Scope: ScopeCredential},
		{Method: "POST", Path: "/api/credentials/{id}/test", ID: "testCredential", Tag: tagIntegrations,
			Summary: "Check a credential against its provider, without spending tokens",
			Handler: s.testCredential, Returns: TestResult{}, Enabled: haveConf, Scope: ScopeCredential},
	}
}
