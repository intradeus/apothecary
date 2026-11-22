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
	err := s.analyticsQuery(ctx, c, []any{&d.RunID, &d.Title, &d.FlowName, &d.FlowHash, &d.State, &d.Archived, &d.CreatedAt, &d.WallMS, &d.CostUSD},
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
// creation, over the given projects only.
func (s *Store) FlowVersionLabels(ctx context.Context, projects []string, hashes []string) (map[string]string, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT hash, name || ' v' || n FROM (
			SELECT v.hash, v.name, row_number() OVER (PARTITION BY v.project_id, v.name ORDER BY v.created_at, v.hash) n
			FROM flow_versions v WHERE v.project_id = ANY($1)) x
		WHERE hash = ANY($2)`, nonNil(projects), hashes)
	return collectPairs(rows)
}

// ProjectNames names the ids among the given projects.
func (s *Store) ProjectNames(ctx context.Context, projects []string, ids []string) (map[string]string, error) {
	rows, _ := s.pool.Query(ctx, `SELECT p.id, p.name FROM projects p WHERE p.id = ANY($1) AND p.id = ANY($2)`, nonNil(projects), ids)
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

// maxVersions is how many versions a dashboard or scorecard keeps.
const maxVersions = 100

// PutDashboard creates or replaces; a replace never moves the owner or the
// creation time. pgx encodes the jsonb columns (a value JSON cannot hold is
// the Exec's error). In the same transaction it appends a version from the
// stored row when the name, filters or panels differ (jsonb equality) from
// the latest one, and keeps the newest maxVersions.
func (s *Store) PutDashboard(ctx context.Context, d analytics.Dashboard, c analytics.Change) error {
	if d.Filters == nil {
		d.Filters = []string{}
	}
	if d.Panels == nil {
		d.Panels = []analytics.Panel{}
	}
	return s.putVersioned(ctx, d.ID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO analytics_dashboards (`+dashboardCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (id) DO UPDATE SET name=$3, filters=$4, panels=$5, updated_at=$7`,
			d.ID, d.UserID, d.Name, d.Filters, d.Panels, d.CreatedAt, d.UpdatedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO analytics_dashboard_versions
			       (dashboard_id, seq, name, filters, panels, author_kind, author_id, restored_from, created_at)
			SELECT d.id, COALESCE(l.seq, 0) + 1, d.name, d.filters, d.panels, $2, $3, NULLIF($4, 0), d.updated_at
			  FROM analytics_dashboards d
			  LEFT JOIN LATERAL (SELECT seq, name, filters, panels FROM analytics_dashboard_versions
			                      WHERE dashboard_id = d.id ORDER BY seq DESC LIMIT 1) l ON true
			 WHERE d.id = $1
			   AND (l.seq IS NULL OR (d.name, d.filters, d.panels) IS DISTINCT FROM (l.name, l.filters, l.panels))`,
			d.ID, string(c.Kind), c.AuthorID, c.RestoredFrom)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM analytics_dashboard_versions
			 WHERE dashboard_id = $1
			   AND seq <= (SELECT max(seq) FROM analytics_dashboard_versions WHERE dashboard_id = $1) - $2`,
			d.ID, maxVersions)
		return err
	})
}

// putVersioned runs a versioned write in one transaction; a unique
// violation (two writers taking the same seq) is analytics.ErrConflict.
func (s *Store) putVersioned(ctx context.Context, id string, write func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := write(tx); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%s changed meanwhile: %w", id, analytics.ErrConflict)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%s changed meanwhile: %w", id, analytics.ErrConflict)
		}
		return err
	}
	return nil
}

const dashboardVersionCols = `dashboard_id, seq, name, filters, panels, author_kind, author_id, COALESCE(restored_from, 0), created_at`

func scanDashboardVersion(row pgx.Row) (analytics.DashboardVersion, error) {
	var v analytics.DashboardVersion
	var filters, panels []byte
	if err := row.Scan(&v.DashboardID, &v.Seq, &v.Name, &filters, &panels, &v.Kind, &v.AuthorID, &v.RestoredFrom, &v.CreatedAt); err != nil {
		return v, err
	}
	if err := json.Unmarshal(filters, &v.Filters); err != nil {
		return v, fmt.Errorf("dashboard %s version %d filters: %w", v.DashboardID, v.Seq, err)
	}
	if err := json.Unmarshal(panels, &v.Panels); err != nil {
		return v, fmt.Errorf("dashboard %s version %d panels: %w", v.DashboardID, v.Seq, err)
	}
	return v, nil
}

// ListDashboardVersions is newest first; an unknown dashboard is
// ErrNotFound.
func (s *Store) ListDashboardVersions(ctx context.Context, id string) ([]analytics.DashboardVersion, error) {
	if err := s.analyticsExists(ctx, "analytics_dashboards", "dashboard", id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+dashboardVersionCols+` FROM analytics_dashboard_versions WHERE dashboard_id=$1 ORDER BY seq DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []analytics.DashboardVersion{}
	for rows.Next() {
		v, err := scanDashboardVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetDashboardVersion(ctx context.Context, id string, seq int) (analytics.DashboardVersion, error) {
	v, err := scanDashboardVersion(s.pool.QueryRow(ctx, `SELECT `+dashboardVersionCols+` FROM analytics_dashboard_versions WHERE dashboard_id=$1 AND seq=$2`, id, seq))
	if err == pgx.ErrNoRows {
		return v, fmt.Errorf("dashboard %s version %d: %w", id, seq, analytics.ErrNotFound)
	}
	return v, err
}

// analyticsExists is ErrNotFound when table has no row id.
func (s *Store) analyticsExists(ctx context.Context, table, what, id string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id=$1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s %s: %w", what, id, analytics.ErrNotFound)
	}
	return nil
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
// Versions as PutDashboard does, on (name, terms).
func (s *Store) PutScorecard(ctx context.Context, sc analytics.Scorecard, c analytics.Change) error {
	if sc.Terms == nil {
		sc.Terms = []analytics.Term{}
	}
	return s.putVersioned(ctx, sc.ID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO analytics_scorecards (`+scorecardCols+`) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET name=$3, terms=$4, updated_at=$6`,
			sc.ID, sc.UserID, sc.Name, sc.Terms, sc.CreatedAt, sc.UpdatedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO analytics_scorecard_versions
			       (scorecard_id, seq, name, terms, author_kind, author_id, restored_from, created_at)
			SELECT sc.id, COALESCE(l.seq, 0) + 1, sc.name, sc.terms, $2, $3, NULLIF($4, 0), sc.updated_at
			  FROM analytics_scorecards sc
			  LEFT JOIN LATERAL (SELECT seq, name, terms FROM analytics_scorecard_versions
			                      WHERE scorecard_id = sc.id ORDER BY seq DESC LIMIT 1) l ON true
			 WHERE sc.id = $1
			   AND (l.seq IS NULL OR (sc.name, sc.terms) IS DISTINCT FROM (l.name, l.terms))`,
			sc.ID, string(c.Kind), c.AuthorID, c.RestoredFrom)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM analytics_scorecard_versions
			 WHERE scorecard_id = $1
			   AND seq <= (SELECT max(seq) FROM analytics_scorecard_versions WHERE scorecard_id = $1) - $2`,
			sc.ID, maxVersions)
		return err
	})
}

const scorecardVersionCols = `scorecard_id, seq, name, terms, author_kind, author_id, COALESCE(restored_from, 0), created_at`

func scanScorecardVersion(row pgx.Row) (analytics.ScorecardVersion, error) {
	var v analytics.ScorecardVersion
	var terms []byte
	if err := row.Scan(&v.ScorecardID, &v.Seq, &v.Name, &terms, &v.Kind, &v.AuthorID, &v.RestoredFrom, &v.CreatedAt); err != nil {
		return v, err
	}
	if err := json.Unmarshal(terms, &v.Terms); err != nil {
		return v, fmt.Errorf("scorecard %s version %d terms: %w", v.ScorecardID, v.Seq, err)
	}
	return v, nil
}

// ListScorecardVersions is newest first; an unknown scorecard is
// ErrNotFound.
func (s *Store) ListScorecardVersions(ctx context.Context, id string) ([]analytics.ScorecardVersion, error) {
	if err := s.analyticsExists(ctx, "analytics_scorecards", "scorecard", id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+scorecardVersionCols+` FROM analytics_scorecard_versions WHERE scorecard_id=$1 ORDER BY seq DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []analytics.ScorecardVersion{}
	for rows.Next() {
		v, err := scanScorecardVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetScorecardVersion(ctx context.Context, id string, seq int) (analytics.ScorecardVersion, error) {
	v, err := scanScorecardVersion(s.pool.QueryRow(ctx, `SELECT `+scorecardVersionCols+` FROM analytics_scorecard_versions WHERE scorecard_id=$1 AND seq=$2`, id, seq))
	if err == pgx.ErrNoRows {
		return v, fmt.Errorf("scorecard %s version %d: %w", id, seq, analytics.ErrNotFound)
	}
	return v, err
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
