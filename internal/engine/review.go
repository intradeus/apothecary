package engine

import (
	"context"
	"sync"
	"time"

	"apothecary/internal/ports"
)

// pendingThreads are the review threads still waiting on the factory: not
// resolved, and whose latest comment is not a factory reply (so a reviewer
// spoke after our last reply, or we never replied). Derived from the forge
// alone — no cursor is stored, so nothing can drift from what the reviewer
// sees.
func pendingThreads(threads []ports.ReviewThread) []ports.ReviewThread {
	var out []ports.ReviewThread
	for _, t := range threads {
		if t.Resolved || len(t.Comments) == 0 {
			continue
		}
		if ports.IsFactoryReply(t.Comments[len(t.Comments)-1].Body) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// newestPending is the latest reviewer comment across pending threads: its
// time drives the quiet period, its id is the trigger key.
func newestPending(pending []ports.ReviewThread) (ports.ReviewComment, bool) {
	var newest ports.ReviewComment
	found := false
	for _, t := range pending {
		for _, c := range t.Comments {
			if ports.IsFactoryReply(c.Body) {
				continue
			}
			if !found || c.CreatedAt.After(newest.CreatedAt) {
				newest, found = c, true
			}
		}
	}
	return newest, found
}

// MRLive is a merge request as the forge reports it right now: the run
// page's view of mergeability and review activity.
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
	// Error is what the forge said when it could not be asked.
	Error string `json:"error,omitempty"`
}

// MRStatuses asks the forge about each of the run's merge requests. It is an
// observation for display, not run history: nothing is recorded.
func (e *Engine) MRStatuses(ctx context.Context, runID string) ([]MRLive, error) {
	v, err := e.runView(ctx, runID)
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
	defer res.release()
	out := make([]MRLive, len(v.MRs))
	var wg sync.WaitGroup
	for i, mr := range v.MRs {
		out[i] = MRLive{Repo: mr.Repo, Branch: mr.Branch, URL: mr.URL, Target: mr.Target}
		forge := res.forgeFor(mr.Repo)
		if forge == nil {
			out[i].Error = "no forge for this repository"
			continue
		}
		wg.Add(1)
		go func(l *MRLive, ref ports.MRRef) {
			defer wg.Done()
			st, err := forge.MRStatus(ctx, ref)
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.State, l.Mergeable, l.HeadSHA = st.State, st.Mergeable, st.HeadSHA
			if st.Target != "" {
				l.Target = st.Target
			}
			threads, err := forge.MRThreads(ctx, ref)
			if err != nil {
				l.Error = err.Error()
				return
			}
			pending := pendingThreads(threads)
			l.PendingThreads = len(pending)
			if c, ok := newestPending(pending); ok {
				at := c.CreatedAt
				l.LastCommentAt = &at
			}
		}(&out[i], ports.MRRef{IID: mr.IID, URL: mr.URL})
	}
	wg.Wait()
	return out, nil
}
