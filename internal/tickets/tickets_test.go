package tickets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/artifacts"
	"apothecary/internal/confstore"
	"apothecary/internal/ports"
	"apothecary/internal/secrets"
)

const testProject = "prj-1"

// --- test fixtures ----------------------------------------------------------

// artDB is an in-memory artifacts.DB (only what the factory exercises).
type artDB struct{ rows map[string]artifacts.Meta }

func (d *artDB) InsertArtifact(_ context.Context, m artifacts.Meta) error {
	d.rows[m.ID] = m
	return nil
}
func (d *artDB) AssociateArtifacts(_ context.Context, ids []string, runID, pingID, scope string) error {
	for _, id := range ids {
		if m, ok := d.rows[id]; ok && m.Scope == artifacts.ScopeOrphan {
			m.RunID, m.PingID, m.Scope = runID, pingID, scope
			d.rows[id] = m
		}
	}
	return nil
}
func (d *artDB) GetArtifact(_ context.Context, id string) (artifacts.Meta, error) {
	if m, ok := d.rows[id]; ok {
		return m, nil
	}
	return artifacts.Meta{}, artifacts.ErrNotFound
}
func (d *artDB) ArtifactsForStage(context.Context, string) ([]artifacts.Meta, error) {
	return nil, nil
}
func (d *artDB) ArtifactsForRun(context.Context, string) ([]artifacts.Meta, error) { return nil, nil }
func (d *artDB) ArtifactsForPing(context.Context, string, string) ([]artifacts.Meta, error) {
	return nil, nil
}
func (d *artDB) DeleteArtifactsForRun(context.Context, string) ([]string, error) { return nil, nil }
func (d *artDB) OrphanArtifactIDsBefore(context.Context, time.Time) ([]string, error) {
	return nil, nil
}
func (d *artDB) DeleteArtifacts(context.Context, []string) error { return nil }

// stubSources serves one ticket source under any id.
type stubSources struct {
	src  ports.TicketSource
	kind string
	err  error
}

func (s stubSources) TicketSource(context.Context, string) (ports.TicketSource, error) {
	return s.src, s.err
}
func (s stubSources) TicketSourceKind(context.Context, string) (string, error) {
	return s.kind, s.err
}

func newFactory(t *testing.T, src ports.TicketSource) (*Factory, *artDB) {
	t.Helper()
	mem := confstore.NewMem()
	if err := mem.UpsertProject(context.Background(), confstore.Project{ID: testProject, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	db := &artDB{rows: map[string]artifacts.Meta{}}
	f := &Factory{
		Conf:      confstore.New(mem, key),
		Artifacts: artifacts.New(db, t.TempDir(), 1<<20, []string{"text/*", "image/png"}),
	}
	if src != nil {
		f.Sources = stubSources{src: src, kind: "faketracker"}
	}
	return f, db
}

func devSource() *fake.TicketSource {
	return &fake.TicketSource{
		Tickets: []ports.SourceTicket{{
			Key: "PROJ-7", Title: "Add a Farewell function",
			Body: "The greeter only says hello.", URL: "https://tracker.test/browse/PROJ-7",
			Status: "To Do", Type: "Story",
			Attachments: []ports.SourceAttachment{
				{ID: "a1", Filename: "spec.md", Mime: "text/markdown", Size: 5},
			},
		}},
		Blobs: map[string][]byte{"a1": []byte("hello")},
	}
}

// --- tests ------------------------------------------------------------------

// The import copies title, body, provenance AND the attachment bytes, and the
// resulting ticket is claimed for the ticket (shared by every run) exactly
// like a hand-attached upload.
func TestCreateFromSourceCopiesEverything(t *testing.T) {
	f, db := newFactory(t, devSource())

	tk, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if tk.Title != "Add a Farewell function" || tk.Goal != "The greeter only says hello." {
		t.Errorf("ticket = %+v", tk)
	}
	if tk.SourceKey != "PROJ-7" || tk.SourceID != "int-1" || tk.SourceKind != "faketracker" ||
		tk.SourceURL != "https://tracker.test/browse/PROJ-7" {
		t.Errorf("provenance = %+v", tk)
	}
	if len(tk.ArtifactIDs) != 1 {
		t.Fatalf("artifact_ids = %v, want the imported attachment", tk.ArtifactIDs)
	}
	got := db.rows[tk.ArtifactIDs[0]]
	if got.Filename != "spec.md" || got.Scope != artifacts.ScopeTicketGoal || got.RunID != "" {
		t.Errorf("imported artifact = %+v, want spec.md claimed for the ticket", got)
	}
	if got.Size != 5 {
		t.Errorf("imported %d bytes, want the attachment's 5", got.Size)
	}
}

// Anything the operator typed into the prefilled form wins over the tracker's
// version — the import is a starting point, not a lock.
func TestCreateFromSourceRespectsOverrides(t *testing.T) {
	f, _ := newFactory(t, devSource())

	tk, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{
		ProjectID: testProject, Title: "My own title", Goal: "my own goal",
		BaseBranch: "feat/proj-7-farewell",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Title != "My own title" || tk.Goal != "my own goal" || tk.BaseBranch != "feat/proj-7-farewell" {
		t.Errorf("overrides lost: %+v", tk)
	}
	if tk.SourceKey != "PROJ-7" {
		t.Errorf("provenance lost: %+v", tk)
	}
}

// Importing the same issue into the same project twice is nearly always a
// mis-click; the refusal names the ticket that already exists.
func TestCreateFromSourceRefusesDuplicate(t *testing.T) {
	f, _ := newFactory(t, devSource())
	first, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second import err = %v, want ErrDuplicate", err)
	}
	if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("err %q does not name the existing ticket %s", err, first.ID)
	}
}

// One unusable attachment must not sink the whole import: it is skipped, named
// in the warnings, and everything else still lands.
func TestCreateFromSourceWarnsOnSkippedAttachment(t *testing.T) {
	src := devSource()
	src.Tickets[0].Attachments = append(src.Tickets[0].Attachments,
		ports.SourceAttachment{ID: "a2", Filename: "archive.zip", Mime: "application/zip", Size: 3})
	src.Blobs["a2"] = []byte("zip")
	f, _ := newFactory(t, src)

	tk, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.ArtifactIDs) != 1 {
		t.Errorf("artifact_ids = %v, want only the allowed file", tk.ArtifactIDs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "archive.zip") {
		t.Fatalf("warnings = %v, want one naming archive.zip", warnings)
	}
}

// With no tracker wired at all, importing says so instead of failing obscurely.
func TestImportWithoutSourcesIsHonest(t *testing.T) {
	f, _ := newFactory(t, nil)
	_, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject})
	if !errors.Is(err, ErrNoSources) {
		t.Fatalf("err = %v, want ErrNoSources", err)
	}
	if _, err := f.Search(context.Background(), "int-1", "", 10); !errors.Is(err, ErrNoSources) {
		t.Fatalf("search err = %v, want ErrNoSources", err)
	}
}

// The hand-written path stays exactly what it was: validated, stored, uploads
// claimed for the ticket.
func TestCreateRaw(t *testing.T) {
	f, db := newFactory(t, nil)
	db.rows["up1"] = artifacts.Meta{ID: "up1", Scope: artifacts.ScopeOrphan, Filename: "design.md"}

	tk, err := f.Create(context.Background(), confstore.Ticket{
		ProjectID: testProject, Title: "T", Goal: "g", ArtifactIDs: []string{"up1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.SourceKey != "" || tk.SourceID != "" {
		t.Errorf("hand-written ticket carries provenance: %+v", tk)
	}
	if got := db.rows["up1"]; got.Scope != artifacts.ScopeTicketGoal || got.RunID != "" {
		t.Errorf("upload = %+v, want claimed for the ticket", got)
	}
}

// An edit must not disown an imported ticket, even though the edit DTO knows
// nothing about provenance.
func TestUpdateKeepsProvenance(t *testing.T) {
	f, _ := newFactory(t, devSource())
	tk, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := f.Update(context.Background(), tk.ID, confstore.Ticket{
		ProjectID: testProject, Title: "renamed", Goal: "g",
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.SourceKey != "PROJ-7" || upd.SourceURL == "" || upd.SourceKind != "faketracker" {
		t.Errorf("provenance after edit = %+v", upd)
	}
}
