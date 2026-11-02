package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"apothecary/internal/analytics"
)

// The analytics.Store: the compiled queries over the fact tables, and the
// person's dashboards and scorecards (configuration, kept across rebuilds).

// analyticsBegin opens an analytics query's transaction: read-only, under a
// 5s statement timeout — a query is a person's chart, and one too broad
// must not hold a connection for minutes. One round trip: the timeout is
// part of the BEGIN.
var analyticsBegin = pgx.TxOptions{BeginQuery: `BEGIN READ ONLY; SET LOCAL statement_timeout = '5s'`}

// analyticsQuery runs one compiled query in its own analytics transaction,
// scanning each row into scans and handing it to fn. A cancelled statement
// is analytics.ErrTimeout.
func (s *Store) analyticsQuery(ctx context.Context, c analytics.Compiled, scans []any, fn func() error) error {
	tx, err := s.pool.BeginTx(ctx, analyticsBegin)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A failed Query hands back rows carrying its error: ForEachRow reports
	// it, like a failed scan.
	rows, _ := tx.Query(ctx, c.SQL, c.Args...)
	_, err = pgx.ForEachRow(rows, scans, fn)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "57014" {
		return fmt.Errorf("%w: %v", analytics.ErrTimeout, err)
	}
	return err
}

func (s *Store) RunQuery(ctx context.Context, c analytics.Compiled) ([]analytics.Row, error) {
	var out []analytics.Row
	dims := make([]*string, c.Dims)
	vals := make([]*float64, c.Measures)
	var n int64
	scans := make([]any, 0, c.Dims+c.Measures+1)
	for i := range dims {
		scans = append(scans, &dims[i])
	}
	for i := range vals {
		scans = append(scans, &vals[i])
	}
	scans = append(scans, &n)
	err := s.analyticsQuery(ctx, c, scans, func() error {
		r := analytics.Row{Dims: make([]string, c.Dims), Values: make([]*float64, c.Measures), N: n}
		for i, d := range dims {
			if d != nil {
				r.Dims[i] = *d
			}
		}
		for i, v := range vals {
			if v != nil {
				r.Values[i] = new(*v)
			}
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

func (s *Store) Drill(ctx context.Context, c analytics.Compiled) ([]analytics.DrillRun, error) {
	var out []analytics.DrillRun
	var d analytics.DrillRun
	err := s.analyticsQuery(ctx, c, []any{&d.RunID, &d.Title, &d.FlowName, &d.FlowHash, &d.State, &d.CreatedAt, &d.WallMS, &d.CostUSD},
		func() error { out = append(out, d); return nil })
	return out, err
}

func (s *Store) Values(ctx context.Context, c analytics.Compiled) ([]string, error) {
	var out []string
	var v string
	err := s.analyticsQuery(ctx, c, []any{&v}, func() error { out = append(out, v); return nil })
	return out, err
}

// FlowVersionLabels numbers each flow's versions within its project by
// creation, over the user's projects only.
func (s *Store) FlowVersionLabels(ctx context.Context, userID string, hashes []string) (map[string]string, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT hash, name || ' v' || n FROM (
			SELECT v.hash, v.name, row_number() OVER (PARTITION BY v.project_id, v.name ORDER BY v.created_at, v.hash) n
			FROM flow_versions v JOIN projects p ON p.id = v.project_id AND p.user_id = $1) x
		WHERE hash = ANY($2)`, userID, hashes)
	return collectPairs(rows)
}

func (s *Store) ProjectNames(ctx context.Context, userID string, ids []string) (map[string]string, error) {
	rows, _ := s.pool.Query(ctx, `SELECT id, name FROM projects WHERE user_id = $1 AND id = ANY($2)`, userID, ids)
	return collectPairs(rows)
}

// collectPairs maps a two-column result; rows from a failed Query carry its
// error.
func collectPairs(rows pgx.Rows) (map[string]string, error) {
	out := map[string]string{}
	var k, v string
	if _, err := pgx.ForEachRow(rows, []any{&k, &v}, func() error { out[k] = v; return nil }); err != nil {
		return nil, err
	}
	return out, nil
}

// --- dashboards ---------------------------------------------------------------

const dashboardCols = `id, user_id, name, filters, panels, created_at, updated_at`

func scanDashboard(row pgx.Row) (analytics.Dashboard, error) {
	var d analytics.Dashboard
	var filters, panels []byte
	if err := row.Scan(&d.ID, &d.UserID, &d.Name, &filters, &panels, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return d, err
	}
	if err := json.Unmarshal(filters, &d.Filters); err != nil {
		return d, fmt.Errorf("dashboard %s filters: %w", d.ID, err)
	}
	if err := json.Unmarshal(panels, &d.Panels); err != nil {
		return d, fmt.Errorf("dashboard %s panels: %w", d.ID, err)
	}
	return d, nil
}

func (s *Store) ListDashboards(ctx context.Context, userID string) ([]analytics.Dashboard, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+dashboardCols+` FROM analytics_dashboards WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []analytics.Dashboard{}
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDashboard(ctx context.Context, id string) (analytics.Dashboard, error) {
	d, err := scanDashboard(s.pool.QueryRow(ctx, `SELECT `+dashboardCols+` FROM analytics_dashboards WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return d, fmt.Errorf("dashboard %s: %w", id, analytics.ErrNotFound)
	}
	return d, err
}

// PutDashboard creates or replaces; a replace never moves the owner or the
// creation time. pgx encodes the jsonb columns (a value JSON cannot hold is
// the Exec's error).
func (s *Store) PutDashboard(ctx context.Context, d analytics.Dashboard) error {
	if d.Filters == nil {
		d.Filters = []string{}
	}
	if d.Panels == nil {
		d.Panels = []analytics.Panel{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO analytics_dashboards (`+dashboardCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE SET name=$3, filters=$4, panels=$5, updated_at=$7`,
		d.ID, d.UserID, d.Name, d.Filters, d.Panels, d.CreatedAt, d.UpdatedAt)
	return err
}

func (s *Store) DeleteDashboard(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM analytics_dashboards WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("dashboard %s: %w", id, analytics.ErrNotFound)
	}
	return nil
}

// --- scorecards ---------------------------------------------------------------

const scorecardCols = `id, user_id, name, terms, created_at, updated_at`

func scanScorecard(row pgx.Row) (analytics.Scorecard, error) {
	var sc analytics.Scorecard
	var terms []byte
	if err := row.Scan(&sc.ID, &sc.UserID, &sc.Name, &terms, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
		return sc, err
	}
	if err := json.Unmarshal(terms, &sc.Terms); err != nil {
		return sc, fmt.Errorf("scorecard %s terms: %w", sc.ID, err)
	}
	return sc, nil
}

func (s *Store) ListScorecards(ctx context.Context, userID string) ([]analytics.Scorecard, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+scorecardCols+` FROM analytics_scorecards WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []analytics.Scorecard{}
	for rows.Next() {
		sc, err := scanScorecard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetScorecard(ctx context.Context, id string) (analytics.Scorecard, error) {
	sc, err := scanScorecard(s.pool.QueryRow(ctx, `SELECT `+scorecardCols+` FROM analytics_scorecards WHERE id=$1`, id))
	if err == pgx.ErrNoRows {
		return sc, fmt.Errorf("scorecard %s: %w", id, analytics.ErrNotFound)
	}
	return sc, err
}

// PutScorecard creates or replaces; a replace never moves the owner or the
// creation time. pgx encodes the terms (a NaN weight is the Exec's error).
func (s *Store) PutScorecard(ctx context.Context, sc analytics.Scorecard) error {
	if sc.Terms == nil {
		sc.Terms = []analytics.Term{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO analytics_scorecards (`+scorecardCols+`) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET name=$3, terms=$4, updated_at=$6`,
		sc.ID, sc.UserID, sc.Name, sc.Terms, sc.CreatedAt, sc.UpdatedAt)
	return err
}

func (s *Store) DeleteScorecard(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM analytics_scorecards WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("scorecard %s: %w", id, analytics.ErrNotFound)
	}
	return nil
}
