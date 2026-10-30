package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"apothecary/internal/ports"
	"apothecary/internal/redact"
	"apothecary/internal/resolution"
)

// Provider logins, engine side (docs/model-providers-plan.md). The keeper
// (internal/logins) refreshes a login before it expires; this file is what
// the engine does about it:
//
//   - push: every live workspace whose run uses the refreshed credential gets
//     its harness files rewritten with the new access token, the token
//     registered with that run's redactor first. Workspaces never hold a
//     refresh token, so this — not a local refresh — is how a long session
//     keeps working.
//   - the auth broker: omp reads its logins from the engine over omp's own
//     broker protocol, under a per-run token derived from an instance secret
//     so it survives engine restarts (run tokens do not).

// authSink is a live run's workspace, as a refreshed login reaches it.
type authSink struct {
	res    *resolution.Resolution
	env    ports.EnvID
	redact *redact.Redactor
}

// setAuthSink records that runID's workspace env holds its harnesses'
// credentials (bootstrap, re-entry).
func (e *Engine) setAuthSink(runID string, res *resolution.Resolution, env ports.EnvID, redact *redact.Redactor) {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	if e.sinks == nil {
		e.sinks = map[string]*authSink{}
	}
	e.sinks[runID] = &authSink{res: res, env: env, redact: redact}
}

func (e *Engine) dropAuthSink(runID string) {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	delete(e.sinks, runID)
}

func (e *Engine) authSink(runID string) *authSink {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	return e.sinks[runID]
}

// PushCredential rewrites the harness auth of every live workspace whose run
// uses credentialID (the keeper's OnRefresh). Scoped by credential id —
// never by provider or harness kind, which would hand one person's token to
// another's run. Best-effort per workspace: a failure is logged, and the
// workspace keeps its current token until it expires.
func (e *Engine) PushCredential(ctx context.Context, credentialID string) {
	e.sinkMu.Lock()
	var targets []struct {
		runID string
		sink  *authSink
	}
	for runID, s := range e.sinks {
		if slices.Contains(s.res.CredentialIDs, credentialID) {
			targets = append(targets, struct {
				runID string
				sink  *authSink
			}{runID, s})
		}
	}
	e.sinkMu.Unlock()
	for _, t := range targets {
		if t.sink.res.SeedAuth == nil {
			continue
		}
		if err := t.sink.res.SeedAuth(ctx, t.sink.env, func(s string) { t.sink.redact.Add(s, "harness-auth") }); err != nil {
			e.log.Warn("could not push a refreshed login into a live workspace", "run", t.runID, "env", t.sink.env, "credential", credentialID, "err", err)
			continue
		}
		e.log.Info("pushed a refreshed login into a live workspace", "run", t.runID, "env", t.sink.env, "credential", credentialID)
	}
}

// SetAuthBrokerKey sets the instance secret per-run broker tokens derive from
// (confstore.Service.InstanceSecret). Without one, omp's logins are off.
func (e *Engine) SetAuthBrokerKey(key []byte) { e.brokerKey = append([]byte(nil), key...) }

// BrokerToken is the bearer omp presents to runID's auth broker: stable for
// the run across engine restarts, worthless for any other run.
func (e *Engine) BrokerToken(runID string) string {
	if len(e.brokerKey) == 0 || runID == "" {
		return ""
	}
	m := hmac.New(sha256.New, e.brokerKey)
	m.Write([]byte("auth-broker\x00" + runID))
	return hex.EncodeToString(m.Sum(nil))
}

// AuthBrokerPath is where runID's broker is served, under the engine URL.
func AuthBrokerPath(runID string) string { return "/api/runs/" + runID + "/auth-broker" }

// providerEnvFor is res.ProviderEnv plus, when a harness reads its logins
// from the broker, where that broker is and the token it takes.
func (e *Engine) providerEnvFor(res *resolution.Resolution, runID, engineURL string) map[string]string {
	if res.AuthBroker == nil || e.BrokerToken(runID) == "" {
		return res.ProviderEnv
	}
	out := maps.Clone(res.ProviderEnv)
	if out == nil {
		out = map[string]string{}
	}
	out["OMP_AUTH_BROKER_URL"] = engineURL + AuthBrokerPath(runID)
	out["OMP_AUTH_BROKER_TOKEN"] = e.BrokerToken(runID)
	return out
}

// RunAuthBroker is runID's broker: its project's harness logins, every token
// registered with the live run's redactor before it is served. release must
// be called when the request is done. A run that has ended gets none — its
// workspace has no business with live credentials.
func (e *Engine) RunAuthBroker(ctx context.Context, runID string) (http.Handler, func(), error) {
	v, err := e.runView(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	if v.State.Terminal() {
		return nil, nil, fmt.Errorf("run %s has ended", runID)
	}
	res, err := e.resolveForRun(ctx, v)
	if err != nil {
		return nil, nil, err
	}
	if res.AuthBroker == nil {
		res.Close()
		return nil, nil, fmt.Errorf("run %s reads no login from the broker", runID)
	}
	register := func(string) {}
	if s := e.authSink(runID); s != nil {
		register = func(v string) { s.redact.Add(v, "harness-auth") }
	}
	return res.AuthBroker(register), res.Close, nil
}
