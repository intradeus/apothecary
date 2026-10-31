package pgstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/ports/egressport"
)

// UpsertEgressAudit persists aggregated audit rows. Counts/bytes are cumulative
// within a gateway lifetime, so conflicts take GREATEST (monotonic) — a re-ingest
// of the same live gateway never double-counts, and last/first seen widen.
func (s *Store) UpsertEgressAudit(ctx context.Context, rows []egressport.EgressAuditRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO egress_audit
			  (project, run, host, dst_ip, port, proto, verdict, count, bytes_out, bytes_in, first_seen, last_seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (run, host, port, verdict) DO UPDATE SET
			  project    = EXCLUDED.project,
			  dst_ip     = EXCLUDED.dst_ip,
			  proto      = EXCLUDED.proto,
			  count      = GREATEST(egress_audit.count, EXCLUDED.count),
			  bytes_out  = GREATEST(egress_audit.bytes_out, EXCLUDED.bytes_out),
			  bytes_in   = GREATEST(egress_audit.bytes_in, EXCLUDED.bytes_in),
			  first_seen = LEAST(egress_audit.first_seen, EXCLUDED.first_seen),
			  last_seen  = GREATEST(egress_audit.last_seen, EXCLUDED.last_seen)`,
			r.Project, r.Run, r.Host, r.DstIP, r.Port, r.Proto, r.Verdict,
			r.Count, r.BytesOut, r.BytesIn, r.First, r.Last)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert egress_audit: %w", err)
		}
	}
	return nil
}

// QueryEgressAudit returns audit rows matching the filter, newest activity first.
func (s *Store) QueryEgressAudit(ctx context.Context, f egressport.EgressAuditFilter) ([]egressport.EgressAuditRow, error) {
	var where []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Run != "" {
		add("run = $%d", f.Run)
	}
	if f.Project != "" {
		add("project = $%d", f.Project)
	}
	if f.Verdict != "" {
		add("verdict = $%d", f.Verdict)
	}
	if f.Host != "" {
		add("host ILIKE '%%' || $%d || '%%'", f.Host)
	}
	q := `SELECT project, run, host, dst_ip, port, proto, verdict, count, bytes_out, bytes_in, first_seen, last_seen FROM egress_audit`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY last_seen DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []egressport.EgressAuditRow
	for rows.Next() {
		var r egressport.EgressAuditRow
		if err := rows.Scan(&r.Project, &r.Run, &r.Host, &r.DstIP, &r.Port, &r.Proto, &r.Verdict,
			&r.Count, &r.BytesOut, &r.BytesIn, &r.First, &r.Last); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
