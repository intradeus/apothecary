package engine

// Verdicts (FLOWLANG §4 edge.verdict): what a gate's person or an agent
// reviewer said, in one shape — approve or reject, a summary, comments on
// files — rendered here, once, into the text the agent reads.
// The page that collects a review sends its structure; it never writes the
// agent's prompt.

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"apothecary/internal/event"
)

const (
	// reviewMaxComments bounds one review: past it the comments are a
	// rewrite, not a review.
	reviewMaxComments = 200
	// verdictMaxBytes caps a verdict carried to the next stage. A review
	// is longer than a failure's tail, and its head is what matters, so it is
	// cut at the end (a failure keeps its last 8 KB).
	verdictMaxBytes = 32 << 10
	// quoteMaxRunes is how much of a selection a comment quotes back.
	quoteMaxRunes = 200
)

// answerFile is one file attached to an answer, by id, where it is
// delivered in the workspace.
type answerFile struct{ id, path string }

func answerPaths(files []answerFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.path)
	}
	return out
}

// imageExts are the files a review shows as a picture: a comment on one is
// about the whole image, never its lines.
var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".avif", ".bmp", ".ico"}

func isImagePath(p string) bool { return slices.Contains(imageExts, strings.ToLower(path.Ext(p))) }

// validateReview checks a review against the files the ping showed: each
// comment is on one of them, says something, and points at lines only where
// a file has lines; it names only files the answer attaches.
func validateReview(shown []string, ans PingAnswer) error {
	if len(ans.Review) == 0 {
		return nil
	}
	if len(shown) == 0 {
		return fmt.Errorf("this question shows no file to comment on")
	}
	if len(ans.Review) > reviewMaxComments {
		return fmt.Errorf("a review holds at most %d comments; got %d", reviewMaxComments, len(ans.Review))
	}
	for i, c := range ans.Review {
		n := i + 1
		if !slices.Contains(shown, c.Path) {
			return fmt.Errorf("comment %d: %q is not one of the files this question shows", n, c.Path)
		}
		if strings.TrimSpace(c.Body) == "" {
			return fmt.Errorf("comment %d: the comment is empty", n)
		}
		if c.StartLine < 0 || c.EndLine < c.StartLine || (c.StartLine == 0 && c.EndLine != 0) {
			return fmt.Errorf("comment %d: lines %d–%d are not a range (start at 1, or 0 for the whole file)", n, c.StartLine, c.EndLine)
		}
		if isImagePath(c.Path) && (c.StartLine != 0 || c.Quote != "") {
			return fmt.Errorf("comment %d: %s is an image — a comment on it is about the whole image", n, c.Path)
		}
		for _, id := range c.AttachmentIDs {
			if !slices.Contains(ans.AttachmentIDs, id) {
				return fmt.Errorf("comment %d: attachment %s is not attached to the answer", n, id)
			}
		}
	}
	return nil
}

// renderReview writes the comments of a verdict: remarks on the whole change
// first, then by file — in the order the files were shown (else of first
// mention), a file's whole-file comments first, then by line — numbered
// across the review. It returns the attachments the comments named, so the
// caller lists only the rest.
func renderReview(b *strings.Builder, shown []string, review []event.ReviewComment, files []answerFile) map[string]bool {
	named := map[string]bool{}
	if len(review) == 0 {
		return named
	}
	pathOf := map[string]string{}
	for _, f := range files {
		pathOf[f.id] = f.path
	}
	firstSeen := map[string]int{}
	for i, c := range review {
		if _, ok := firstSeen[c.Path]; !ok {
			firstSeen[c.Path] = i
		}
	}
	order := func(p string) int {
		switch i := slices.Index(shown, p); {
		case p == "":
			return -1
		case i >= 0:
			return i
		}
		return len(shown) + firstSeen[p]
	}
	sorted := slices.Clone(review)
	slices.SortStableFunc(sorted, func(a, b event.ReviewComment) int {
		if d := order(a.Path) - order(b.Path); d != 0 {
			return d
		}
		return a.StartLine - b.StartLine
	})
	nfiles := 0
	for i, c := range sorted {
		if c.Path != "" && (i == 0 || c.Path != sorted[i-1].Path) {
			nfiles++
		}
	}
	if nfiles > 0 {
		fmt.Fprintf(b, "%s, on %s:\n", plural(len(sorted), "comment"), plural(nfiles, "file"))
	} else {
		fmt.Fprintf(b, "%s:\n", plural(len(sorted), "comment"))
	}
	for i, c := range sorted {
		if c.Path != "" && (i == 0 || c.Path != sorted[i-1].Path) {
			fmt.Fprintf(b, "\n%s\n", c.Path)
		}
		where := ""
		switch {
		case c.Path == "":
		case c.StartLine > 0 && c.EndLine > c.StartLine:
			where = fmt.Sprintf("lines %d–%d", c.StartLine, c.EndLine)
		case c.StartLine > 0:
			where = fmt.Sprintf("line %d", c.StartLine)
		case isImagePath(c.Path):
			where = "the whole image"
		default:
			where = "the whole file"
		}
		if c.Heading != "" {
			where = "§ " + c.Heading + ", " + where
		}
		body := strings.Join(strings.Split(strings.TrimSpace(c.Body), "\n"), "\n   ")
		if where == "" && strings.TrimSpace(c.Quote) == "" {
			// A remark on the whole change reads on its own line.
			fmt.Fprintf(b, "%d. %s\n", i+1, body)
			continue
		}
		if where != "" {
			fmt.Fprintf(b, "%d. [%s]\n", i+1, where)
		} else {
			fmt.Fprintf(b, "%d.\n", i+1)
		}
		if q := strings.TrimSpace(c.Quote); q != "" {
			if r := []rune(q); len(r) > quoteMaxRunes {
				q = string(r[:quoteMaxRunes]) + "…"
			}
			for _, l := range strings.Split(q, "\n") {
				fmt.Fprintf(b, "   > %s\n", l)
			}
		}
		fmt.Fprintf(b, "   %s\n", body)
		var att []string
		for _, id := range c.AttachmentIDs {
			if p := pathOf[id]; p != "" {
				att = append(att, p)
				named[id] = true
			}
		}
		if len(att) > 0 {
			fmt.Fprintf(b, "   attached: %s\n", strings.Join(att, ", "))
		}
	}
	return named
}

// unnamed are the attachments no comment named, by path.
func unnamed(files []answerFile, named map[string]bool) []string {
	var out []string
	for _, f := range files {
		if !named[f.id] {
			out = append(out, f.path)
		}
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// gateVerdict is the verdict a gate's answer carries: on_reject's always;
// on_success's when the approval said something (else ""). auto answers a
// signal supplied read as their summary ("auto: mr_merged").
func gateVerdict(stageID string, rec *event.PingAnswered, shown []string, files []answerFile) string {
	approve := rec.Answer == "approve"
	if approve && strings.TrimSpace(rec.Feedback) == "" && len(rec.Review) == 0 && len(files) == 0 {
		return ""
	}
	return renderVerdict(approve, "at gate "+stageID, rec.Feedback, rec.Review, shown, files)
}

// verdictText is the text an agent's verdict carries: on_reject's always;
// on_success's when the approval said something (else "").
func verdictText(stageID string, v *Verdict) string {
	if v.Approve && v.Summary == "" && len(v.Comments) == 0 {
		return ""
	}
	review := make([]event.ReviewComment, 0, len(v.Comments))
	for _, c := range v.Comments {
		review = append(review, event.ReviewComment{Path: c.Path, StartLine: c.Line, EndLine: c.EndLine, Quote: c.Quote, Body: c.Body})
	}
	return renderVerdict(v.Approve, "by the review at stage "+stageID, v.Summary, review, nil, nil)
}

// renderVerdict is a verdict as the next agent reads it — whoever gave it,
// one text: what was decided and where, the summary, the comments, the
// files the person attached.
func renderVerdict(approve bool, where, summary string, review []event.ReviewComment, shown []string, files []answerFile) string {
	var b strings.Builder
	verb := "REJECTED"
	if approve {
		verb = "APPROVED"
	}
	fmt.Fprintf(&b, "%s %s.\n", verb, where)
	if s := strings.TrimSpace(summary); s != "" {
		fmt.Fprintf(&b, "\n%s\n", s)
	}
	if len(review) > 0 {
		b.WriteString("\n")
	}
	named := renderReview(&b, shown, review, files)
	out := strings.TrimRight(b.String(), "\n") + attachedFilesNote(unnamed(files, named))
	return capVerdict(strings.TrimRight(out, "\n") + "\n")
}

// capVerdict keeps a verdict's head: a cut review still opens with what
// was decided and its first comments.
func capVerdict(s string) string {
	if len(s) <= verdictMaxBytes {
		return s
	}
	cut := verdictMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… (the rest of the review is cut: it is longer than 32 KB)\n"
}

// Verdict is a judgement on the work: approve or reject, a summary, and
// comments — each a remark on the whole, or on a file (and its lines).
type Verdict struct {
	Approve  bool
	Summary  string
	Comments []VerdictComment
}

// VerdictComment is one comment of a verdict: Path "" is a remark on the
// whole change; Line 0 is on the whole file.
type VerdictComment struct {
	Path    string
	Line    int
	EndLine int
	Quote   string
	Body    string
}
