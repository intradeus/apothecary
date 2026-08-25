package confstore

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Settings are the policy plane (DESIGN2 §6.4): stored as one row per key,
// read on every admission decision and reaper pass — no cache, no watch.
type Settings struct {
	MaxConcurrentRuns     int    `json:"max_concurrent_runs"`
	MaxEnvironments       int    `json:"max_environments"`
	DestroyCompletedAfter string `json:"destroy_completed_after"` // duration string
	DestroyAbandoned      string `json:"destroy_abandoned"`       // only "never"
	// OrphanUploadsAfter is how long a never-claimed upload survives before
	// the GC removes it (DESIGN-FILES §6; replaces a hardcoded 24h).
	OrphanUploadsAfter string `json:"orphan_uploads_after"` // duration string
	// SnapshotsAfter is the snapshot CONTENT retention: "never" (the
	// default — today's keep-forever posture as a stated policy) or a
	// duration after which snapshot blobs are swept. Rows and events keep
	// sha/size/name forever — a swept snapshot still shows WHAT an attempt
	// produced, just not the bytes.
	SnapshotsAfter string `json:"snapshots_after"` // duration string or "never"
}

const (
	keyMaxConcurrent   = "limits.max_concurrent_runs"
	keyMaxEnvs         = "limits.max_environments"
	keyDestroyAfter    = "reaper.destroy_completed_after"
	keyDestroyAbandond = "reaper.destroy_abandoned"
	keyOrphanUploads   = "retention.orphan_uploads_after"
	keySnapshotsAfter  = "retention.snapshots_after"
)

// DefaultSettings mirror the v1 config defaults.
func DefaultSettings() Settings {
	return Settings{
		MaxConcurrentRuns:     3,
		MaxEnvironments:       6,
		DestroyCompletedAfter: "48h",
		DestroyAbandoned:      "never",
		OrphanUploadsAfter:    "24h",
		SnapshotsAfter:        "never",
	}
}

// CompletedWindow parses the reaper window (validated on write).
func (s Settings) CompletedWindow() time.Duration {
	d, err := time.ParseDuration(s.DestroyCompletedAfter)
	if err != nil || d <= 0 {
		return 48 * time.Hour
	}
	return d
}

// OrphanWindow parses the unclaimed-upload GC window.
func (s Settings) OrphanWindow() time.Duration {
	d, err := time.ParseDuration(s.OrphanUploadsAfter)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	return d
}

// SnapshotWindow parses the snapshot content retention: (0, false) = keep
// forever ("never").
func (s Settings) SnapshotWindow() (time.Duration, bool) {
	if s.SnapshotsAfter == "" || s.SnapshotsAfter == "never" {
		return 0, false
	}
	d, err := time.ParseDuration(s.SnapshotsAfter)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// Validate applies the v1 config rules (§6.4).
func (s Settings) Validate() error {
	if s.MaxConcurrentRuns < 1 {
		return invalidf("settings: max_concurrent_runs must be >= 1")
	}
	if s.MaxEnvironments < 1 {
		return invalidf("settings: max_environments must be >= 1")
	}
	if d, err := time.ParseDuration(s.DestroyCompletedAfter); err != nil || d <= 0 {
		return invalidf("settings: destroy_completed_after: invalid duration %q", s.DestroyCompletedAfter)
	}
	if s.DestroyAbandoned != "never" {
		return invalidf(`settings: destroy_abandoned must be "never", got %q`, s.DestroyAbandoned)
	}
	if d, err := time.ParseDuration(s.OrphanUploadsAfter); err != nil || d <= 0 {
		return invalidf("settings: orphan_uploads_after: invalid duration %q", s.OrphanUploadsAfter)
	}
	if s.SnapshotsAfter != "never" {
		if d, err := time.ParseDuration(s.SnapshotsAfter); err != nil || d <= 0 {
			return invalidf(`settings: snapshots_after must be "never" or a positive duration, got %q`, s.SnapshotsAfter)
		}
	}
	return nil
}

// Settings reads the settings rows, filling defaults for absent keys.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	rows, err := s.db.GetSettings(ctx)
	if err != nil {
		return DefaultSettings(), err
	}
	out := DefaultSettings()
	assign := func(key string, into any) {
		if raw, ok := rows[key]; ok {
			if err := json.Unmarshal([]byte(raw), into); err != nil {
				slog.Warn("settings: corrupt stored value, using default", "key", key, "err", err)
			}
		}
	}
	assign(keyMaxConcurrent, &out.MaxConcurrentRuns)
	assign(keyMaxEnvs, &out.MaxEnvironments)
	assign(keyDestroyAfter, &out.DestroyCompletedAfter)
	assign(keyDestroyAbandond, &out.DestroyAbandoned)
	assign(keyOrphanUploads, &out.OrphanUploadsAfter)
	assign(keySnapshotsAfter, &out.SnapshotsAfter)
	// An older UI PUTting a partial Settings body must not brick the row.
	if out.OrphanUploadsAfter == "" {
		out.OrphanUploadsAfter = "24h"
	}
	if out.SnapshotsAfter == "" {
		out.SnapshotsAfter = "never"
	}
	return out, nil
}

// PutSettings validates and persists every key.
func (s *Service) PutSettings(ctx context.Context, set Settings) error {
	if err := set.Validate(); err != nil {
		return err
	}
	pairs := []struct {
		key string
		val any
	}{
		{keyMaxConcurrent, set.MaxConcurrentRuns},
		{keyMaxEnvs, set.MaxEnvironments},
		{keyDestroyAfter, set.DestroyCompletedAfter},
		{keyDestroyAbandond, set.DestroyAbandoned},
		{keyOrphanUploads, set.OrphanUploadsAfter},
		{keySnapshotsAfter, set.SnapshotsAfter},
	}
	for _, p := range pairs {
		raw, err := json.Marshal(p.val)
		if err != nil {
			return err
		}
		if err := s.db.PutSetting(ctx, p.key, string(raw)); err != nil {
			return err
		}
	}
	return nil
}
