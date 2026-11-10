package mergerequest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
)

// host answers GetMR, and nothing else.
type host struct {
	repoport.RepoHost
	mr  repoport.MRInfo
	err error
}

func (h host) GetMR(context.Context, int) (repoport.MRInfo, error) { return h.mr, h.err }

func open(iid int) repoport.MRInfo {
	return repoport.MRInfo{Ref: repoport.MRRef{IID: iid, URL: "https://git.test/mr/7"}, Title: "Add keys", Body: " why ",
		Author: "ada", State: repoport.MROpen, Source: "feat/keys", Target: "main", Head: "abc", HeadRef: "refs/merge-requests/7/head"}
}

func TestKeys(t *testing.T) {
	if Key("", 7) != "repo!7" || Key("docs", 7) != "docs!7" {
		t.Error("key")
	}
	if r := Ref("p", "", 7); r != (workitem.Ref{Kind: Name, Project: "p", Key: "repo!7"}) {
		t.Errorf("ref = %v", r)
	}
	repo, iid, err := ParseKey("docs!12")
	if err != nil || repo != "docs" || iid != 12 {
		t.Errorf("parse = %q %d %v", repo, iid, err)
	}
	for _, bad := range []string{"12", "!12", "repo!x", "repo!0", "repo!-1"} {
		if _, _, err := ParseKey(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
	k := Kind{}
	for ref, want := range map[workitem.Ref]string{
		Ref("p", "", 7):        "merge request !7",
		Ref("p", "docs", 7):    "merge request docs!7",
		{Kind: Name, Key: "x"}: "merge request x",
	} {
		if got := k.Describe(ref); got != want {
			t.Errorf("describe %v = %q", ref, got)
		}
	}
}

func TestMergeRequestKind(t *testing.T) {
	k := Kind{}
	if k.Name() != "merge_request" || k.Noun() != "merge request" || !k.Required() || k.Accompanies() {
		t.Error("kind")
	}
	ref, ok := k.Requested("p", runinput.Ticket{}, runinput.Input{MR: &runinput.MR{IID: 7}})
	if !ok || ref != Ref("p", "", 7) {
		t.Errorf("requested = %v", ref)
	}
	if _, ok := k.Requested("p", runinput.Ticket{TicketID: "tk"}, runinput.Input{}); ok {
		t.Error("a ticket named a merge request")
	}
	// Started on one, opened another (and recorded the first again): each once.
	v := run.View{ProjectID: "p", Adopted: &event.AdoptedMR{Repo: "", IID: 7},
		MRs: []run.MRView{{Repo: "repo", IID: 7}, {Repo: "docs", IID: 2}}}
	held := k.Held(v)
	if len(held) != 2 || held[0] != Ref("p", "", 7) || held[1] != Ref("p", "docs", 2) {
		t.Errorf("held = %v", held)
	}
	if k.Held(run.View{ProjectID: "p"}) != nil {
		t.Error("a run with no merge request held one")
	}
}

func TestPrepare(t *testing.T) {
	ctx := context.Background()
	res := func(h repoport.RepoHost) *resolution.Resolution {
		return &resolution.Resolution{RepoHost: h, ExtraRepos: []resolution.ExtraRepo{{Name: "docs"}}}
	}
	t.Run("puts the checkout on its head", func(t *testing.T) {
		s := &workitem.Start{Res: res(host{mr: open(7)})}
		if err := (Kind{}).Prepare(ctx, s, Ref("p", "", 7)); err != nil {
			t.Fatal(err)
		}
		want := workitem.Repoint{Repo: "repo", Branch: "feat/keys", Source: "main", Fetch: "refs/merge-requests/7/head"}
		if len(s.Repoints) != 1 || s.Repoints[0] != want {
			t.Errorf("repoints = %+v", s.Repoints)
		}
		if a := s.Adopted; a == nil || a.IID != 7 || a.Head != "abc" || a.Author != "ada" || a.Title != "Add keys" {
			t.Errorf("adopted = %+v", a)
		}
	})
	t.Run("a fork's branch is a local one", func(t *testing.T) {
		mr := open(7)
		mr.Fork = true
		s := &workitem.Start{Res: res(host{mr: mr})}
		if err := (Kind{}).Prepare(ctx, s, Ref("p", "", 7)); err != nil || s.Repoints[0].Branch != "mr-7" || !s.Repoints[0].Fork {
			t.Errorf("fork = %+v, %v", s.Repoints, err)
		}
	})
	boom := errors.New("host down")
	merged := open(7)
	merged.State = repoport.MRMerged
	for _, c := range []struct {
		name string
		res  *resolution.Resolution
		item workitem.Ref
		want string
	}{
		{"a bad key", res(host{}), workitem.Ref{Kind: Name, Key: "x"}, "not repo!iid"},
		{"an unknown repository", res(host{}), Ref("p", "ghost", 7), `no repository "ghost"`},
		{"a repository no integration reaches", res(host{}), Ref("p", "docs", 7), `no integration reaches repository "docs"`},
		{"the host failing", res(host{err: boom}), Ref("p", "", 7), "merge request !7: host down"},
		{"one merged", res(host{mr: merged}), Ref("p", "", 7), "is merged: a run starts on an open merge request"},
	} {
		err := (Kind{}).Prepare(ctx, &workitem.Start{Res: c.res}, c.item)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if err := CheckOpen(merged); !errors.Is(err, ErrNotOpen) {
		t.Errorf("check open = %v", err)
	}
}

// A run on a merge request is given its title and description, with no
// ticket made for it.
func TestWork(t *testing.T) {
	got := Work(open(7))
	if got.Title != "Add keys (!7 by ada)" || got.TicketID != "" || got.SourceKind != "" {
		t.Errorf("work = %+v", got)
	}
	if want := "why\n\nMerge request https://git.test/mr/7 by ada: feat/keys → main."; got.Goal != want {
		t.Errorf("goal = %q", got.Goal)
	}
	bare := open(7)
	bare.Body = " "
	if got := Work(bare); !strings.HasPrefix(got.Goal, "Merge request ") {
		t.Errorf("no description, no gap: %q", got.Goal)
	}
}
