// Package authsink keeps live workspaces' harness logins current: it pushes
// a refreshed login into every workspace whose run uses that credential, and
// serves omp's per-run auth broker. It holds which run's workspace has which
// credentials (registered by the engine as a run's workspace comes up), so
// the keeper and the API reach them without the engine.
package authsink

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"

	"apothecary/internal/ports/envport"
	"apothecary/internal/redact"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
)

// Sink tracks live runs' workspaces and serves their auth brokers.
type Sink struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	// Log is nil for slog.Default.
	Log *slog.Logger

	// brokerKey is the instance secret per-run broker tokens derive from
	// (confstore.Service.InstanceSecret). Without one, omp's logins are off.
	brokerKey []byte

	mu    sync.Mutex
	sinks map[string]*authSink
}

func New(runs *run.Reader, resolver resolution.Resolver, brokerKey []byte, log *slog.Logger) *Sink {
	return &Sink{Runs: runs, Resolver: resolver, Log: log, brokerKey: append([]byte(nil), brokerKey...), sinks: map[string]*authSink{}}
}

func (s *Sink) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Provider logins, workspace side (docs/model-providers-plan.md). The keeper
// (internal/account/logins) refreshes a login before it expires; this package is
// what reaches the live workspaces about it:
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
	env    envport.EnvID
	redact *redact.Redactor
}

// Set records that runID's workspace env holds its harnesses'
// credentials (bootstrap, re-entry).
func (s *Sink) Set(runID string, res *resolution.Resolution, env envport.EnvID, red *redact.Redactor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sinks == nil {
		s.sinks = map[string]*authSink{}
	}
	s.sinks[runID] = &authSink{res: res, env: env, redact: red}
}

// Drop forgets runID's workspace (its loop ended).
func (s *Sink) Drop(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sinks, runID)
}

func (s *Sink) get(runID string) *authSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sinks[runID]
}

// Push rewrites the harness auth of every live workspace whose run
// uses credentialID (the keeper's OnRefresh). Scoped by credential id —
// never by provider or harness kind, which would hand one person's token to
// another's run. Best-effort per workspace: a failure is logged, and the
// workspace keeps its current token until it expires.
func (s *Sink) Push(ctx context.Context, credentialID string) {
	s.mu.Lock()
	var targets []struct {
		runID string
		sink  *authSink
	}
	for runID, sk := range s.sinks {
		if slices.Contains(sk.res.CredentialIDs, credentialID) {
			targets = append(targets, struct {
				runID string
				sink  *authSink
			}{runID, sk})
		}
	}
	s.mu.Unlock()
	for _, t := range targets {
		if t.sink.res.SeedAuth == nil {
			continue
		}
		if err := t.sink.res.SeedAuth(ctx, t.sink.env, func(v string) { t.sink.redact.Add(v, "harness-auth") }); err != nil {
			s.log().Warn("could not push a refreshed login into a live workspace", "run", t.runID, "env", t.sink.env, "credential", credentialID, "err", err)
			continue
		}
		s.log().Info("pushed a refreshed login into a live workspace", "run", t.runID, "env", t.sink.env, "credential", credentialID)
	}
}

// BrokerToken is the bearer omp presents to runID's auth broker: stable for
// the run across engine restarts, worthless for any other run.
func (s *Sink) BrokerToken(runID string) string {
	if len(s.brokerKey) == 0 || runID == "" {
		return ""
	}
	m := hmac.New(sha256.New, s.brokerKey)
	m.Write([]byte("auth-broker\x00" + runID))
	return hex.EncodeToString(m.Sum(nil))
}

// AuthBrokerPath is where runID's broker is served, under the engine URL.
func AuthBrokerPath(runID string) string { return "/api/runs/" + runID + "/auth-broker" }

// ProviderEnv is res.ProviderEnv plus, when a harness reads its logins
// from the broker, where that broker is and the token it takes.
func (s *Sink) ProviderEnv(res *resolution.Resolution, runID, engineURL string) map[string]string {
	if res.AuthBroker == nil || s.BrokerToken(runID) == "" {
		return res.ProviderEnv
	}
	out := maps.Clone(res.ProviderEnv)
	if out == nil {
		out = map[string]string{}
	}
	out["OMP_AUTH_BROKER_URL"] = engineURL + AuthBrokerPath(runID)
	out["OMP_AUTH_BROKER_TOKEN"] = s.BrokerToken(runID)
	return out
}

// RunAuthBroker is runID's broker: its project's harness logins, every token
// registered with the live run's redactor before it is served. release must
// be called when the request is done. A run that has ended gets none — its
// workspace has no business with live credentials.
func (s *Sink) RunAuthBroker(ctx context.Context, runID string) (http.Handler, func(), error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	if v.State.Terminal() {
		return nil, nil, fmt.Errorf("run %s has ended", runID)
	}
	res, err := resolution.ForRun(ctx, s.Resolver, v)
	if err != nil {
		return nil, nil, err
	}
	if res.AuthBroker == nil {
		res.Close()
		return nil, nil, fmt.Errorf("run %s reads no login from the broker", runID)
	}
	register := func(string) {}
	if sk := s.get(runID); sk != nil {
		register = func(v string) { sk.redact.Add(v, "harness-auth") }
	}
	return res.AuthBroker(register), res.Close, nil
}
