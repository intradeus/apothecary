package main

import (
	"context"
	"time"

	"apothecary/internal/automation"
	"apothecary/internal/confstore"
	"apothecary/internal/logins"
	"apothecary/internal/tickets"
)

// background starts the daemon's loops — reaper, login keeper, workspace
// reconciler, resource sampler, egress audit ingest, automations — all on
// ctx, which serve cancels before it shuts the engine down.
func (a *app) background(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				// Settings are per user: each owner's windows apply to their
				// runs and uploads. One read per owner per pass, then every
				// decision looks its owner up (defaults for an unknown one).
				owners, err := a.store.SettingsOwners(ctx)
				if err != nil {
					a.log.Error("reaper: owners read failed", "err", err)
					continue
				}
				settings := map[string]confstore.Settings{}
				for _, uid := range owners {
					set, err := a.conf.Settings(ctx, uid)
					if err != nil {
						a.log.Error("reaper: settings read failed", "user", uid, "err", err)
						set = confstore.DefaultSettings()
					}
					settings[uid] = set
				}
				settingsOf := func(uid string) confstore.Settings {
					if set, ok := settings[uid]; ok {
						return set
					}
					return confstore.DefaultSettings()
				}
				if err := a.e.ReapOnce(ctx, func(uid string) time.Duration {
					return settingsOf(uid).CompletedWindow()
				}); err != nil {
					a.log.Error("reaper pass failed", "err", err)
				}
				// Uploads never claimed by a ticket/run/answer age out
				// (retention.orphan_uploads_after; rows only — bytes go in
				// the blob sweep below).
				for _, uid := range owners {
					if n, err := a.attachments.GCOrphans(ctx, uid, settingsOf(uid).OrphanWindow()); err != nil {
						a.log.Error("reaper: upload GC failed", "user", uid, "err", err)
					} else if n > 0 {
						a.log.Info("reaper: swept unclaimed uploads", "user", uid, "count", n)
					}
				}
				// Blob sweep: delete bytes no live row references. The mark
				// set honors each owner's snapshot content retention — rows
				// older than their retention.snapshots_after stop pinning
				// blobs while the rows/events keep the record (DESIGN-FILES §6).
				cutoffs := map[string]time.Time{}
				for _, uid := range owners {
					if window, ok := settingsOf(uid).SnapshotWindow(); ok {
						cutoffs[uid] = time.Now().Add(-window)
					}
				}
				live, err := a.store.LiveBlobSHAs(ctx, cutoffs)
				if err != nil {
					a.log.Error("reaper: live blob set failed", "err", err)
				} else if n, err := a.blobs.Sweep(live); err != nil {
					a.log.Error("reaper: blob sweep failed", "err", err)
				} else if n > 0 {
					a.log.Info("reaper: swept unreferenced blobs", "count", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// The login keeper: renews every provider login before it expires, idle
	// or not, and pushes each renewal into the live workspaces using it.
	go a.keeper.Run(ctx, logins.DefaultEvery)

	// Live-workspace reconciler: refreshes each run's real container status
	// (running/stopped/removed) so the workspace badge reflects reality, not
	// just the event-sourced env_id. Runs far more often than the reaper —
	// its whole job is catching out-of-band container changes promptly. An
	// eager first pass populates the badge without waiting a full interval.
	go a.envSt.Run(ctx, 30*time.Second)

	// Resource sampler: one docker-stats reading of every active run's sandbox
	// per pass, feeding the run-page cpu/ram graphs. Same honesty rule as
	// the reconciler: a failed sample is a gap in the series, never a
	// carried-forward value.
	go a.sampler.Run(ctx, 10*time.Second)

	// Egress audit ingest: persist each ACTIVE run's audit trail (from its
	// per-project gateway) into the DB near-live, so the trail is durable BEFORE
	// the UI reads it (the single audit path: gateway file → engine → DB → UI).
	// Cheap — only active runs, not full history — so it can run often.
	go a.egressSvc.Run(ctx, 5*time.Second)

	// Automations: tracker queries polled into tickets and runs, schedules
	// firing runs — the ways a run begins without a person (flow v4). A
	// pass walks every enabled automation; each one's failure is recorded
	// on it, never on the pass.
	watcher := &automation.Watcher{
		Runs: a.e, MRs: a.mrSvc, Conf: a.conf, Notify: a.chreg, Log: a.log,
		Tickets: &tickets.Factory{Conf: a.conf, Attachments: a.attachments, Sources: a.resolver, Log: a.log},
	}
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := watcher.RunOnce(ctx); err != nil && ctx.Err() == nil {
					a.log.Error("automation pass failed", "err", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

}
