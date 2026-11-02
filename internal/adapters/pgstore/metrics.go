package pgstore

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// storeMetrics are the store's synchronous instruments, on the global meter
// (no-op while telemetry is off). The zero value records nothing, so a Store
// built without Open is safe.
type storeMetrics struct {
	append  metric.Float64Histogram
	rebuild metric.Float64Histogram
	// reg is the observable callback registerMetrics installed; Close drops it.
	reg metric.Registration
}

func newStoreMetrics() storeMetrics {
	m := otel.Meter("apothecaryd")
	var sm storeMetrics
	var err1, err2 error
	sm.append, err1 = m.Float64Histogram("apothecary.events.append.duration", metric.WithUnit("s"),
		metric.WithDescription("one append transaction: events plus their projections"))
	sm.rebuild, err2 = m.Float64Histogram("apothecary.projections.rebuild.duration", metric.WithUnit("s"),
		metric.WithDescription("a full projection rebuild from the event log"))
	if err := errors.Join(err1, err2); err != nil {
		slog.Default().Warn("pgstore metrics", "err", err)
		return storeMetrics{}
	}
	return sm
}

func (m storeMetrics) appendTook(start time.Time) {
	if m.append != nil {
		m.append.Record(context.Background(), time.Since(start).Seconds())
	}
}

func (m storeMetrics) rebuildTook(start time.Time) {
	if m.rebuild != nil {
		m.rebuild.Record(context.Background(), time.Since(start).Seconds())
	}
}

// registerMetrics reports the pool's occupancy and two operational counts
// the database already knows — live workspaces and denied egress — as
// observable instruments read at collection time.
func (s *Store) registerMetrics() {
	m := otel.Meter("apothecaryd")
	acquired, e1 := m.Int64ObservableGauge("apothecary.db.pool.acquired", metric.WithDescription("connections in use"))
	idle, e2 := m.Int64ObservableGauge("apothecary.db.pool.idle", metric.WithDescription("idle connections"))
	total, e3 := m.Int64ObservableGauge("apothecary.db.pool.total", metric.WithDescription("open connections"))
	acquires, e4 := m.Int64ObservableCounter("apothecary.db.pool.acquires", metric.WithDescription("connections acquired"))
	empty, e5 := m.Int64ObservableCounter("apothecary.db.pool.empty_acquires",
		metric.WithDescription("acquires that had to wait for a connection"))
	wait, e6 := m.Float64ObservableCounter("apothecary.db.pool.acquire_wait", metric.WithUnit("s"),
		metric.WithDescription("total time spent waiting to acquire a connection"))
	live, e7 := m.Int64ObservableGauge("apothecary.workspaces.live", metric.WithDescription("runs holding a workspace"))
	denied, e8 := m.Int64ObservableCounter("apothecary.egress.denied", metric.WithDescription("egress connections denied"))
	if err := errors.Join(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
		slog.Default().Warn("pgstore metrics", "err", err)
		return
	}
	reg, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		st := s.pool.Stat()
		o.ObserveInt64(acquired, int64(st.AcquiredConns()))
		o.ObserveInt64(idle, int64(st.IdleConns()))
		o.ObserveInt64(total, int64(st.TotalConns()))
		o.ObserveInt64(acquires, st.AcquireCount())
		o.ObserveInt64(empty, st.EmptyAcquireCount())
		o.ObserveFloat64(wait, st.AcquireDuration().Seconds())
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var nLive, nDenied int64
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE env_id <> ''`).Scan(&nLive); err != nil {
			return err
		}
		o.ObserveInt64(live, nLive)
		if err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(count),0) FROM egress_audit WHERE verdict='deny'`).Scan(&nDenied); err != nil {
			return err
		}
		o.ObserveInt64(denied, nDenied)
		return nil
	}, acquired, idle, total, acquires, empty, wait, live, denied)
	if err != nil {
		slog.Default().Warn("pgstore metrics", "err", err)
		return
	}
	s.metrics.reg = reg
}
