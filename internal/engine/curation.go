package engine

// Curation (TODO 45): a gate over a review document — a review_threads file
// before post_review, a review_replies file before reply_review_comments —
// lets the person decide what of it is posted and how it reads. The answer
// carries DECISIONS over the document as the gate showed it (by item), never
// a rewritten document: the engine rebuilds the file from the raise-time
// snapshot and these, so what is posted is provably a subset, rewritten, of
// what the person saw — even when the workspace file changed under a parked
// gate. The result goes on the record (review_curated) at the gate, and into
// the workspace when the next stage that reads the path starts: a gate has
// no workspace of its own.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/flow"
	"apothecary/internal/reviewfile"
)

// curatable reports whether a shown file of that type takes a curation.
func curatable(typ string) bool {
	return typ == agentspec.TypeReviewThreads || typ == agentspec.TypeReviewReplies
}

// pingSnapshot is the bytes of a file as the ping showed it: the ping_file
// snapshot captured when the question was raised.
func (e *Engine) pingSnapshot(ctx context.Context, pingID, path string) ([]byte, error) {
	if e.snapshots == nil {
		return nil, fmt.Errorf("no snapshot store")
	}
	list, err := e.snapshots.ForPing(ctx, pingID)
	if err != nil {
		return nil, err
	}
	for _, sn := range list {
		if sn.Kind == filestore.KindPingFile && sn.Path == path {
			data, _, err := e.snapshots.Bytes(ctx, sn.ID)
			return data, err
		}
	}
	return nil, fmt.Errorf("no snapshot of %s", path)
}

// validateCuration holds an answer's curation against what the ping showed
// — each curated path one of its files with a review type, its snapshot
// there, every decision about a real item — and tidies it in place (a text
// equal to the original is no rewrite). One implementation for the live and
// the dormant answer path, like validateAnswer.
func (e *Engine) validateCuration(ctx context.Context, pingID string, files []string, types map[string]string, ans *PingAnswer) error {
	for path, c := range ans.Curated {
		typ := types[path]
		if !slices.Contains(files, path) || !curatable(typ) {
			return fmt.Errorf("%s is not a review this question curates", path)
		}
		data, err := e.pingSnapshot(ctx, pingID, path)
		if err != nil {
			return fmt.Errorf("%s was not captured when the question was raised, so it can only be approved as written (%v)", path, err)
		}
		switch typ {
		case agentspec.TypeReviewThreads:
			doc, err := reviewfile.ParseReview(path, data)
			if err != nil {
				return fmt.Errorf("%s, as shown, is not a review: %v", path, err)
			}
			if err := reviewfile.ValidateReview(doc, &c); err != nil {
				return fmt.Errorf("%s: %v", path, err)
			}
		case agentspec.TypeReviewReplies:
			list, err := reviewfile.ParseReplies(path, data)
			if err != nil {
				return fmt.Errorf("%s, as shown, is not a replies file: %v", path, err)
			}
			if err := reviewfile.ValidateReplies(list, &c); err != nil {
				return fmt.Errorf("%s: %v", path, err)
			}
		}
		ans.Curated[path] = c
	}
	return nil
}

// recordCuration resolves a gate answer's curation against what the gate
// showed and puts it on the record, once per ping: the curated document, the
// notes dropped (a re-review never proposes them again), the replies held
// back — on an approval their threads are skipped until someone writes
// there again. Approve and reject alike: a person's "no" to a note stands
// even when the draft goes back.
func (x *exec) recordCuration(st *flow.Stage, rec *event.PingAnswered) ([]*event.ReviewCurated, error) {
	rs := x.rs
	if len(rec.Curated) == 0 || rs.curatedSeen[rec.PingID] {
		return nil, nil
	}
	var types map[string]string
	if raised := rs.raisedPings[rec.PingID]; raised != nil {
		types = raised.FileTypes
	}
	paths := make([]string, 0, len(rec.Curated))
	for p := range rec.Curated {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	approved := rec.Answer == "approve"
	var out []*event.ReviewCurated
	for _, path := range paths {
		c := rec.Curated[path]
		data, err := x.e.pingSnapshot(x.bg, rec.PingID, path)
		if err != nil {
			return nil, fmt.Errorf("reading %s as gate %s showed it: %w", path, st.ID, err)
		}
		ev := &event.ReviewCurated{StageID: st.ID, PingID: rec.PingID, Path: path, Type: types[path], Approved: approved}
		switch ev.Type {
		case agentspec.TypeReviewThreads:
			doc, err := reviewfile.ParseReview(path, data)
			if err != nil {
				return nil, fmt.Errorf("%s as gate %s showed it: %w", path, st.ID, err)
			}
			curated, ap := reviewfile.ApplyReview(*doc, c)
			ev.Review, ev.Dropped, ev.DroppedReplies, ev.Rewritten = &curated, ap.Dropped, ap.DroppedReplies, ap.Rewritten
		case agentspec.TypeReviewReplies:
			list, err := reviewfile.ParseReplies(path, data)
			if err != nil {
				return nil, fmt.Errorf("%s as gate %s showed it: %w", path, st.ID, err)
			}
			curated, ap := reviewfile.ApplyReplies(list, c)
			ev.Replies, ev.DroppedReplies, ev.Rewritten = curated, ap.DroppedReplies, ap.Rewritten
			if ev.Replies == nil {
				ev.Replies = []reviewfile.Reply{}
			}
			if approved && len(ap.DroppedReplies) > 0 {
				ev.Skipped = map[string]string{}
				for _, id := range ap.DroppedReplies {
					if lt := rs.roundThread(id); lt != nil && lt.Last != "" {
						ev.Skipped[id] = lt.Last
					}
				}
			}
		default:
			return nil, fmt.Errorf("%s: gate %s shows no review there", path, st.ID)
		}
		if err := x.append(ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// materialiseCurated writes the curated documents waiting on the paths a
// stage reads, before it reads them: the gate that approved them had no
// workspace. Idempotent — a crash after the write and before the stage
// records anything writes the same bytes again.
func (x *exec) materialiseCurated(st *flow.Stage) error {
	rs := x.rs
	if len(rs.curated) == 0 {
		return nil
	}
	for _, in := range st.Inputs {
		if flow.IsEngineSource(in.Value) {
			continue
		}
		path := flow.FilePath(in.Value)
		ev := rs.curated[path]
		if ev == nil || x.materialised[path] == ev {
			continue
		}
		var doc any
		switch ev.Type {
		case agentspec.TypeReviewThreads:
			doc = ev.Review
		case agentspec.TypeReviewReplies:
			doc = ev.Replies
		}
		data, err := reviewfile.Encode(path, doc)
		if err != nil {
			return fmt.Errorf("rendering the review a person approved at gate %s: %w", ev.StageID, err)
		}
		if err := x.res.Envp.WriteFile(x.ctx, rs.envID, path, data, 0o644); err != nil {
			return fmt.Errorf("writing %s as approved at gate %s: %w", path, ev.StageID, err)
		}
		x.e.log.Info("curated review written", "run", rs.runID, "gate", ev.StageID, "path", path, "stage", st.ID)
		if x.materialised == nil {
			x.materialised = map[string]*event.ReviewCurated{}
		}
		x.materialised[path] = ev
	}
	return nil
}

// unskippedRound is the round's threads a reply is still owed to: the ones
// a person did not hold a reply back from at the gate (skipped at their
// last comment).
func (rs *runState) unskippedRound() []string {
	var out []string
	for _, t := range rs.round {
		if last, ok := rs.skipped[t.ID]; ok && last == t.Last {
			continue
		}
		out = append(out, t.ID)
	}
	return out
}

// renderCuration says, for the agent that redoes a rejected review, what the
// person made of the draft: the notes they dropped (never to propose again)
// and the replies they held back.
func renderCuration(evs []*event.ReviewCurated) string {
	var b strings.Builder
	for _, ev := range evs {
		if len(ev.Dropped) == 0 && len(ev.DroppedReplies) == 0 {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\nOf the draft reviewed:\n")
		}
		for _, n := range ev.Dropped {
			fmt.Fprintf(&b, "- dropped, do not propose again: %s:%d [%s] %s\n", n.Path, n.Line, n.Kind, firstWords(n.Body, 120))
		}
		for _, id := range ev.DroppedReplies {
			fmt.Fprintf(&b, "- held back: the reply in thread %s\n", id)
		}
	}
	return b.String()
}

// firstWords is the head of a text on one line.
func firstWords(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
