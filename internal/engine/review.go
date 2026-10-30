package engine

import (
	"context"
	"sync"
	"time"

	"apothecary/internal/ports"
)

// MRLive is a merge request as the repository host reports it right now: the
// run page's view of mergeability and review activity.
type MRLive struct {
	Repo           string          `json:"repo"`
	Branch         string          `json:"branch"`
	URL            string          `json:"url"`
	State          ports.MRState   `json:"state"`
	Mergeable      ports.Mergeable `json:"mergeable"`
	Target         string          `json:"target,omitempty"`
	HeadSHA        string          `json:"head_sha,omitempty"`
	PendingThreads int             `json:"pending_threads"`
	// LastCommentAt is the newest pending reviewer comment (zero: none).
	LastCommentAt *time.Time `json:"last_comment_at,omitempty"`
	// MergeableNow is the repository host's verdict that it would merge now;
	// Draft whether it is still a draft; Checks the head's CI; Approval its
	// reviews' verdict. Checks and Approval are read for an open one only.
	MergeableNow bool            `json:"mergeable_now,omitempty"`
	Draft        bool            `json:"draft,omitempty"`
	Checks       *ports.Checks   `json:"checks,omitempty"`
	Approval     *ports.Approval `json:"approval,omitempty"`
	// StackedOn is the open merge request whose branch an open one targets:
	// merging it lands there, not on the repository's main line.
	StackedOn *ports.MRBase `json:"stacked_on,omitempty"`
	// Error is what the repository host said when it could not be asked.
	Error string `json:"error,omitempty"`
}

// MRStatuses asks the repository host about each of the run's merge requests.
// It is an observation for display, not run history: nothing is recorded.
func (e *Engine) MRStatuses(ctx context.Context, runID string) ([]MRLive, error) {
	v, err := e.runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(v.MRs) == 0 {
		return []MRLive{}, nil
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
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
		go func(l *MRLive, ref ports.MRRef) {
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
			if st.State == ports.MROpen {
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
			pending := ports.PendingThreads(threads)
			l.PendingThreads = len(pending)
			if c, ok := ports.NewestPending(pending); ok {
				at := c.CreatedAt
				l.LastCommentAt = &at
			}
		}(&out[i], ports.MRRef{IID: mr.IID, URL: mr.URL})
	}
	wg.Wait()
	// Stacking is read off the repository's open merge requests, listed once
	// per repository. Best effort: a list that fails leaves no notice rather
	// than an error over a status that was read.
	open := map[string][]ports.MRInfo{}
	for i := range out {
		l := &out[i]
		if l.Error != "" || l.State != ports.MROpen || l.Target == "" {
			continue
		}
		mrs, ok := open[l.Repo]
		if !ok {
			mrs, _ = res.RepoHostFor(l.Repo).ListMRs(ctx)
			open[l.Repo] = mrs
		}
		l.StackedOn = ports.StackedOn(l.Target, mrs)
	}
	return out, nil
}
