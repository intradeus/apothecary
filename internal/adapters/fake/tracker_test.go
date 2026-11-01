package fake

import (
	"errors"
	"io"
	"strings"
	"testing"

	"apothecary/internal/ports/trackerport"
)

func tracker() *TicketSource {
	return &TicketSource{Tickets: []trackerport.SourceTicket{
		{Key: "DEV-1", Title: "Login page", Body: "users cannot sign in", Status: "To Do",
			Attachments: []trackerport.SourceAttachment{{ID: "a1"}}},
		{Key: "DEV-2", RemoteID: "1002", Title: "Logout", Body: "button", Status: "In Progress", Labels: []string{"ui"}},
		{Key: "OPS-3", Title: "Rotate keys", Body: "quarterly", Status: "Done"},
	}, Blobs: map[string][]byte{"a1": []byte("bytes")}}
}

func TestSearchAndFetch(t *testing.T) {
	s := tracker()
	cases := []struct {
		q     string
		limit int
		want  string
	}{
		{"", 0, "DEV-1,DEV-2,OPS-3"},
		{"LOG", 0, "DEV-1,DEV-2"},
		{"quarterly", 0, "OPS-3"},
		{"ops-3", 0, "OPS-3"},
		{"", 2, "DEV-1,DEV-2"},
		{"nothing", 0, ""},
	}
	for _, c := range cases {
		got, err := s.Search(bgCtx, c.q, c.limit)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, k := range got {
			keys = append(keys, k.Key)
			if k.Attachments != nil {
				t.Errorf("%q: search carried attachments", c.q)
			}
		}
		if strings.Join(keys, ",") != c.want {
			t.Errorf("Search(%q, %d) = %v", c.q, c.limit, keys)
		}
	}
	got, err := s.Fetch(bgCtx, "dev-1")
	if err != nil || got.Key != "DEV-1" || len(got.Attachments) != 1 {
		t.Errorf("fetch = %+v, %v", got, err)
	}
	if _, err := s.Fetch(bgCtx, "NOPE"); !errors.Is(err, trackerport.ErrTicketNotFound) {
		t.Errorf("fetched nothing = %v, want ErrTicketNotFound", err)
	}
	// The stable id finds the ticket too, and a copy cannot write through.
	byID, err := s.Fetch(bgCtx, "1002")
	if err != nil || byID.Key != "DEV-2" {
		t.Errorf("fetch by remote id = %+v, %v", byID, err)
	}
	byID.Labels[0] = "changed"
	if again, _ := s.Fetch(bgCtx, "DEV-2"); again.Labels[0] != "ui" {
		t.Error("Fetch shares the labels")
	}
	many, err := s.FetchMany(bgCtx, []string{"OPS-3", "NOPE", "dev-1"})
	if err != nil || len(many) != 2 || many[0].Key != "OPS-3" || many[1].Key != "DEV-1" {
		t.Errorf("fetch many = %+v, %v", many, err)
	}
	rc, err := s.OpenAttachment(bgCtx, trackerport.SourceAttachment{ID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, _ := io.ReadAll(rc); string(b) != "bytes" {
		t.Errorf("blob = %q", b)
	}
	if _, err := s.OpenAttachment(bgCtx, trackerport.SourceAttachment{ID: "ghost"}); err == nil {
		t.Error("opened nothing")
	}
}

func TestErrFailsEverything(t *testing.T) {
	s := tracker()
	boom := errors.New("tracker down")
	s.Err = boom
	ro := ReadOnlyTicketSource{Src: s}
	ops := map[string]func() error{
		"test":      func() error { return s.Test(bgCtx) },
		"search":    func() error { _, err := s.Search(bgCtx, "", 0); return err },
		"fetch":     func() error { _, err := s.Fetch(bgCtx, "DEV-1"); return err },
		"open":      func() error { _, err := s.OpenAttachment(bgCtx, trackerport.SourceAttachment{}); return err },
		"comments":  func() error { _, err := s.ListComments(bgCtx, "DEV-1"); return err },
		"many":      func() error { _, err := s.FetchMany(bgCtx, nil); return err },
		"statuses":  func() error { _, err := s.Statuses(bgCtx); return err },
		"types":     func() error { _, err := s.Types(bgCtx); return err },
		"fields":    func() error { _, err := s.Fields(bgCtx, "DEV-1", nil); return err },
		"ro test":   func() error { return ro.Test(bgCtx) },
		"ro search": func() error { _, err := ro.Search(bgCtx, "", 0); return err },
		"ro fetch":  func() error { _, err := ro.Fetch(bgCtx, "DEV-1"); return err },
		"ro open":   func() error { _, err := ro.OpenAttachment(bgCtx, trackerport.SourceAttachment{}); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, boom) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func TestReadOnlyServesTheCorpus(t *testing.T) {
	s := tracker()
	ro := ReadOnlyTicketSource{Src: s}
	if err := ro.Test(bgCtx); err != nil {
		t.Error(err)
	}
	if got, _ := ro.Search(bgCtx, "rotate", 0); len(got) != 1 {
		t.Errorf("search = %+v", got)
	}
	if got, err := ro.Fetch(bgCtx, "OPS-3"); err != nil || got.Key != "OPS-3" {
		t.Errorf("fetch = %+v, %v", got, err)
	}
	if rc, err := ro.OpenAttachment(bgCtx, trackerport.SourceAttachment{ID: "a1"}); err != nil {
		t.Error(err)
	} else {
		rc.Close()
	}
}

func TestWritesFailOnErrAndWriteErrAndUnknownKeys(t *testing.T) {
	writes := func(s *TicketSource, key string) map[string]func() error {
		return map[string]func() error{
			"transition": func() error { return s.Transition(bgCtx, key, "Done") },
			"comment":    func() error { return s.Comment(bgCtx, key, "b") },
			"fields":     func() error { return s.SetFields(bgCtx, key, nil) },
			"link":       func() error { return s.AddLink(bgCtx, key, trackerport.TicketLink{}) },
			"attach":     func() error { _, err := s.Attach(bgCtx, key, "n", "", nil); return err },
		}
	}
	boom := errors.New("x")
	for name, op := range writes(&TicketSource{Err: boom}, "DEV-1") {
		if err := op(); !errors.Is(err, boom) {
			t.Errorf("Err: %s = %v", name, err)
		}
	}
	s := tracker()
	s.WriteErr = boom
	for name, op := range writes(s, "DEV-1") {
		if err := op(); !errors.Is(err, boom) {
			t.Errorf("WriteErr: %s = %v", name, err)
		}
	}
	if len(s.Calls) != 0 {
		t.Errorf("a refused write was recorded: %v", s.Calls)
	}
	for name, op := range writes(tracker(), "GHOST-1") {
		if err := op(); err == nil || !strings.Contains(err.Error(), "no ticket") {
			t.Errorf("unknown key: %s = %v", name, err)
		}
	}
	u := tracker()
	if _, err := u.ListComments(bgCtx, "GHOST-1"); err == nil {
		t.Error("comments of nothing")
	}
	if _, err := u.Fields(bgCtx, "GHOST-1", nil); err == nil {
		t.Error("fields of nothing")
	}
}

func TestTransition(t *testing.T) {
	s := tracker()
	if err := s.Transition(bgCtx, "dev-1", "to do"); err != nil {
		t.Fatal(err)
	}
	if s.Calls[0] != "transition DEV-1 To Do (already there)" {
		t.Errorf("calls = %v", s.Calls)
	}
	if err := s.Transition(bgCtx, "DEV-1", "Done"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Fetch(bgCtx, "DEV-1"); got.Status != "Done" || got.StatusID != "4" {
		t.Errorf("status = %q (%q)", got.Status, got.StatusID)
	}
	// A status the list does not have leaves no id.
	if err := s.Transition(bgCtx, "DEV-1", "Limbo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Fetch(bgCtx, "DEV-1"); got.Status != "Limbo" || got.StatusID != "" {
		t.Errorf("status = %q (%q)", got.Status, got.StatusID)
	}
	// Reachable's vocabulary wins over the flow's spelling.
	s = tracker()
	s.Reachable = map[string][]string{"To Do": {"In Progress"}}
	if err := s.Transition(bgCtx, "DEV-1", "in progress"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Fetch(bgCtx, "DEV-1"); got.Status != "In Progress" {
		t.Errorf("status = %q", got.Status)
	}
	var un *trackerport.ErrStatusUnreachable
	err := s.Transition(bgCtx, "DEV-1", "Done")
	if !errors.As(err, &un) || un.From != "In Progress" || un.To != "Done" {
		t.Errorf("unreachable = %v", err)
	}
}

func TestMetadata(t *testing.T) {
	s := tracker()
	statuses, err := s.Statuses(bgCtx)
	if err != nil || len(statuses) != len(DefaultStatuses) || statuses[3] != (trackerport.RemoteStatus{ID: "4", Name: "Done", Category: trackerport.CategoryDone}) {
		t.Errorf("statuses = %+v, %v", statuses, err)
	}
	types, err := s.Types(bgCtx)
	if err != nil || len(types) != len(DefaultTypes) {
		t.Errorf("types = %+v, %v", types, err)
	}
	statuses[0].Name, types[0].Name = "changed", "changed"
	if DefaultStatuses[0].Name != "To Do" || DefaultTypes[0].Name != "Story" {
		t.Error("the lists share storage with the defaults")
	}
	s.RemoteStatuses = []trackerport.RemoteStatus{{ID: "open", Name: "Open"}}
	s.RemoteTypes = []trackerport.RemoteType{{ID: "issue", Name: "Issue"}}
	if got, _ := s.Statuses(bgCtx); len(got) != 1 || got[0].ID != "open" {
		t.Errorf("configured statuses = %+v", got)
	}
	if got, _ := s.Types(bgCtx); len(got) != 1 || got[0].ID != "issue" {
		t.Errorf("configured types = %+v", got)
	}
	if err := s.Transition(bgCtx, "DEV-1", "open"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Fetch(bgCtx, "DEV-1"); got.StatusID != "open" {
		t.Errorf("status id = %q", got.StatusID)
	}
}

func TestComments(t *testing.T) {
	s := tracker()
	s.Thread = map[string][]trackerport.Comment{"DEV-1": {{ID: "h1", Author: "ann", Body: "human"}}}
	if err := s.Comment(bgCtx, "dev-1", "one"); err != nil {
		t.Fatal(err)
	}
	s.Comment(bgCtx, "DEV-1", "two")
	got, err := s.ListComments(bgCtx, "DEV-1")
	if err != nil || len(got) != 3 || got[0].ID != "h1" || got[1].Body != "one" || got[2].Body != "two" {
		t.Fatalf("comments = %+v, %v", got, err)
	}
	if got[1].ID == got[2].ID || got[1].ID == "" || got[1].Author != "apothecary" || got[1].Created.IsZero() {
		t.Errorf("posted = %+v", got[1:])
	}
	if strings.Join(s.Posted["DEV-1"], ",") != "one,two" {
		t.Errorf("posted = %v", s.Posted)
	}
	got[0].Body = "changed"
	if again, _ := s.ListComments(bgCtx, "DEV-1"); again[0].Body != "human" {
		t.Error("ListComments shares storage")
	}
}

func TestFields(t *testing.T) {
	s := tracker()
	if err := s.SetFields(bgCtx, "dev-1", map[string]string{"b": "2", "a": "1"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Calls, ",") != "set DEV-1 a=1,set DEV-1 b=2" {
		t.Errorf("calls = %v", s.Calls)
	}
	got, err := s.Fields(bgCtx, "DEV-1", []string{"a", "unset"})
	if err != nil || got["a"] != "1" || got["unset"] != "" {
		t.Errorf("fields = %v, %v", got, err)
	}
	// Labels are the ticket's own: a sync reads them back.
	if err := s.SetFields(bgCtx, "DEV-1", map[string]string{"Labels": "x, ,y"}); err != nil {
		t.Fatal(err)
	}
	if tk, _ := s.Fetch(bgCtx, "DEV-1"); strings.Join(tk.Labels, ",") != "x,y" {
		t.Errorf("labels = %q", tk.Labels)
	}
	s.KnownFields = []string{"Priority"}
	if err := s.SetFields(bgCtx, "DEV-1", map[string]string{"priority": "high"}); err != nil {
		t.Errorf("known field, other case = %v", err)
	}
	var un *trackerport.ErrFieldUnknown
	if err := s.SetFields(bgCtx, "DEV-1", map[string]string{"ghost": "x"}); !errors.As(err, &un) || un.Field != "ghost" {
		t.Errorf("unknown write = %v", err)
	}
	if _, err := s.Fields(bgCtx, "DEV-1", []string{"ghost"}); !errors.As(err, &un) {
		t.Errorf("unknown read = %v", err)
	}
}

func TestLinksUpsertOnURL(t *testing.T) {
	s := tracker()
	s.AddLink(bgCtx, "dev-1", trackerport.TicketLink{Title: "MR", URL: "https://x/1"})
	s.AddLink(bgCtx, "DEV-1", trackerport.TicketLink{Title: "Renamed", URL: "https://x/1"})
	s.AddLink(bgCtx, "DEV-1", trackerport.TicketLink{Title: "Other", URL: "https://x/2"})
	if l := s.Linked["DEV-1"]; len(l) != 2 || l[0].Title != "Renamed" {
		t.Errorf("links = %+v", l)
	}
}

func TestAttachIsALinkNeverAnImage(t *testing.T) {
	s := tracker()
	a, err := s.Attach(bgCtx, "dev-1", "shot.png", "image/png", []byte("x"))
	if err != nil || !strings.HasPrefix(a.Markdown, "[shot.png](") || !strings.Contains(a.URL, "/DEV-1/attachments/1/shot.png") {
		t.Errorf("attach = %+v, %v", a, err)
	}
	if len(s.Attached) != 1 || string(s.Attached[0].Data) != "x" || s.Attached[0].Key != "DEV-1" {
		t.Errorf("attached = %+v", s.Attached)
	}
}
