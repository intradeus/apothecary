// Package mergerequest is the merge request kind of work item
// (operates_on: merge_request): a merge request someone opened in one of the
// project's repositories. It is no ticket and gets none. A run names one by
// its repository and number; the run works on its branch, checked out from
// the ref the repository host keeps for its head, and is given its title
// and description as its work. A merge request's runs are the ones that
// hold it — started on it, or the one that opened it.
package mergerequest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
	"apothecary/internal/workspace/checkout"
)

// Name is the kind's operates_on value.
const Name = "merge_request"

// ErrNotOpen refuses a run on a merge request that is merged or closed.
var ErrNotOpen = errors.New("a run starts on an open merge request")

// Kind is the merge request kind.
type Kind struct{}

var _ workitem.Kind = Kind{}

// Key is merge request iid of the checkout repo ("" = the primary) as a
// work item key: repo!iid.
func Key(repo string, iid int) string {
	return cmp.Or(repo, event.PrimaryCheckoutName) + "!" + strconv.Itoa(iid)
}

// Ref names merge request iid of the checkout repo ("" = the primary) of a
// project.
func Ref(projectID, repo string, iid int) workitem.Ref {
	return workitem.Ref{Kind: Name, Project: projectID, Key: Key(repo, iid)}
}

// ParseKey is the repo and iid a key names.
func ParseKey(key string) (repo string, iid int, err error) {
	repo, n, ok := strings.Cut(key, "!")
	if iid, err = strconv.Atoi(n); !ok || repo == "" || err != nil || iid <= 0 {
		return "", 0, fmt.Errorf("merge request key %q is not repo!iid", key)
	}
	return repo, iid, nil
}

func (Kind) Name() string { return Name }
func (Kind) Noun() string { return "merge request" }

func (Kind) Describe(r workitem.Ref) string {
	repo, iid, err := ParseKey(r.Key)
	switch {
	case err != nil:
		return "merge request " + r.Key
	case repo == event.PrimaryCheckoutName:
		return fmt.Sprintf("merge request !%d", iid)
	}
	return fmt.Sprintf("merge request %s!%d", repo, iid)
}

// Required is true: a run of a flow on merge requests names one.
func (Kind) Required() bool { return true }

// Accompanies is false: a flow on tickets does not take a merge request.
func (Kind) Accompanies() bool { return false }

func (Kind) Requested(projectID string, _ runinput.Ticket, in runinput.Input) (workitem.Ref, bool) {
	if in.MR == nil {
		return workitem.Ref{}, false
	}
	return Ref(projectID, in.MR.Repo, in.MR.IID), true
}

// Held are the one the run was started on and the ones it opened.
func (Kind) Held(v run.View) []workitem.Ref {
	var out []workitem.Ref
	add := func(repo string, iid int) {
		if r := Ref(v.ProjectID, repo, iid); !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	if a := v.Adopted; a != nil {
		add(a.Repo, a.IID)
	}
	for _, m := range v.MRs {
		add(m.Repo, m.IID)
	}
	return out
}

// Prepare reads the merge request and puts its checkout on it: the branch
// is the merge request's own (a fork's, which has none here, a local
// mr-<iid>), forked from nothing — its head is fetched from the repository
// host's ref — and its target is what it merges into.
func (Kind) Prepare(ctx context.Context, s *workitem.Start, item workitem.Ref) error {
	repo, iid, err := ParseKey(item.Key)
	if err != nil {
		return err
	}
	known := repo == event.PrimaryCheckoutName
	for _, x := range s.Res.ExtraRepos {
		known = known || x.Name == repo
	}
	if !known {
		return fmt.Errorf("merge request: the project has no repository %q", repo)
	}
	host := s.Res.RepoHostFor(repo)
	if host == nil {
		return fmt.Errorf("merge request: no integration reaches repository %q", repo)
	}
	info, err := host.GetMR(ctx, iid)
	if err != nil {
		return fmt.Errorf("merge request !%d: %w", iid, err)
	}
	if err := CheckOpen(info); err != nil {
		return err
	}
	branch := info.Source
	if info.Fork || checkout.CheckBranch(branch) != nil {
		branch = fmt.Sprintf("mr-%d", info.Ref.IID)
	}
	s.Repoints = append(s.Repoints, workitem.Repoint{Repo: repo, Branch: branch, Source: info.Target, Fetch: info.HeadRef, Fork: info.Fork})
	s.Adopted = &event.AdoptedMR{Repo: repo, IID: info.Ref.IID, URL: info.Ref.URL, Head: info.Head, Author: info.Author, Title: info.Title}
	return nil
}

// CheckOpen refuses a merge request that is no longer open.
func CheckOpen(mr repoport.MRInfo) error {
	if mr.State != repoport.MROpen {
		return fmt.Errorf("merge request %s is %s: %w", mr.Ref.URL, mr.State, ErrNotOpen)
	}
	return nil
}

// Work is what a run on a merge request is given: titled with it and its
// author, its description and a line naming it as the goal. No ticket: the
// run's ticket fields carry it, so a flow's prompts read it the way they
// read a ticket's.
func Work(mr repoport.MRInfo) runinput.Ticket {
	goal := strings.TrimSpace(mr.Body)
	if goal != "" {
		goal += "\n\n"
	}
	goal += fmt.Sprintf("Merge request %s by %s: %s → %s.", mr.Ref.URL, mr.Author, mr.Source, mr.Target)
	return runinput.Ticket{Title: fmt.Sprintf("%s (!%d by %s)", mr.Title, mr.Ref.IID, mr.Author), Goal: goal}
}
