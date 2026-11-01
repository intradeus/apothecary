package fake

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/repoport"
)

var bgCtx = context.Background()

func repoWithMR(t *testing.T) (*RepoHost, repoport.MRRef) {
	t.Helper()
	f := &RepoHost{NewHead: "h1"}
	ref, err := f.CreateDraftMR(bgCtx, repoport.RepoRef{DefaultBranch: "main"}, "feat/x", "", "T", "B")
	if err != nil {
		t.Fatal(err)
	}
	return f, ref
}

func TestMRInfoAndListing(t *testing.T) {
	f, ref := repoWithMR(t)
	got, err := f.GetMR(bgCtx, ref.IID)
	if err != nil || got.Author != "apothecary" || got.Target != "main" || got.Source != "feat/x" || got.Head != "h1" ||
		got.Body != "B" || got.HeadRef != "refs/merge-requests/1/head" {
		t.Fatalf("info = %+v, %v", got, err)
	}
	f.User = "bot"
	if who, _ := f.Whoami(bgCtx); who != "bot" {
		t.Errorf("whoami = %q", who)
	}
	if got, _ := f.GetMR(bgCtx, ref.IID); got.Author != "bot" {
		t.Errorf("author = %q", got.Author)
	}
	other := f.AddOpenMR("alice", "Theirs", "topic", "main", "h2")
	if got, _ := f.GetMR(bgCtx, other.IID); got.Author != "alice" || got.Head != "h2" {
		t.Errorf("foreign = %+v", got)
	}
	third := f.AddOpenMR("bob", "Closed", "x", "main", "h3")
	f.SetMRState(third.IID, repoport.MRClosed)
	list, err := f.ListMRs(bgCtx)
	if err != nil || len(list) != 2 || list[0].Ref.IID != other.IID || list[1].Ref.IID != ref.IID {
		t.Errorf("list = %+v, %v (newest open first)", list, err)
	}
	if _, err := f.GetMR(bgCtx, 99); err == nil {
		t.Error("unknown MR found")
	}
	if snap := f.SnapshotMRs(); len(snap) != 3 || snap[0].Title != "T" {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestMRStateErrFailsEveryRead(t *testing.T) {
	f, ref := repoWithMR(t)
	boom := errors.New("host down")
	f.SetMRStateErr(boom)
	ops := map[string]func() error{
		"get":      func() error { _, err := f.GetMR(bgCtx, 1); return err },
		"list":     func() error { _, err := f.ListMRs(bgCtx); return err },
		"status":   func() error { _, err := f.MRStatus(bgCtx, ref); return err },
		"checks":   func() error { _, err := f.MRChecks(bgCtx, ref, "h"); return err },
		"failures": func() error { _, err := f.CIFailures(bgCtx, ref, "h"); return err },
		"merge":    func() error { return f.MergeMR(bgCtx, ref, repoport.MergeOptions{}) },
		"approval": func() error { _, err := f.MRApproval(bgCtx, ref); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, boom) {
			t.Errorf("%s = %v", name, err)
		}
	}
	f.SetMRStateErr(nil)
	if _, err := f.MRStatus(bgCtx, ref); err != nil {
		t.Errorf("cleared = %v", err)
	}
}

func TestUnknownMRIsAnError(t *testing.T) {
	f := &RepoHost{}
	ghost := repoport.MRRef{IID: 9}
	ops := map[string]func() error{
		"labels":   func() error { return f.AddMRLabels(bgCtx, ghost, nil) },
		"status":   func() error { _, err := f.MRStatus(bgCtx, ghost); return err },
		"checks":   func() error { _, err := f.MRChecks(bgCtx, ghost, ""); return err },
		"failures": func() error { _, err := f.CIFailures(bgCtx, ghost, ""); return err },
		"merge":    func() error { return f.MergeMR(bgCtx, ghost, repoport.MergeOptions{}) },
		"review":   func() error { _, err := f.PostReview(bgCtx, ghost, repoport.Review{}); return err },
		"approval": func() error { _, err := f.MRApproval(bgCtx, ghost); return err },
		"threads":  func() error { _, err := f.MRThreads(bgCtx, ghost); return err },
		"reply":    func() error { return f.ReplyThread(bgCtx, ghost, "t", "b") },
		"resolve":  func() error { return f.ResolveThread(bgCtx, ghost, "t") },
	}
	for name, op := range ops {
		if err := op(); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s = %v", name, err)
		}
	}
	// Test controls on nothing are no-ops, not panics.
	f.SetMergeable(9, repoport.MergeClean, "")
	f.SetHead(9, "")
	f.SetChecks(9)
	f.SetCIFailures(9)
	f.SetMergeableNow(9, true)
	f.SetMergeRefusal(9, "x")
	f.SetApprovedBy(9)
	f.SetTargetSHA(9, "")
	f.AddComment(9, "t", "p", "a", "b", time.Now())
	if f.SnapshotThreads(9) != nil {
		t.Error("threads of nothing")
	}
}

func TestCreateFailureAndUploads(t *testing.T) {
	boom := errors.New("nope")
	f := &RepoHost{CreateErr: boom}
	if _, err := f.CreateDraftMR(bgCtx, repoport.RepoRef{}, "b", "main", "", ""); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	f.CreateErr = nil
	img, err := f.UploadAttachment(bgCtx, repoport.RepoRef{}, "shot.png", "image/png", []byte("x"))
	if err != nil || !strings.HasPrefix(img.Markdown, "![shot.png](") || !strings.Contains(img.URL, "/uploads/1/shot.png") {
		t.Errorf("image = %+v, %v", img, err)
	}
	doc, _ := f.UploadAttachment(bgCtx, repoport.RepoRef{}, "a.pdf", "application/pdf", nil)
	if !strings.HasPrefix(doc.Markdown, "[a.pdf](") || !strings.Contains(doc.URL, "/uploads/2/") {
		t.Errorf("doc = %+v", doc)
	}
	if ups := f.SnapshotUploads(); len(ups) != 2 || string(ups[0].Data) != "x" {
		t.Errorf("uploads = %+v", ups)
	}
	f.UploadErr = boom
	if _, err := f.UploadAttachment(bgCtx, repoport.RepoRef{}, "x", "", nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestReset(t *testing.T) {
	boom := errors.New("x")
	f, _ := repoWithMR(t)
	f.User, f.AccessErr, f.CreateErr, f.MRStateErr, f.FindErr, f.ThreadsErr, f.ReplyErr, f.UploadErr = "u", boom, boom, boom, boom, boom, boom, boom
	f.ChecksCalls = 4
	f.Reset()
	if len(f.MRs) != 0 || f.nextID != 0 || f.ChecksCalls != 0 || f.NewHead != "" || f.User != "" {
		t.Errorf("state left: %+v", f)
	}
	if f.AccessErr != nil || f.CreateErr != nil || f.MRStateErr != nil || f.FindErr != nil || f.ThreadsErr != nil || f.ReplyErr != nil || f.UploadErr != nil {
		t.Errorf("scripted failure left: %+v", f)
	}
	ref, _ := f.CreateDraftMR(bgCtx, repoport.RepoRef{}, "b", "m", "", "")
	if ref.IID != 1 {
		t.Errorf("iid after reset = %d", ref.IID)
	}
}

// "every scripted failure", as Reset says: a host shared across tests must
// not carry one test's failure into the next.
func TestResetForgetsEveryScriptedFailure(t *testing.T) {
	boom := errors.New("x")
	f := &RepoHost{LabelErr: boom, BranchErr: boom, Unpushed: true, MissingFiles: map[string]bool{"a": true}, ReplyFailAfter: 1, replies: 1}
	f.Reset()
	if f.LabelErr != nil || f.BranchErr != nil || f.Unpushed || f.MissingFiles != nil || f.ReplyFailAfter != 0 || f.replies != 0 {
		t.Errorf("left: %+v", f)
	}
}

func TestLabels(t *testing.T) {
	f, ref := repoWithMR(t)
	if err := f.AddMRLabels(bgCtx, ref, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.AddMRLabels(bgCtx, ref, []string{"b", "c"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.SnapshotMRs()[0].Labels, ","); got != "a,b,c" {
		t.Errorf("labels = %s", got)
	}
	boom := errors.New("x")
	f.LabelErr = boom
	if err := f.AddMRLabels(bgCtx, ref, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestFindMR(t *testing.T) {
	repo := repoport.RepoRef{DefaultBranch: "main"}
	f, ref := repoWithMR(t)
	if got, ok, err := f.FindMR(bgCtx, repo, "feat/x", ""); err != nil || !ok || got != ref {
		t.Errorf("default target = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := f.FindMR(bgCtx, repo, "feat/x", "develop"); ok {
		t.Error("found on another target")
	}
	if _, ok, _ := f.FindMR(bgCtx, repo, "other", ""); ok {
		t.Error("found on another branch")
	}
	f.SetMRState(ref.IID, repoport.MRMerged)
	if _, ok, _ := f.FindMR(bgCtx, repo, "feat/x", ""); ok {
		t.Error("found a merged MR")
	}
	boom := errors.New("x")
	f.FindErr = boom
	if _, _, err := f.FindMR(bgCtx, repo, "feat/x", ""); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestBranchURL(t *testing.T) {
	f := &RepoHost{}
	if u, ok, err := f.BranchURL(bgCtx, repoport.RepoRef{}, "b"); err != nil || !ok || !strings.HasSuffix(u, "/tree/b") {
		t.Errorf("pushed = %q %v %v", u, ok, err)
	}
	f.Unpushed = true
	if u, ok, _ := f.BranchURL(bgCtx, repoport.RepoRef{}, "b"); ok || u != "" {
		t.Errorf("unpushed = %q %v", u, ok)
	}
	boom := errors.New("x")
	f.BranchErr = boom
	if _, _, err := f.BranchURL(bgCtx, repoport.RepoRef{}, "b"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestMRStatusReports(t *testing.T) {
	f, ref := repoWithMR(t)
	st, _ := f.MRStatus(bgCtx, ref)
	if st.Mergeable != repoport.MergeClean || st.TargetSHA != "" || st.MergeableNow {
		t.Errorf("default = %+v", st)
	}
	f.SetMergeableNow(ref.IID, true)
	f.SetTargetSHA(ref.IID, "tgt")
	f.SetMergeable(ref.IID, repoport.MergeConflicts, "h2")
	st, _ = f.MRStatus(bgCtx, ref)
	if st.Mergeable != repoport.MergeConflicts || st.TargetSHA != "tgt" || st.HeadSHA != "h2" || !st.MergeableNow {
		t.Errorf("conflicted = %+v", st)
	}
	f.SetHead(ref.IID, "h3")
	// A closed MR is never mergeable now, whatever was scripted.
	f.SetMRState(ref.IID, repoport.MRClosed)
	st, _ = f.MRStatus(bgCtx, ref)
	if st.HeadSHA != "h3" || st.MergeableNow {
		t.Errorf("closed = %+v", st)
	}
}

func TestChecksAndCIFailures(t *testing.T) {
	f, ref := repoWithMR(t)
	f.SetChecks(ref.IID,
		repoport.Check{Name: "unit", State: repoport.CheckFailure, URL: "u"},
		repoport.Check{Name: "lint", State: repoport.CheckSuccess})
	c, err := f.MRChecks(bgCtx, ref, "h1")
	if err != nil || c.Head != "h1" || c.State != repoport.CheckFailure || len(c.Items) != 2 || f.ChecksCalls != 1 {
		t.Errorf("checks = %+v, %v, calls %d", c, err, f.ChecksCalls)
	}
	fails, err := f.CIFailures(bgCtx, ref, "h1")
	if err != nil || len(fails) != 1 || fails[0].Name != "unit" || fails[0].URL != "u" {
		t.Errorf("derived = %+v, %v", fails, err)
	}
	f.SetCIFailures(ref.IID, repoport.CIFailure{Name: "scripted"})
	if fails, _ := f.CIFailures(bgCtx, ref, "h1"); len(fails) != 1 || fails[0].Name != "scripted" {
		t.Errorf("scripted = %+v", fails)
	}
}

func TestMergeRules(t *testing.T) {
	refused := func(err error, want string) {
		t.Helper()
		var r *repoport.ErrMergeRefused
		if !errors.As(err, &r) || !strings.Contains(r.Reason, want) {
			t.Errorf("err = %v, want refusal %q", err, want)
		}
	}
	f, ref := repoWithMR(t)
	f.SetMergeRefusal(ref.IID, "policy")
	refused(f.MergeMR(bgCtx, ref, repoport.MergeOptions{}), "policy")
	f.SetMergeRefusal(ref.IID, "")
	refused(f.MergeMR(bgCtx, ref, repoport.MergeOptions{Head: "stale"}), "head moved since stale")
	f.MRs[0].Draft = true
	refused(f.MergeMR(bgCtx, ref, repoport.MergeOptions{}), "draft")
	f.MRs[0].Draft = false
	if err := f.MergeMR(bgCtx, ref, repoport.MergeOptions{Method: "squash", Head: "h1", DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}
	m := f.SnapshotMRs()[0]
	if m.State != repoport.MRMerged || m.MergedWith != "squash" || !m.BranchDeleted {
		t.Errorf("merged = %+v", m)
	}
	refused(f.MergeMR(bgCtx, ref, repoport.MergeOptions{}), "it is merged")
}

func TestPostReview(t *testing.T) {
	f, ref := repoWithMR(t)
	f.MRs[0].DiffLines = map[string][]int{"a.go": {10}}
	rev := repoport.Review{ID: "r1", Notes: []repoport.ReviewNote{
		{Path: "a.go", Line: 10, Body: " on the line ", Kind: "blocker"},
		{Path: "a.go", Line: 11, Body: "off the diff"},
		{Path: "b.go", Line: 1, Body: "another file"},
	}}
	got, err := f.PostReview(bgCtx, ref, rev)
	if err != nil || got.Inline != 1 || got.Folded != 2 || got.Already {
		t.Fatalf("posted = %+v, %v", got, err)
	}
	threads, _ := f.MRThreads(bgCtx, ref)
	if len(threads) != 1 || threads[0].Path != "a.go" || threads[0].Line != 10 {
		t.Fatalf("threads = %+v", threads)
	}
	body := threads[0].Comments[0].Body
	if !strings.HasPrefix(body, "on the line\n\n") || !strings.Contains(body, repoport.NoteMarker("r1", 0, "blocker")) {
		t.Errorf("body = %q", body)
	}
	if again, _ := f.PostReview(bgCtx, ref, rev); !again.Already {
		t.Errorf("repost = %+v", again)
	}
	if len(f.SnapshotThreads(ref.IID)) != 1 {
		t.Error("a repost duplicated its notes")
	}
	// With no diff lines scripted, any line takes a note.
	f.MRs[0].DiffLines = nil
	got, _ = f.PostReview(bgCtx, ref, repoport.Review{ID: "r2", Notes: []repoport.ReviewNote{{Path: "z", Line: 5, Body: "x"}}})
	if got.Inline != 1 {
		t.Errorf("no diff = %+v", got)
	}
	boom := errors.New("x")
	f.ReplyErr = boom
	if _, err := f.PostReview(bgCtx, ref, rev); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestApproval(t *testing.T) {
	f, ref := repoWithMR(t)
	if a, _ := f.MRApproval(bgCtx, ref); a.Approved || len(a.By) != 0 {
		t.Errorf("none = %+v", a)
	}
	f.SetApprovedBy(ref.IID, "alice", "bob")
	if a, _ := f.MRApproval(bgCtx, ref); !a.Approved || len(a.By) != 2 {
		t.Errorf("approved = %+v", a)
	}
}

func TestThreads(t *testing.T) {
	f, ref := repoWithMR(t)
	at := time.Unix(100, 0)
	f.AddComment(ref.IID, "t1", "a.go", "alice", "first", at)
	f.AddComment(ref.IID, "t1", "a.go", "bob", "second", at.Add(time.Second))
	if err := f.ResolveThread(bgCtx, ref, "t1"); err != nil {
		t.Fatal(err)
	}
	if !f.SnapshotThreads(ref.IID)[0].Resolved {
		t.Error("not resolved")
	}
	// A new comment reopens it.
	f.AddComment(ref.IID, "t1", "a.go", "carol", "third", at.Add(2*time.Second))
	th := f.SnapshotThreads(ref.IID)
	if len(th) != 1 || th[0].Resolved || len(th[0].Comments) != 3 {
		t.Fatalf("threads = %+v", th)
	}
	if err := f.ReplyThread(bgCtx, ref, "t1", "reply"); err != nil {
		t.Fatal(err)
	}
	listed, _ := f.MRThreads(bgCtx, ref)
	if c := listed[0].Comments; len(c) != 4 || c[3].Body != "reply" || c[3].Author != "factory" || c[3].ID != "r1" {
		t.Errorf("comments = %+v", c)
	}
	// Copies: an assertion cannot change the host's state.
	listed[0].Comments[0].Body = "changed"
	if f.SnapshotThreads(ref.IID)[0].Comments[0].Body == "changed" {
		t.Error("MRThreads shares storage")
	}
	snap := f.SnapshotThreads(ref.IID)
	snap[0].Comments[0].Body = "changed"
	if f.SnapshotThreads(ref.IID)[0].Comments[0].Body == "changed" {
		t.Error("SnapshotThreads shares storage")
	}
	if err := f.ReplyThread(bgCtx, ref, "ghost", "x"); err == nil || !strings.Contains(err.Error(), "thread ghost") {
		t.Errorf("reply = %v", err)
	}
	if err := f.ResolveThread(bgCtx, ref, "ghost"); err == nil {
		t.Error("resolved nothing")
	}
	boom := errors.New("x")
	f.ThreadsErr = boom
	if _, err := f.MRThreads(bgCtx, ref); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	f.ReplyErr = boom
	if err := f.ReplyThread(bgCtx, ref, "t1", "x"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// A crash in the middle of posting replies: the first ones land.
func TestReplyFailAfter(t *testing.T) {
	f, ref := repoWithMR(t)
	f.AddComment(ref.IID, "t1", "a", "x", "y", time.Unix(1, 0))
	f.ReplyFailAfter = 2
	for i, want := range []bool{true, true, false, false} {
		if err := f.ReplyThread(bgCtx, ref, "t1", "b"); (err == nil) != want {
			t.Errorf("reply %d = %v", i, err)
		}
	}
	if n := len(f.SnapshotThreads(ref.IID)[0].Comments); n != 3 {
		t.Errorf("comments = %d", n)
	}
}
