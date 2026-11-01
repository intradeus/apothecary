package tickets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
)

const testProject = "prj-1"

// stubSources serves one ticket source under any id.
type stubSources struct {
	src  trackerport.TicketSource
	kind string
	err  error
}

func (s stubSources) TicketSource(context.Context, string) (trackerport.TicketSource, error) {
	return s.src, s.err
}
func (s stubSources) TicketSourceKind(context.Context, string) (string, error) {
	return s.kind, s.err
}

func newFactory(t *testing.T, src trackerport.TicketSource) (*Factory, *filestore.Attachments) {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(confstore.NewMem(), key)
	conftest.SeedProject(t, conf, confstore.Project{ID: testProject, Name: "p"})
	att := filestore.NewAttachments(filestore.NewMem(), filestore.NewBlobs(t.TempDir()),
		1<<20, []string{"text/*", "image/png"})
	f := &Factory{
		Conf:        conf,
		Attachments: att,
	}
	if src != nil {
		f.Sources = stubSources{src: src, kind: "faketracker"}
	}
	return f, att
}

func devSource() *fake.TicketSource {
	return &fake.TicketSource{
		Tickets: []trackerport.SourceTicket{{
			Key: "PROJ-7", Title: "Add a Farewell function",
			Body: "The greeter only says hello.", URL: "https://tracker.test/browse/PROJ-7",
			Status: "To Do", Type: "Story",
			Attachments: []trackerport.SourceAttachment{
				{ID: "a1", Filename: "spec.md", Mime: "text/markdown", Size: 5},
			},
		}},
		Blobs: map[string][]byte{"a1": []byte("hello")},
	}
}

func ticketAttachments(t *testing.T, att *filestore.Attachments, ticketID string) []filestore.Attachment {
	t.Helper()
	out, err := att.ForTicket(context.Background(), ticketID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- tests ------------------------------------------------------------------

// The import copies title, body, provenance AND the attachment bytes, and the
// resulting files are claimed by the ticket (shared by every run) exactly
// like hand-attached uploads.
func TestCreateFromSourceCopiesEverything(t *testing.T) {
	f, att := newFactory(t, devSource())

	tk, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject}, nil)
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
	// No branch given: named from the issue — its type, key and title.
	if tk.BaseBranch != "feat/proj-7-add-a-farewell-function" {
		t.Errorf("base branch = %q", tk.BaseBranch)
	}
	if tk.ChangeType != trackerport.ChangeFeat || tk.SourceType != "Story" {
		t.Errorf("type = %q from %q, want feat from Story", tk.ChangeType, tk.SourceType)
	}
	atts := ticketAttachments(t, att, tk.ID)
	if len(atts) != 1 || atts[0].Filename != "spec.md" || atts[0].RunID != "" {
		t.Fatalf("attachments = %+v, want spec.md claimed for the ticket", atts)
	}
	if atts[0].Size != 5 {
		t.Errorf("imported %d bytes, want the attachment's 5", atts[0].Size)
	}
	data, _, err := att.Bytes(context.Background(), atts[0].ID)
	if err != nil || string(data) != "hello" {
		t.Errorf("imported bytes = %q (%v)", data, err)
	}
}

// The issue's type comes across as the ticket's change type — the
// integration's mapping first, else read from the type's name — and names the
// branch; a type the operator picked on the form wins over both.
func TestCreateFromSourceCarriesTheType(t *testing.T) {
	src := devSource()
	src.Tickets[0].Type = "Bug"
	f, _ := newFactory(t, src)
	ctx := context.Background()
	tk, _, err := f.CreateFromSource(ctx, "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tk.ChangeType != trackerport.ChangeFix || tk.SourceType != "Bug" || tk.BaseBranch != "fix/proj-7-add-a-farewell-function" {
		t.Errorf("guessed = %q from %q, branch %q", tk.ChangeType, tk.SourceType, tk.BaseBranch)
	}
	got, err := f.Conf.GetTicket(ctx, tk.ID)
	if err != nil || got.ChangeType != trackerport.ChangeFix || got.SourceType != "Bug" {
		t.Errorf("stored = %+v (%v)", got, err)
	}

	src2 := devSource()
	src2.Tickets[0].Type, src2.Tickets[0].ChangeType = "Bug", trackerport.ChangeChore
	f2, _ := newFactory(t, src2)
	if tk, _, err := f2.CreateFromSource(ctx, "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil); err != nil || tk.ChangeType != trackerport.ChangeChore {
		t.Errorf("mapped = %q (%v), want the integration's chore", tk.ChangeType, err)
	}

	f3, _ := newFactory(t, src)
	tk, _, err = f3.CreateFromSource(ctx, "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject, ChangeType: trackerport.ChangeFeat}, nil)
	if err != nil || tk.ChangeType != trackerport.ChangeFeat || tk.BaseBranch != "feat/proj-7-add-a-farewell-function" {
		t.Errorf("picked = %q, branch %q (%v)", tk.ChangeType, tk.BaseBranch, err)
	}
}

// A change type is one of the three, or the write is refused.
func TestChangeTypeIsValidated(t *testing.T) {
	f, _ := newFactory(t, nil)
	_, err := f.Create(context.Background(), confstore.Ticket{ProjectID: testProject, Title: "T", Goal: "g", ChangeType: "bugfix"}, nil)
	if !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("err = %v, want invalid", err)
	}
	tk, err := f.Create(context.Background(), confstore.Ticket{ProjectID: testProject, Title: "Crash", Goal: "g", ChangeType: "FIX"}, nil)
	if err != nil || tk.ChangeType != trackerport.ChangeFix || tk.BaseBranch != "fix/crash" {
		t.Errorf("ticket = %+v (%v)", tk, err)
	}
}

// Anything the operator typed into the prefilled form wins over the tracker's
// version — the import is a starting point, not a lock.
func TestCreateFromSourceRespectsOverrides(t *testing.T) {
	f, _ := newFactory(t, devSource())

	tk, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{
		ProjectID: testProject, Title: "My own title", Goal: "my own goal",
		BaseBranch: "feat/proj-7-farewell",
	}, nil)
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
		confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject}, nil)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second import err = %v, want ErrDuplicate", err)
	}
	if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("err %q does not name the existing ticket %s", err, first.ID)
	}
}

// One unusable attachment must not sink the whole import: it is skipped,
// named in the warnings, and everything else still lands.
func TestCreateFromSourceWarnsOnSkippedAttachment(t *testing.T) {
	src := devSource()
	src.Tickets[0].Attachments = append(src.Tickets[0].Attachments,
		trackerport.SourceAttachment{ID: "a2", Filename: "archive.zip", Mime: "application/zip", Size: 3})
	src.Blobs["a2"] = []byte("zip")
	f, att := newFactory(t, src)

	tk, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if atts := ticketAttachments(t, att, tk.ID); len(atts) != 1 {
		t.Errorf("attachments = %+v, want only the allowed file", atts)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "archive.zip") {
		t.Fatalf("warnings = %v, want one naming archive.zip", warnings)
	}
}

// With no tracker wired at all, importing says so instead of failing obscurely.
func TestImportWithoutSourcesIsHonest(t *testing.T) {
	f, _ := newFactory(t, nil)
	_, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject}, nil)
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
	f, att := newFactory(t, nil)
	up, err := att.Save(context.Background(), "", "design.md", "text/markdown", strings.NewReader("# d"))
	if err != nil {
		t.Fatal(err)
	}

	tk, err := f.Create(context.Background(), confstore.Ticket{
		ProjectID: testProject, Title: "T", Goal: "g",
	}, []string{up.ID})
	if err != nil {
		t.Fatal(err)
	}
	if tk.SourceKey != "" || tk.SourceID != "" {
		t.Errorf("hand-written ticket carries provenance: %+v", tk)
	}
	if tk.BaseBranch != "feat/t" {
		t.Errorf("base branch = %q, want one named from the title", tk.BaseBranch)
	}
	if got, _ := att.Get(context.Background(), up.ID); got.TicketID != tk.ID || got.RunID != "" {
		t.Errorf("upload = %+v, want claimed for the ticket", got)
	}
}

// An edit must not disown an imported ticket, even though the edit DTO knows
// nothing about provenance.
func TestUpdateKeepsProvenance(t *testing.T) {
	f, _ := newFactory(t, devSource())
	tk, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil {
		t.Fatal(err)
	}
	upd, err := f.Update(context.Background(), tk.ID, confstore.Ticket{
		ProjectID: testProject, Title: "renamed", Goal: "g",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if upd.SourceKey != "PROJ-7" || upd.SourceURL == "" || upd.SourceKind != "faketracker" {
		t.Errorf("provenance after edit = %+v", upd)
	}
}

// Delete removes the ticket AND its attachment rows — the old leak.
func TestDeleteReclaimsAttachments(t *testing.T) {
	f, att := newFactory(t, devSource())
	tk, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7",
		confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil {
		t.Fatal(err)
	}
	atts := ticketAttachments(t, att, tk.ID)
	if len(atts) != 1 {
		t.Fatalf("setup: attachments = %+v", atts)
	}
	if err := f.Delete(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Conf.GetTicket(context.Background(), tk.ID); err == nil {
		t.Error("ticket row survived delete")
	}
	if _, err := att.Get(context.Background(), atts[0].ID); !errors.Is(err, filestore.ErrNotFound) {
		t.Errorf("attachment row survived ticket delete: %v", err)
	}
}
