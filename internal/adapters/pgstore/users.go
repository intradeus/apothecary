package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/account/auth"
)

// Accounts (TODO 25): pgstore implements auth.DB. Secrets (password hashes)
// are opaque bytes to this layer; sessions and tokens are stored by hash.

const userCols = `id, handle, email, name, admin, disabled_at, last_seen_at, created_at, updated_at`

func scanUser(row pgx.Row) (auth.User, error) {
	var u auth.User
	err := row.Scan(&u.ID, &u.Handle, &u.Email, &u.Name, &u.Admin, &u.DisabledAt, &u.LastSeenAt, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func userConflict(err error, u auth.User) error {
	if !isUnique(err) {
		return err
	}
	if strings.Contains(err.Error(), "users_email_idx") {
		return fmt.Errorf("email %s: %w", u.Email, auth.ErrConflict)
	}
	return fmt.Errorf("handle %s: %w", u.Handle, auth.ErrConflict)
}

func (s *Store) CreateUser(ctx context.Context, u auth.User) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (`+userCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		u.ID, u.Handle, u.Email, u.Name, u.Admin, u.DisabledAt, u.LastSeenAt, u.CreatedAt, u.UpdatedAt)
	return userConflict(err, u)
}

func (s *Store) UpdateUser(ctx context.Context, u auth.User) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users SET handle=$2, email=$3, name=$4, admin=$5, disabled_at=$6, updated_at=$7 WHERE id=$1`,
		u.ID, u.Handle, u.Email, u.Name, u.Admin, u.DisabledAt, u.UpdatedAt)
	if err != nil {
		return userConflict(err, u)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %s: %w", u.ID, auth.ErrNotFound)
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (auth.User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, fmt.Errorf("user %s: %w", id, auth.ErrNotFound)
	}
	return u, err
}

func (s *Store) GetUserByLogin(ctx context.Context, login string) (auth.User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE lower(handle)=lower($1) OR lower(email)=lower($1) LIMIT 1`, login))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, fmt.Errorf("user %s: %w", login, auth.ErrNotFound)
	}
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]auth.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) TouchUser(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET last_seen_at=$2 WHERE id=$1`, id, at)
	return err
}

// --- methods ------------------------------------------------------------------

const methodCols = `id, user_id, kind, subject, secret, config, created_at, updated_at, last_used_at`

func scanMethod(row pgx.Row) (auth.Method, error) {
	var m auth.Method
	var cfg []byte
	err := row.Scan(&m.ID, &m.UserID, &m.Kind, &m.Subject, &m.Secret, &cfg, &m.CreatedAt, &m.UpdatedAt, &m.LastUsedAt)
	if err != nil {
		return m, err
	}
	if len(cfg) > 0 {
		_ = json.Unmarshal(cfg, &m.Config)
	}
	return m, nil
}

func (s *Store) PutMethod(ctx context.Context, m auth.Method) error {
	cfg, err := json.Marshal(m.Config)
	if err != nil {
		return err
	}
	if m.Config == nil {
		cfg = []byte("{}")
	}
	secret := m.Secret
	if secret == nil {
		secret = []byte{}
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO auth_methods (`+methodCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET subject=$4, secret=$5, config=$6, updated_at=$8, last_used_at=$9`,
		m.ID, m.UserID, m.Kind, m.Subject, secret, cfg, m.CreatedAt, m.UpdatedAt, m.LastUsedAt)
	if isUnique(err) {
		return fmt.Errorf("%s method: %w", m.Kind, auth.ErrConflict)
	}
	return err
}

func (s *Store) TouchMethod(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE auth_methods SET last_used_at=$2 WHERE id=$1`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("method %s: %w", id, auth.ErrNotFound)
	}
	return nil
}

func (s *Store) GetMethod(ctx context.Context, userID, kind string) (auth.Method, error) {
	m, err := scanMethod(s.pool.QueryRow(ctx,
		`SELECT `+methodCols+` FROM auth_methods WHERE user_id=$1 AND kind=$2 ORDER BY created_at LIMIT 1`, userID, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, fmt.Errorf("%s method: %w", kind, auth.ErrNotFound)
	}
	return m, err
}

func (s *Store) FindMethodBySubject(ctx context.Context, kind, subject string) (auth.Method, error) {
	if subject == "" {
		return auth.Method{}, fmt.Errorf("%s method: %w", kind, auth.ErrNotFound)
	}
	m, err := scanMethod(s.pool.QueryRow(ctx,
		`SELECT `+methodCols+` FROM auth_methods WHERE kind=$1 AND subject=$2`, kind, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, fmt.Errorf("%s method: %w", kind, auth.ErrNotFound)
	}
	return m, err
}

func (s *Store) ListMethods(ctx context.Context, userID string) ([]auth.Method, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+methodCols+` FROM auth_methods WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.Method
	for rows.Next() {
		m, err := scanMethod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMethod(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_methods WHERE id=$1`, id)
	return err
}

// --- sessions -----------------------------------------------------------------

func (s *Store) CreateSession(ctx context.Context, se auth.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, user_agent) VALUES ($1,$2,$3,$4,$5,$6)`,
		se.ID, se.UserID, se.CreatedAt, se.ExpiresAt, se.LastSeenAt, se.UserAgent)
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (auth.Session, error) {
	var se auth.Session
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, created_at, expires_at, last_seen_at, user_agent FROM sessions WHERE id=$1`, id).
		Scan(&se.ID, &se.UserID, &se.CreatedAt, &se.ExpiresAt, &se.LastSeenAt, &se.UserAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return se, fmt.Errorf("session: %w", auth.ErrNotFound)
	}
	return se, err
}

func (s *Store) TouchSession(ctx context.Context, id string, seen, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at=$2, expires_at=$3 WHERE id=$1`, id, seen, expires)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session: %w", auth.ErrNotFound)
	}
	return nil
}

func (s *Store) DeleteSessionsOf(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}

// --- tokens -------------------------------------------------------------------

const tokenCols = `id, user_id, name, hash, prefix, created_at, last_used_at, expires_at`

func scanToken(row pgx.Row) (auth.Token, error) {
	var t auth.Token
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Hash, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	return t, err
}

func (s *Store) CreateToken(ctx context.Context, t auth.Token) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.UserID, t.Name, t.Hash, t.Prefix, t.CreatedAt, t.LastUsedAt, t.ExpiresAt)
	return err
}

func (s *Store) GetTokenByHash(ctx context.Context, hash string) (auth.Token, error) {
	t, err := scanToken(s.pool.QueryRow(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE hash=$1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("token: %w", auth.ErrNotFound)
	}
	return t, err
}

func (s *Store) ListTokens(ctx context.Context, userID string) ([]auth.Token, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteToken(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("token %s: %w", id, auth.ErrNotFound)
	}
	return nil
}

func (s *Store) TouchToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at=$2 WHERE id=$1`, id, at)
	return err
}

func (s *Store) DeleteTokensOf(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE user_id=$1`, userID)
	return err
}

// --- ssh keys -----------------------------------------------------------------

const sshKeyCols = `id, user_id, name, public_key, fingerprint, created_at, last_used_at`

func scanSSHKey(row pgx.Row) (auth.SSHKey, error) {
	var k auth.SSHKey
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &k.Fingerprint, &k.CreatedAt, &k.LastUsedAt)
	if err == nil {
		if f := strings.Fields(k.PublicKey); len(f) > 0 {
			k.Type = f[0]
		}
	}
	return k, err
}

func (s *Store) CreateSSHKey(ctx context.Context, k auth.SSHKey) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO ssh_keys (`+sshKeyCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		k.ID, k.UserID, k.Name, k.PublicKey, k.Fingerprint, k.CreatedAt, k.LastUsedAt)
	if isUnique(err) {
		return fmt.Errorf("ssh key %s: %w", k.Fingerprint, auth.ErrConflict)
	}
	return err
}

func (s *Store) GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (auth.SSHKey, error) {
	k, err := scanSSHKey(s.pool.QueryRow(ctx, `SELECT `+sshKeyCols+` FROM ssh_keys WHERE fingerprint=$1`, fingerprint))
	if errors.Is(err, pgx.ErrNoRows) {
		return k, fmt.Errorf("ssh key: %w", auth.ErrNotFound)
	}
	return k, err
}

func (s *Store) ListSSHKeys(ctx context.Context, userID string) ([]auth.SSHKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sshKeyCols+` FROM ssh_keys WHERE user_id=$1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.SSHKey
	for rows.Next() {
		k, err := scanSSHKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSSHKey(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ssh_keys WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ssh key %s: %w", id, auth.ErrNotFound)
	}
	return nil
}

func (s *Store) TouchSSHKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE ssh_keys SET last_used_at=$2 WHERE id=$1`, id, at)
	return err
}

var _ auth.DB = (*Store)(nil)
