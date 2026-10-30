// Package egress is the live control of a run's network egress and its
// durable audit trail: what the per-project gateway blocked, the run page's
// "allow this host" and rule toggles (hot-reloaded, no restart), and the
// ingest of the gateway's audit into the store before anyone reads it. None of
// it is a step of a run's flow, so it lives beside the engine, not in it.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/wsname"
)

// ErrOff marks an egress request for a run with no live gateway (not live,
// or a provisioner without one).
var ErrOff = errors.New("egress proxy mode is off")

// AuditStore persists and queries the durable egress audit trail (what
// each workspace reached / was blocked from). nil = audit off. Implemented by
// *pgstore.Store.
type AuditStore interface {
	UpsertEgressAudit(ctx context.Context, rows []egressport.EgressAuditRow) error
	QueryEgressAudit(ctx context.Context, f egressport.EgressAuditFilter) ([]egressport.EgressAuditRow, error)
}

// Service controls and audits egress through each project's resolved
// provisioner.
type Service struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	// Audit is the durable trail; nil = audit off.
	Audit AuditStore
	// ActiveRuns lists the runs whose audit the ingest pulls (the engine's
	// live loops); nil = none.
	ActiveRuns func() []string
	// Log is nil for slog.Default.
	Log *slog.Logger
}

func New(runs *run.Reader, resolver resolution.Resolver, audit AuditStore) *Service {
	return &Service{Runs: runs, Resolver: resolver, Audit: audit}
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run ingests the audit every interval until ctx ends (no eager pass: there
// is nothing to ingest at boot that the first tick will not find).
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := s.IngestOnce(ctx); err != nil && ctx.Err() == nil {
				s.log().Error("egress audit ingest failed", "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// Forget is part of the engine's teardown hook: egress keeps no per-run
// memory.
func (s *Service) Forget(string) {}

// BeforeDestroy flushes the run's audit one last time before its workspace
// (and possibly the project gateway) is torn down, so the trail is durable
// even for the last activity.
func (s *Service) BeforeDestroy(ctx context.Context, v run.View, res *resolution.Resolution) {
	if s.Audit == nil {
		return
	}
	if rows := auditRows(ctx, v, res.Envp); len(rows) > 0 {
		_ = s.Audit.UpsertEgressAudit(ctx, rows)
	}
}

// ctrl resolves a run to its provisioner's egress controller (the
// per-project gateway), or ErrOff when the run isn't live or the
// provisioner has no gateway.
func (s *Service) ctrl(ctx context.Context, runID string) (egressport.EgressController, envport.EnvID, func(), error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, "", nil, err
	}
	if v.ProjectID == "" {
		return nil, "", nil, fmt.Errorf("run %s: %w", runID, ErrOff)
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, "", nil, err
	}
	ctrl, ok := res.Envp.(egressport.EgressController)
	if !ok {
		res.Close()
		return nil, "", nil, ErrOff
	}
	// Works during provisioning too (EnvID not yet set): the most important time
	// to "add to egress" is when the build is blocked on a denied domain.
	return ctrl, envport.EnvID(wsname.ForRun(v, runID)), res.Close, nil
}

// Denials lists a run's gateway-blocked hosts (run page feedback), each
// classified against the policy in force now: held (nobody ruled on it yet —
// the run page asks) or blocked (reviewed — it never asks again). Without a
// readable policy every denial stays held: asking twice beats never asking.
func (s *Service) Denials(ctx context.Context, runID string) []egressport.EgressDenial {
	ctrl, env, release, err := s.ctrl(ctx, runID)
	if err != nil {
		return nil
	}
	defer release()
	dens := ctrl.EgressDenials(ctx, env)
	pol, perr := ctrl.EgressPolicy(ctx, env)
	for i := range dens {
		dens[i].Status = egressport.EgressHeld
		if perr == nil {
			dens[i].Status = pol.Status(dens[i].Host)
		}
	}
	return dens
}

// Allow adds a host to a run's project egress policy live — the gateway
// hot-reloads it, so the next request succeeds with no restart. Project-wide by
// design. Returns ErrOff when the run isn't live / has no gateway.
// Persistence to the project (future runs) is the caller's job.
func (s *Service) Allow(ctx context.Context, runID, host string) error {
	ctrl, env, release, err := s.ctrl(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	return ctrl.AllowEgress(ctx, env, host)
}

// SetRule live-toggles a host's egress rule for a run (allow or force-deny),
// project-wide via the gateway, no restart. Returns ErrOff when the run has
// no gateway. Persistence to the project is the caller's job.
func (s *Service) SetRule(ctx context.Context, runID, host string, allow bool) error {
	ctrl, env, release, err := s.ctrl(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	return ctrl.SetEgressRule(ctx, env, host, allow)
}

// Policy returns the egress policy the gateway enforces for a run
// RIGHT NOW — the run page renders each host's current rule from it (the
// audit trail only records past verdicts, which never change when the rule
// does). Returns ErrOff when the run has no live gateway.
func (s *Service) Policy(ctx context.Context, runID string) (egressport.EgressPolicy, error) {
	ctrl, env, release, err := s.ctrl(ctx, runID)
	if err != nil {
		return egressport.EgressPolicy{}, err
	}
	defer release()
	return ctrl.EgressPolicy(ctx, env)
}

// auditRows converts a run's provisioner audit records into durable rows tagged
// with the project + engine run id. Empty when the provisioner has no auditor.
// Uses wsname.ForRun so it works DURING provisioning too (EnvID not yet set).
func auditRows(ctx context.Context, v run.View, envp envport.EnvProvisioner) []egressport.EgressAuditRow {
	auditor, ok := envp.(egressport.EgressAuditor)
	if !ok {
		return nil
	}
	recs := auditor.EgressAudit(ctx, envport.EnvID(wsname.ForRun(v, v.RunID)))
	rows := make([]egressport.EgressAuditRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, egressport.EgressAuditRow{
			Project: v.ProjectID, Run: v.RunID, Host: r.Host, DstIP: r.DstIP, Port: r.Port,
			Proto: r.Proto, Verdict: r.Verdict, Count: r.Count, BytesOut: r.BytesOut,
			BytesIn: r.BytesIn, First: r.First, Last: r.Last,
		})
	}
	return rows
}

// IngestOnce pulls the aggregated egress audit of every ACTIVE run
// (ActiveRuns: the engine's live loops — cheap, no full-history scan) and upserts it
// into durable storage. This is the SINGLE audit path: the gateway writes a file
// (the only channel out of the container), the engine ingests it to the DB, and
// the UI reads the DB — so every audit fact is PERSISTED before it is delivered.
// Run on a fast ticker for near-live freshness, and once more before env destroy.
func (s *Service) IngestOnce(ctx context.Context) error {
	if s.Audit == nil {
		return nil
	}
	var ids []string
	if s.ActiveRuns != nil {
		ids = s.ActiveRuns()
	}

	var rows []egressport.EgressAuditRow
	for _, id := range ids {
		v, err := s.Runs.View(ctx, id)
		if err != nil {
			continue
		}
		if v.ProjectID == "" {
			continue
		}
		res, err := s.Resolver.Resolve(ctx, v.ProjectID)
		if err != nil {
			continue
		}
		rows = append(rows, auditRows(ctx, v, res.Envp)...)
		res.Close()
	}
	if len(rows) == 0 {
		return nil
	}
	return s.Audit.UpsertEgressAudit(ctx, rows)
}

// RunAudit returns a run's egress audit from the durable store — the
// single source of truth, kept near-live by IngestOnce. Filterable.
func (s *Service) RunAudit(ctx context.Context, runID, verdict, host string, limit int) ([]egressport.EgressAuditRow, error) {
	if s.Audit == nil {
		return nil, nil
	}
	return s.Audit.QueryEgressAudit(ctx, egressport.EgressAuditFilter{Run: runID, Verdict: verdict, Host: host, Limit: limit})
}

// ProjectAudit returns the durable audit rows for a whole project (filterable).
func (s *Service) ProjectAudit(ctx context.Context, projectID, verdict, host string, limit int) ([]egressport.EgressAuditRow, error) {
	if s.Audit == nil {
		return nil, nil
	}
	return s.Audit.QueryEgressAudit(ctx, egressport.EgressAuditFilter{Project: projectID, Verdict: verdict, Host: host, Limit: limit})
}
