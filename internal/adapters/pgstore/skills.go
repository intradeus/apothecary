package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/project/confstore"
	"apothecary/internal/skills/skillstore"
)

// --- skills -----------------------------------------------------------------

const skillCols = `id, user_id, name, description, files, hash, source, created_at, updated_at`

func scanSkill(row pgx.Row) (skillstore.Skill, error) {
	var s skillstore.Skill
	var files, source []byte
	if err := row.Scan(&s.ID, &s.UserID, &s.Name, &s.Description, &files, &s.Hash, &source, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return s, err
	}
	if err := json.Unmarshal(files, &s.Files); err != nil {
		return s, fmt.Errorf("skill %s: files: %w", s.ID, err)
	}
	if len(source) > 0 {
		s.Source = &skillstore.Source{}
		if err := json.Unmarshal(source, s.Source); err != nil {
			return s, fmt.Errorf("skill %s: source: %w", s.ID, err)
		}
	}
	return s, nil
}

func sourceJSON(src *skillstore.Source) ([]byte, error) {
	if src == nil {
		return nil, nil
	}
	return json.Marshal(src)
}

func (s *Store) UpsertSkill(ctx context.Context, sk skillstore.Skill) error {
	files, err := json.Marshal(sk.Files)
	if err != nil {
		return err
	}
	if sk.Files == nil {
		files = []byte("[]")
	}
	source, err := sourceJSON(sk.Source)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO skills (`+skillCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			name=$3, description=$4, files=$5, hash=$6, source=$7, updated_at=$9`,
		sk.ID, sk.UserID, sk.Name, sk.Description, files, sk.Hash, source, sk.CreatedAt, sk.UpdatedAt)
	if isUnique(err) {
		return fmt.Errorf("skill %s: %w", sk.Name, confstore.ErrConflict)
	}
	return err
}

func (s *Store) GetSkill(ctx context.Context, id string) (skillstore.Skill, error) {
	sk, err := scanSkill(s.pool.QueryRow(ctx, `SELECT `+skillCols+` FROM skills WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sk, fmt.Errorf("skill %s: %w", id, confstore.ErrNotFound)
	}
	return sk, err
}

func (s *Store) GetSkillByName(ctx context.Context, userID, name string) (skillstore.Skill, error) {
	sk, err := scanSkill(s.pool.QueryRow(ctx, `SELECT `+skillCols+` FROM skills WHERE user_id=$1 AND name=$2`, userID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return sk, fmt.Errorf("skill %s: %w", name, confstore.ErrNotFound)
	}
	return sk, err
}

func (s *Store) GetSkillBySource(ctx context.Context, userID, url string) (skillstore.Skill, error) {
	sk, err := scanSkill(s.pool.QueryRow(ctx, `SELECT `+skillCols+` FROM skills WHERE user_id=$1 AND source->>'url'=$2`, userID, url))
	if errors.Is(err, pgx.ErrNoRows) {
		return sk, fmt.Errorf("skill from %s: %w", url, confstore.ErrNotFound)
	}
	return sk, err
}

func (s *Store) ListSkills(ctx context.Context, userID string) ([]skillstore.Skill, error) {
	where, args := ownerClause(userID, 1)
	rows, err := s.pool.Query(ctx, `SELECT `+skillCols+` FROM skills`+where+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []skillstore.Skill
	for rows.Next() {
		sk, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	sets, err := s.SkillsetsContaining(ctx, id)
	if err != nil {
		return err
	}
	if len(sets) > 0 {
		names := make([]string, 0, len(sets))
		for _, set := range sets {
			names = append(names, set.Name)
		}
		return fmt.Errorf("skill is used by skillset %s: %w", strings.Join(names, ", "), confstore.ErrReferenced)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM skills WHERE id=$1`, id)
	if isFK(err) {
		return fmt.Errorf("skill %s is used by a skillset: %w", id, confstore.ErrReferenced)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("skill %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

// --- skillsets --------------------------------------------------------------

// skillsetCols folds the ordered membership in as a json array so one row
// is one skillset.
const skillsetCols = `s.id, s.user_id, s.name, s.description, s.source, s.head_seq, s.created_at, s.updated_at,
	(SELECT COALESCE(jsonb_agg(m.skill_id ORDER BY m.position), '[]'::jsonb) FROM skillset_skills m WHERE m.skillset_id = s.id)`

func scanSkillset(row pgx.Row) (skillstore.Skillset, error) {
	var set skillstore.Skillset
	var source, members []byte
	if err := row.Scan(&set.ID, &set.UserID, &set.Name, &set.Description, &source, &set.HeadSeq, &set.CreatedAt, &set.UpdatedAt, &members); err != nil {
		return set, err
	}
	if len(source) > 0 {
		set.Source = &skillstore.Source{}
		if err := json.Unmarshal(source, set.Source); err != nil {
			return set, fmt.Errorf("skillset %s: source: %w", set.ID, err)
		}
	}
	if err := json.Unmarshal(members, &set.SkillIDs); err != nil {
		return set, fmt.Errorf("skillset %s: members: %w", set.ID, err)
	}
	if set.SkillIDs == nil {
		set.SkillIDs = []string{}
	}
	return set, nil
}

func (s *Store) UpsertSkillset(ctx context.Context, set skillstore.Skillset) error {
	source, err := sourceJSON(set.Source)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO skillsets (id, user_id, name, description, source, head_seq, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,0,$6,$7)
		ON CONFLICT (id) DO UPDATE SET name=$3, description=$4, source=$5, updated_at=$7`,
		set.ID, set.UserID, set.Name, set.Description, source, set.CreatedAt, set.UpdatedAt)
	if isUnique(err) {
		return fmt.Errorf("skillset %s: %w", set.Name, confstore.ErrConflict)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM skillset_skills WHERE skillset_id=$1`, set.ID); err != nil {
		return err
	}
	for i, id := range set.SkillIDs {
		_, err := tx.Exec(ctx, `INSERT INTO skillset_skills (skillset_id, skill_id, position) VALUES ($1,$2,$3)`, set.ID, id, i)
		if isFK(err) {
			return fmt.Errorf("skillset %s references a missing skill %s: %w", set.Name, id, confstore.ErrInvalid)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) GetSkillset(ctx context.Context, id string) (skillstore.Skillset, error) {
	set, err := scanSkillset(s.pool.QueryRow(ctx, `SELECT `+skillsetCols+` FROM skillsets s WHERE s.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return set, fmt.Errorf("skillset %s: %w", id, confstore.ErrNotFound)
	}
	return set, err
}

func (s *Store) querySkillsets(ctx context.Context, q string, args ...any) ([]skillstore.Skillset, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []skillstore.Skillset
	for rows.Next() {
		set, err := scanSkillset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, set)
	}
	return out, rows.Err()
}

func (s *Store) ListSkillsets(ctx context.Context, userID string) ([]skillstore.Skillset, error) {
	q := `SELECT ` + skillsetCols + ` FROM skillsets s`
	if userID == "" {
		return s.querySkillsets(ctx, q+` ORDER BY s.name, s.id`)
	}
	return s.querySkillsets(ctx, q+` WHERE s.user_id=$1 ORDER BY s.name, s.id`, userID)
}

func (s *Store) SkillsetsContaining(ctx context.Context, skillID string) ([]skillstore.Skillset, error) {
	return s.querySkillsets(ctx, `SELECT `+skillsetCols+` FROM skillsets s
		WHERE EXISTS (SELECT 1 FROM skillset_skills x WHERE x.skillset_id = s.id AND x.skill_id = $1)
		ORDER BY s.name, s.id`, skillID)
}

// DeleteSkillset refuses while a live run pins the skillset: the runs
// projection's view carries the run's pins (run.View.Skillsets).
func (s *Store) DeleteSkillset(ctx context.Context, id string) error {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id FROM runs
		WHERE state IN ('pending','provisioning','running','waiting_on_human','in_review')
		  AND view @> jsonb_build_object('skillsets', jsonb_build_array(jsonb_build_object('id', $1::text)))
		ORDER BY run_id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("skillset is pinned by run %s: %w", strings.Join(refs, ", "), confstore.ErrReferenced)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM skillsets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("skillset %s: %w", id, confstore.ErrNotFound)
	}
	return nil
}

// --- versions ---------------------------------------------------------------

const versionCols = `skillset_id, seq, hash, manifest, created_at`

func scanVersion(row pgx.Row) (skillstore.Version, error) {
	var v skillstore.Version
	var manifest []byte
	if err := row.Scan(&v.SkillsetID, &v.Seq, &v.Hash, &manifest, &v.CreatedAt); err != nil {
		return v, err
	}
	if err := json.Unmarshal(manifest, &v.Members); err != nil {
		return v, fmt.Errorf("skillset %s v%d: manifest: %w", v.SkillsetID, v.Seq, err)
	}
	if v.Members == nil {
		v.Members = []skillstore.VersionMember{}
	}
	return v, nil
}

func (s *Store) InsertVersion(ctx context.Context, v skillstore.Version) error {
	manifest, err := json.Marshal(v.Members)
	if err != nil {
		return err
	}
	if v.Members == nil {
		manifest = []byte("[]")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO skillset_versions (`+versionCols+`) VALUES ($1,$2,$3,$4,$5)`,
		v.SkillsetID, v.Seq, v.Hash, manifest, v.CreatedAt)
	if isUnique(err) {
		return fmt.Errorf("skillset %s v%d: %w", v.SkillsetID, v.Seq, confstore.ErrConflict)
	}
	if isFK(err) {
		return fmt.Errorf("skillset %s: %w", v.SkillsetID, confstore.ErrInvalid)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE skillsets SET head_seq=$2, updated_at=$3 WHERE id=$1 AND head_seq < $2`,
		v.SkillsetID, v.Seq, v.CreatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetVersion(ctx context.Context, skillsetID string, seq int) (skillstore.Version, error) {
	v, err := scanVersion(s.pool.QueryRow(ctx, `SELECT `+versionCols+` FROM skillset_versions WHERE skillset_id=$1 AND seq=$2`, skillsetID, seq))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, fmt.Errorf("skillset %s v%d: %w", skillsetID, seq, confstore.ErrNotFound)
	}
	return v, err
}

func (s *Store) ListVersions(ctx context.Context, skillsetID string) ([]skillstore.Version, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+versionCols+` FROM skillset_versions WHERE skillset_id=$1 ORDER BY seq DESC`, skillsetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []skillstore.Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

var _ skillstore.DB = (*Store)(nil)
