// Package mrs is the read side of merge requests: what a project's
// repository hosts report right now, for people picking a merge request to
// start a run on and for the run page's live status. It observes and never
// records — nothing here appends to a run's log — so it needs only the
// resolver and the run views, not the engine.
package mrs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// Service answers merge-request questions through each project's resolved
// repository hosts.
type Service struct {
	Resolver resolution.Resolver
	Runs     *run.Reader
}

func New(resolver resolution.Resolver, runs *run.Reader) *Service {
	return &Service{Resolver: resolver, Runs: runs}
}

// ListMRs is the open merge requests of a project's repository ("" = the
// primary), for a person picking one to start a run on.
func (s *Service) ListMRs(ctx context.Context, projectID, repo string) ([]repoport.MRInfo, error) {
	res, err := s.Resolver.Resolve(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	repoHost := res.RepoHostFor(repo)
	if repoHost == nil {
		return nil, fmt.Errorf("no integration reaches repository %q", repo)
	}
	return repoHost.ListMRs(ctx)
}

// MR reads one merge request of a project's repository ("" = the primary).
func (s *Service) MR(ctx context.Context, projectID, repo string, iid int) (repoport.MRInfo, error) {
	res, err := s.Resolver.Resolve(ctx, projectID)
	if err != nil {
		return repoport.MRInfo{}, err
	}
	defer res.Close()
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	repoHost := res.RepoHostFor(repo)
	if repoHost == nil {
		return repoport.MRInfo{}, fmt.Errorf("no integration reaches repository %q", repo)
	}
	return repoHost.GetMR(ctx, iid)
}

// RepoHostUser is who a project's repository host integration acts as in a
// repository ("" = the primary): the account the factory's own merge requests
// are opened by.
func (s *Service) RepoHostUser(ctx context.Context, projectID, repo string) (string, error) {
	res, err := s.Resolver.Resolve(ctx, projectID)
	if err != nil {
		return "", err
	}
	defer res.Close()
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	repoHost := res.RepoHostFor(repo)
	if repoHost == nil {
		return "", fmt.Errorf("no integration reaches repository %q", repo)
	}
	return repoHost.Whoami(ctx)
}

// MRLive is a merge request as the repository host reports it right now: the
// run page's view of mergeability and review activity.
type MRLive struct {
	Repo           string             `json:"repo"`
	Branch         string             `json:"branch"`
	URL            string             `json:"url"`
	State          repoport.MRState   `json:"state"`
	Mergeable      repoport.Mergeable `json:"mergeable"`
	Target         string             `json:"target,omitempty"`
	HeadSHA        string             `json:"head_sha,omitempty"`
	PendingThreads int                `json:"pending_threads"`
	// LastCommentAt is the newest pending reviewer comment (zero: none).
	LastCommentAt *time.Time `json:"last_comment_at,omitempty"`
	// MergeableNow is the repository host's verdict that it would merge now;
	// Draft whether it is still a draft; Checks the head's CI; Approval its
	// reviews' verdict. Checks and Approval are read for an open one only.
	MergeableNow bool               `json:"mergeable_now,omitempty"`
	Draft        bool               `json:"draft,omitempty"`
	Checks       *repoport.Checks   `json:"checks,omitempty"`
	Approval     *repoport.Approval `json:"approval,omitempty"`
	// StackedOn is the open merge request whose branch an open one targets:
	// merging it lands there, not on the repository's main line.
	StackedOn *repoport.MRBase `json:"stacked_on,omitempty"`
	// Error is what the repository host said when it could not be asked.
	Error string `json:"error,omitempty"`
}

// MRStatuses asks the repository host about each of the run's merge requests.
// It is an observation for display, not run history: nothing is recorded.
func (s *Service) MRStatuses(ctx context.Context, runID string) ([]MRLive, error) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(v.MRs) == 0 {
		return []MRLive{}, nil
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	out := make([]MRLive, len(v.MRs))
	var wg sync.WaitGroup
	for i, mr := range v.MRs {
		out[i] = MRLive{Repo: mr.Repo, Branch: mr.Branch, URL: mr.URL, Target: mr.Target}
		repoHost := res.RepoHostFor(mr.Repo)
		if repoHost == nil {
			out[i].Error = "no integration reaches this repository"
			continue
		}
		wg.Add(1)
		go func(l *MRLive, ref repoport.MRRef) {
			defer wg.Done()
			st, err := repoHost.MRStatus(ctx, ref)
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.State, l.Mergeable, l.HeadSHA = st.State, st.Mergeable, st.HeadSHA
			l.MergeableNow, l.Draft = st.MergeableNow, st.Draft
			if st.Target != "" {
				l.Target = st.Target
			}
			if st.State == repoport.MROpen {
				checks, err := repoHost.MRChecks(ctx, ref, st.HeadSHA)
				if err != nil {
					l.Error = err.Error()
					return
				}
				l.Checks = &checks
				ap, err := repoHost.MRApproval(ctx, ref)
				if err != nil {
					l.Error = err.Error()
					return
				}
				l.Approval = &ap
			}
			threads, err := repoHost.MRThreads(ctx, ref)
			if err != nil {
				l.Error = err.Error()
				return
			}
			pending := repoport.PendingThreads(threads)
			l.PendingThreads = len(pending)
			if c, ok := repoport.NewestPending(pending); ok {
				at := c.CreatedAt
				l.LastCommentAt = &at
			}
		}(&out[i], repoport.MRRef{IID: mr.IID, URL: mr.URL})
	}
	wg.Wait()
	// Stacking is read off the repository's open merge requests, listed once
	// per repository. Best effort: a list that fails leaves no notice rather
	// than an error over a status that was read.
	open := map[string][]repoport.MRInfo{}
	for i := range out {
		l := &out[i]
		if l.Error != "" || l.State != repoport.MROpen || l.Target == "" {
			continue
		}
		mrs, ok := open[l.Repo]
		if !ok {
			mrs, _ = res.RepoHostFor(l.Repo).ListMRs(ctx)
			open[l.Repo] = mrs
		}
		l.StackedOn = repoport.StackedOn(l.Target, mrs)
	}
	return out, nil
}
