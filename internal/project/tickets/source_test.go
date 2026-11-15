package tickets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// An integration that does not resolve fails the read, by its error.
func TestAnUnresolvedSourceFailsTheRead(t *testing.T) {
	w := newWorld(t, devSource())
	ctx := context.Background()
	addTracker(t, w.conf, "int-gone")
	if _, err := w.live.List(ctx, "int-gone", trackerport.SearchQuery{}, false); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("list = %v", err)
	}
	if _, err := w.live.List(ctx, "no-such-integration", trackerport.SearchQuery{}, false); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("an unknown integration = %v", err)
	}
}

// brokenOpen is a tracker whose files cannot be read, one way or another.
type brokenOpen struct {
	*countingSource
	open func() (io.ReadCloser, error)
}

func (b brokenOpen) OpenAttachment(context.Context, trackerport.SourceAttachment) (io.ReadCloser, error) {
	return b.open()
}

// A file that cannot be copied never costs a run its start: it is logged
// unless the upload policy refused it, and nothing is claimed.
func TestFileFailuresAtRunStartAreLogged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mime   string
		open   func() (io.ReadCloser, error)
		logged string
	}{
		{"unreadable", "text/markdown", func() (io.ReadCloser, error) { return nil, errors.New("403 from the tracker") }, "403 from the tracker"},
		{"too large", "text/markdown", func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(make([]byte, 2<<20))), nil }, "too large"},
		{"refused type", "application/x-msdownload", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("MZ")), nil }, "not on the upload allowlist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := devSource()
			src.Tickets[0].Attachments[0].Mime = tc.mime
			w := newWorld(t, src)
			w.f.Sources = stubSources{remote: brokenOpen{w.src, tc.open}}
			var logged bytes.Buffer
			w.f.Log = slog.New(slog.NewTextHandler(&logged, nil))
			rec := w.record(t)
			got, found, err := w.f.RunTicket(context.Background(), rec.ID, testProject)
			if err != nil || !found || got.Title != rec.Title {
				t.Fatalf("run ticket = %+v, %v, %v", got, found, err)
			}
			if n := len(ticketAttachments(t, w.att, rec.ID)); n != 0 {
				t.Errorf("%d attachments claimed", n)
			}
			if !strings.Contains(logged.String(), tc.logged) {
				t.Errorf("log = %q, want %q", logged.String(), tc.logged)
			}
		})
	}
}

// A store that refuses the claim loses nothing: the ticket stands, the
// attachment stays claimable, and the failure is logged.
func TestClaimFailureDoesNotFailTheWrite(t *testing.T) {
	w := newWorld(t, devSource())
	var logged bytes.Buffer
	w.f.Log = slog.New(slog.NewTextHandler(&logged, nil))
	w.f.Attachments = filestore.NewAttachments(failingClaims{filestore.NewMem()}, filestore.NewBlobs(t.TempDir()), 1<<20, []string{"text/*"})
	got, err := w.f.Create(context.Background(), confstore.Ticket{UserID: owner, Title: "T", Goal: "g"}, []string{"att-1"})
	if err != nil || got.ID == "" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	if !strings.Contains(logged.String(), "claim ticket attachments") {
		t.Errorf("log = %q", logged.String())
	}
}

func TestUpdateAndDeleteFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	if _, err := w.f.Update(ctx, "missing", confstore.Ticket{Title: "T"}, nil); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("update = %v", err)
	}
	w.f.Attachments = filestore.NewAttachments(failingClaims{filestore.NewMem()}, filestore.NewBlobs(t.TempDir()), 1<<20, nil)
	tk, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "T", Goal: "g"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.f.Delete(ctx, tk.ID); err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Errorf("delete = %v", err)
	}
	if _, err := w.conf.GetTicket(ctx, tk.ID); err != nil {
		t.Errorf("a ticket whose attachments could not go must stay: %v", err)
	}
	w.f.Attachments = nil
	if err := w.f.Delete(ctx, tk.ID); err != nil {
		t.Errorf("no file store: %v", err)
	}
}

func TestCreateFailure(t *testing.T) {
	w := newWorld(t, devSource())
	if _, err := w.f.Create(context.Background(), confstore.Ticket{Title: "T", Goal: "g"}, nil); err == nil {
		t.Error("a ticket with no owner is refused")
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
