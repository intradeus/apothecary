package tickets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

func mrInfo() repoport.MRInfo {
	return repoport.MRInfo{Ref: repoport.MRRef{IID: 4, URL: "https://git.example/g/p/-/merge_requests/4"}, Title: "Add x", Body: "  why  ",
		Author: "alice", Source: "feature", Target: "main"}
}

func TestFindByKeyStaysInItsProject(t *testing.T) {
	f, _ := newFactory(t, nil)
	if _, found, err := f.FindByKey(context.Background(), "other-project", mrInfo().Ref.URL); err != nil || found {
		t.Errorf("another project's key: %v, %v", found, err)
	}
}

// failingDB is a config store whose ticket lookups are down.
type failingDB struct {
	confstore.DB
	err error
}

func (d failingDB) GetTicketBySource(context.Context, string, string, string) (confstore.Ticket, error) {
	return confstore.Ticket{}, d.err
}

func (d failingDB) GetTicketByProjectKey(context.Context, string, string) (confstore.Ticket, error) {
	return confstore.Ticket{}, d.err
}

func TestFailingLookupReachesTheCaller(t *testing.T) {
	f, _ := newFactory(t, devSource())
	boom := errors.New("db down")
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	f.Conf = confstore.New(failingDB{DB: f.Conf.DB(), err: boom}, key)
	ctx := context.Background()
	if _, _, err := f.FindByKey(ctx, testProject, "k"); !errors.Is(err, boom) {
		t.Errorf("find = %v", err)
	}
	// A duplicate check that cannot run must not import blindly.
	if _, _, err := f.CreateFromSource(ctx, "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil); !errors.Is(err, boom) {
		t.Errorf("import = %v", err)
	}
}

func TestSourceResolution(t *testing.T) {
	ctx := context.Background()
	f, _ := newFactory(t, devSource())
	if _, _, err := f.source(ctx, "  "); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("no id = %v", err)
	}
	boom := errors.New("integration gone")
	f.Sources = stubSources{err: boom}
	if _, _, err := f.CreateFromSource(ctx, "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil); !errors.Is(err, boom) {
		t.Errorf("import = %v", err)
	}
	f.Sources = kindFails{stubSources{src: devSource()}, boom}
	if _, err := f.Search(ctx, "int-1", "x", 5); !errors.Is(err, boom) {
		t.Errorf("kind = %v", err)
	}
}

type kindFails struct {
	stubSources
	err error
}

func (k kindFails) TicketSourceKind(context.Context, string) (string, error) { return "", k.err }

func TestSearchAndFetchServeThePicker(t *testing.T) {
	ctx := context.Background()
	f, _ := newFactory(t, devSource())
	found, err := f.Search(ctx, "int-1", "farewell", 5)
	if err != nil || len(found) != 1 || found[0].Key != "PROJ-7" {
		t.Errorf("search = %+v, %v", found, err)
	}
	got, err := f.Fetch(ctx, "int-1", "PROJ-7")
	if err != nil || len(got.Attachments) != 1 {
		t.Errorf("fetch = %+v, %v", got, err)
	}
	if _, err := f.Fetch(ctx, " ", "PROJ-7"); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("fetch with no source = %v", err)
	}
}

func TestCreateFromSourceFetchFailure(t *testing.T) {
	f, _ := newFactory(t, devSource())
	_, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-404", confstore.Ticket{ProjectID: testProject}, nil)
	if err == nil || !strings.Contains(err.Error(), "fetch PROJ-404") {
		t.Errorf("err = %v", err)
	}
}

// keyless is a tracker whose issue answers with no key of its own.
type keyless struct{ *fake.TicketSource }

func (k keyless) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	t, err := k.TicketSource.Fetch(ctx, key)
	t.Key = ""
	return t, err
}

func TestCreateFromSourceKeepsTheAskedKeyWhenTheTrackerGivesNone(t *testing.T) {
	f, _ := newFactory(t, keyless{devSource()})
	got, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil || got.SourceKey != "PROJ-7" {
		t.Errorf("ticket = %+v, %v", got, err)
	}
}

// An import into a project that is not there is refused, by name, and an
// issue with no project is no duplicate of anything.
func TestCreateFromSourceNeedsItsProject(t *testing.T) {
	f, _ := newFactory(t, devSource())
	for _, project := range []string{"gone", ""} {
		_, _, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{ProjectID: project}, nil)
		if err == nil || !strings.Contains(err.Error(), "project") {
			t.Errorf("project %q: %v", project, err)
		}
	}
}

func TestImportOfAnIssueWithNoFiles(t *testing.T) {
	src := devSource()
	src.Tickets[0].Attachments = nil
	f, att := newFactory(t, src)
	got, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil || warnings != nil || len(ticketAttachments(t, att, got.ID)) != 0 {
		t.Errorf("ticket %+v, warnings %v, %v", got, warnings, err)
	}
}

func TestImportWithNoFileStoreWarns(t *testing.T) {
	f, _ := newFactory(t, devSource())
	f.Attachments = nil
	got, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil)
	if err != nil || got.ID == "" {
		t.Fatalf("the ticket still lands: %+v, %v", got, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "1 attachment(s) skipped") {
		t.Errorf("warnings = %v", warnings)
	}
}

// brokenOpen is a tracker whose files cannot be read, one way or another.
type brokenOpen struct {
	*fake.TicketSource
	open func() (io.ReadCloser, error)
}

func (b brokenOpen) OpenAttachment(context.Context, trackerport.SourceAttachment) (io.ReadCloser, error) {
	return b.open()
}

// An attachment that cannot be copied is a warning naming the file, never the
// loss of the import.
func TestImportAttachmentFailuresAreWarnings(t *testing.T) {
	for _, tc := range []struct {
		name string
		mime string
		open func() (io.ReadCloser, error)
		want string
	}{
		{"unreadable", "text/markdown", func() (io.ReadCloser, error) { return nil, errors.New("403 from the tracker") }, "spec.md: 403 from the tracker"},
		{"too large", "text/markdown", func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(make([]byte, 2<<20))), nil }, "too large"},
		{"refused type", "application/x-msdownload", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("MZ")), nil }, "not on the upload allowlist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := devSource()
			src.Tickets[0].Attachments[0].Mime = tc.mime
			f, att := newFactory(t, brokenOpen{src, tc.open})
			var logged bytes.Buffer
			f.Log = slog.New(slog.NewTextHandler(&logged, nil))
			got, warnings, err := f.CreateFromSource(context.Background(), "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], tc.want) {
				t.Errorf("warnings = %v", warnings)
			}
			if n := len(ticketAttachments(t, att, got.ID)); n != 0 {
				t.Errorf("%d attachments claimed", n)
			}
		})
	}
}

// A store that refuses the claim loses nothing: the ticket stands, the
// attachment stays claimable, and the failure is logged.
func TestClaimFailureDoesNotFailTheWrite(t *testing.T) {
	f, _ := newFactory(t, nil)
	var logged bytes.Buffer
	f.Log = slog.New(slog.NewTextHandler(&logged, nil))
	f.Attachments = filestore.NewAttachments(failingClaims{filestore.NewMem()}, filestore.NewBlobs(t.TempDir()), 1<<20, []string{"text/*"})
	got, err := f.Create(context.Background(), confstore.Ticket{ProjectID: testProject, Title: "T", Goal: "g"}, []string{"att-1"})
	if err != nil || got.ID == "" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	if !strings.Contains(logged.String(), "claim ticket attachments") {
		t.Errorf("log = %q", logged.String())
	}
}

func TestUpdateAndDeleteFailures(t *testing.T) {
	ctx := context.Background()
	f, _ := newFactory(t, nil)
	if _, err := f.Update(ctx, "missing", confstore.Ticket{ProjectID: testProject, Title: "T"}, nil); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("update = %v", err)
	}
	f.Attachments = filestore.NewAttachments(failingClaims{filestore.NewMem()}, filestore.NewBlobs(t.TempDir()), 1<<20, nil)
	tk, err := f.Create(ctx, confstore.Ticket{ProjectID: testProject, Title: "T", Goal: "g"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Delete(ctx, tk.ID); err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Errorf("delete = %v", err)
	}
	if _, err := f.Conf.GetTicket(ctx, tk.ID); err != nil {
		t.Errorf("a ticket whose attachments could not go must stay: %v", err)
	}
	f.Attachments = nil
	if err := f.Delete(ctx, tk.ID); err != nil {
		t.Errorf("no file store: %v", err)
	}
}

func TestCreateFailure(t *testing.T) {
	f, _ := newFactory(t, nil)
	if _, err := f.Create(context.Background(), confstore.Ticket{ProjectID: "gone", Title: "T", Goal: "g"}, nil); err == nil {
		t.Error("a ticket with no project is refused")
	}
}

// failingClaims is an attachment store refusing claims and deletes.
type failingClaims struct{ *filestore.Mem }

func (failingClaims) ClaimAttachments(context.Context, string, []string, string, string, string) error {
	return errors.New("claim refused")
}

func (failingClaims) DeleteAttachmentsForTicket(context.Context, string) error {
	return errors.New("delete refused")
}
