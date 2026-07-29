package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDB is an in-memory artifacts.DB.
type fakeDB struct{ rows map[string]Meta }

func newFakeDB() *fakeDB { return &fakeDB{rows: map[string]Meta{}} }

func (d *fakeDB) InsertArtifact(_ context.Context, m Meta) error {
	m.CreatedAt = time.Unix(1, 0) // stand-in for the DB's default now()
	d.rows[m.ID] = m
	return nil
}
func (d *fakeDB) ArtifactsForStage(_ context.Context, runID string) ([]Meta, error) {
	var out []Meta
	for _, m := range d.rows {
		if m.RunID == runID && IsStageScope(m.Scope) {
			out = append(out, m)
		}
	}
	return out, nil
}
func (d *fakeDB) AssociateArtifacts(_ context.Context, ids []string, runID, pingID, scope string) error {
	for _, id := range ids {
		if m, ok := d.rows[id]; ok && m.Scope == ScopeOrphan {
			m.RunID, m.PingID, m.Scope = runID, pingID, scope
			d.rows[id] = m
		}
	}
	return nil
}
func (d *fakeDB) ArtifactsForRun(_ context.Context, runID string) (out []Meta, _ error) {
	for _, m := range d.rows {
		if m.RunID == runID {
			out = append(out, m)
		}
	}
	return out, nil
}
func (d *fakeDB) ArtifactsForPing(_ context.Context, pingID, scope string) (out []Meta, _ error) {
	for _, m := range d.rows {
		if m.PingID == pingID && m.Scope == scope {
			out = append(out, m)
		}
	}
	return out, nil
}
func (d *fakeDB) GetArtifact(_ context.Context, id string) (Meta, error) {
	if m, ok := d.rows[id]; ok {
		return m, nil
	}
	return Meta{}, ErrNotFound
}
func (d *fakeDB) DeleteArtifactsForRun(_ context.Context, runID string) (ids []string, _ error) {
	for id, m := range d.rows {
		if m.RunID == runID {
			ids = append(ids, id)
			delete(d.rows, id)
		}
	}
	return ids, nil
}
func (d *fakeDB) OrphanArtifactIDsBefore(_ context.Context, cutoff time.Time) (ids []string, _ error) {
	for id, m := range d.rows {
		if m.Scope == ScopeOrphan && m.CreatedAt.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (d *fakeDB) DeleteArtifacts(_ context.Context, ids []string) error {
	for _, id := range ids {
		delete(d.rows, id)
	}
	return nil
}

func newStore(t *testing.T) (*Store, *fakeDB) {
	t.Helper()
	db := newFakeDB()
	return New(db, t.TempDir(), 32, []string{"text/*", "image/png"}), db
}

func TestSaveRoundTrip(t *testing.T) {
	s, db := newStore(t)
	m, err := s.Save(context.Background(), "notes.txt", "text/plain", strings.NewReader("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Scope != ScopeOrphan || m.Filename != "notes.txt" || m.Size != 11 || m.SHA256 == "" {
		t.Errorf("meta = %+v", m)
	}
	// Blob is on disk under the 2-char shard.
	if _, err := os.Stat(filepath.Join(s.dir, m.ID[:2], m.ID)); err != nil {
		t.Errorf("blob not on disk: %v", err)
	}
	if _, ok := db.rows[m.ID]; !ok {
		t.Error("row not inserted")
	}
	data, got, err := s.Bytes(context.Background(), m.ID)
	if err != nil || string(data) != "hello world" || got.ID != m.ID {
		t.Errorf("Bytes = %q, %+v, %v", data, got, err)
	}
}

func TestSaveRejects(t *testing.T) {
	s, _ := newStore(t)
	// Over the 32-byte cap.
	if _, err := s.Save(context.Background(), "big.txt", "text/plain", strings.NewReader(strings.Repeat("x", 40))); err != ErrTooLarge {
		t.Errorf("oversized err = %v, want ErrTooLarge", err)
	}
	// A type outside the allowlist.
	if _, err := s.Save(context.Background(), "app.bin", "application/x-thing", strings.NewReader("data")); err != ErrMimeType {
		t.Errorf("bad-mime err = %v, want ErrMimeType", err)
	}
}

func TestAssociateAndDelete(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	a, _ := s.Save(ctx, "a.txt", "text/plain", strings.NewReader("a"))
	b, _ := s.Save(ctx, "b.png", "image/png", strings.NewReader("b"))

	if err := s.Associate(ctx, []string{a.ID}, "run1", "", ScopeGoal); err != nil {
		t.Fatal(err)
	}
	if err := s.Associate(ctx, []string{b.ID}, "run1", "ping1", ScopePingAnswer); err != nil {
		t.Fatal(err)
	}
	goal, _ := s.ForRun(ctx, "run1")
	if len(goal) != 2 {
		t.Errorf("ForRun = %d, want 2", len(goal))
	}
	ans, _ := s.ForPing(ctx, "ping1", ScopePingAnswer)
	if len(ans) != 1 || ans[0].ID != b.ID {
		t.Errorf("ForPing = %+v", ans)
	}

	// DeleteForRun drops rows AND blobs.
	if err := s.DeleteForRun(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ForRun(ctx, "run1"); len(left) != 0 {
		t.Errorf("rows survived delete: %d", len(left))
	}
	if _, err := os.Stat(s.blobPath(a.ID)); !os.IsNotExist(err) {
		t.Errorf("blob a survived delete: %v", err)
	}
}

func TestGCOrphans(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	orphan, _ := s.Save(ctx, "o.txt", "text/plain", strings.NewReader("o"))
	kept, _ := s.Save(ctx, "k.txt", "text/plain", strings.NewReader("k"))
	_ = s.Associate(ctx, []string{kept.ID}, "run1", "", ScopeGoal)

	// Look from the future so the epoch-stamped orphan is "old".
	s.now = func() time.Time { return time.Unix(1, 0).Add(2 * time.Hour) }
	n, err := s.GCOrphans(ctx, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("GCOrphans = %d, %v, want 1", n, err)
	}
	if _, err := os.Stat(s.blobPath(orphan.ID)); !os.IsNotExist(err) {
		t.Errorf("orphan blob survived GC: %v", err)
	}
	if _, ok := db.rows[kept.ID]; !ok {
		t.Error("attached artifact wrongly GC'd")
	}
}
