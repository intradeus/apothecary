// Package reviewfile holds the three documents a merge-request review passes
// around as files, and nothing else: a review to post (review_threads), a
// round's replies (review_replies) and the threads already on the merge
// request (mr_threads). It is a leaf — the flow parser prints its schemas,
// the engine checks and posts its documents, the API decodes what a gate
// showed, the event log keeps what a person made of it — so every reader
// decodes the same shape the same way.
//
// Every document is YAML or JSON by its path's extension, with unknown keys
// refused: an agent's typo is a failure it can read, never a field silently
// dropped. What is POSTED to the repository host is a note's body, a reply's
// text and a review's summary; everything else a note or a reply carries
// (its kind, its rationale, evidence, example) is for the person deciding
// whether to post it.
package reviewfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kinds of a note: what it asks of the author. post_review renders it back
// into the posted text as a leading "[kind] " tag.
const (
	KindBlocking   = "blocking"
	KindSuggestion = "suggestion"
	KindQuestion   = "question"
	KindNit        = "nit"
)

// Kinds lists the note kinds, in order of weight.
var Kinds = []string{KindBlocking, KindSuggestion, KindQuestion, KindNit}

// What a reply did about its thread: the code changed (fixed), it was left
// as it is on purpose (declined), or the thread asked something and the
// reply answers it (answered).
const (
	DidFixed    = "fixed"
	DidDeclined = "declined"
	DidAnswered = "answered"
)

// Dids lists what a reply may say it did.
var Dids = []string{DidFixed, DidDeclined, DidAnswered}

// MaxItems bounds a review: more notes and replies than this is a report,
// which belongs in the summary.
const MaxItems = 50

// Review is a review_threads document: a summary, notes that open new
// threads, and replies in threads already on the merge request.
type Review struct {
	Summary string         `yaml:"summary,omitempty" json:"summary,omitempty"`
	Notes   []Note         `yaml:"notes,omitempty" json:"notes,omitempty"`
	Replies []Continuation `yaml:"replies,omitempty" json:"replies,omitempty"`
}

// Note is one new thread: on a line of a file, with a kind. Body is posted;
// Rationale, Evidence and Example are for the person at the gate.
type Note struct {
	Path      string `yaml:"path" json:"path"`
	Line      int    `yaml:"line" json:"line"`
	Kind      string `yaml:"kind" json:"kind"`
	Body      string `yaml:"body" json:"body"`
	Rationale string `yaml:"rationale,omitempty" json:"rationale,omitempty"`
	Evidence  string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
	Example   string `yaml:"example,omitempty" json:"example,omitempty"`
}

// Continuation is a reply the reviewer writes in a thread already on the
// merge request — its own, to say an earlier note is addressed or still
// open, or someone else's.
type Continuation struct {
	Thread    string `yaml:"thread" json:"thread"`
	Body      string `yaml:"body" json:"body"`
	Resolve   bool   `yaml:"resolve,omitempty" json:"resolve,omitempty"`
	Rationale string `yaml:"rationale,omitempty" json:"rationale,omitempty"`
}

// Reply is one entry of a review_replies document: what a round did about
// one thread it loaded. Reply is posted; the rest is the owner's.
type Reply struct {
	Thread    string `yaml:"thread" json:"thread"`
	Did       string `yaml:"did" json:"did"`
	Reply     string `yaml:"reply" json:"reply"`
	Resolve   bool   `yaml:"resolve,omitempty" json:"resolve,omitempty"`
	Rationale string `yaml:"rationale,omitempty" json:"rationale,omitempty"`
	Evidence  string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// Threads is an mr_threads document: what is on the merge request, as the
// engine read it — every thread the loader took, and the notes a person
// declined to post earlier in the run, so a re-review never proposes them
// again.
type Threads struct {
	MergeRequest string   `yaml:"merge_request,omitempty" json:"merge_request,omitempty"`
	Threads      []Thread `yaml:"threads" json:"threads"`
	Dropped      []Note   `yaml:"dropped,omitempty" json:"dropped,omitempty"`
}

// Thread is one conversation on the merge request. Ours says apothecary
// opened it (a review note), Kind the note's kind when its marker carried
// one; Hunk is the diff hunk the host shows with it.
type Thread struct {
	ID       string    `yaml:"id" json:"id"`
	Path     string    `yaml:"path,omitempty" json:"path,omitempty"`
	Line     int       `yaml:"line,omitempty" json:"line,omitempty"`
	Resolved bool      `yaml:"resolved" json:"resolved"`
	Ours     bool      `yaml:"ours" json:"ours"`
	Kind     string    `yaml:"kind,omitempty" json:"kind,omitempty"`
	Hunk     string    `yaml:"hunk,omitempty" json:"hunk,omitempty"`
	Comments []Comment `yaml:"comments" json:"comments"`
}

// Comment is one comment of a thread, oldest first in its thread. Ours is
// apothecary's own word; New says it came after apothecary's last word in
// the thread (what a round answers); Did is what an earlier reply of ours
// said it did. At is RFC 3339.
type Comment struct {
	ID     string `yaml:"id" json:"id"`
	Author string `yaml:"author" json:"author"`
	At     string `yaml:"at,omitempty" json:"at,omitempty"`
	Ours   bool   `yaml:"ours,omitempty" json:"ours,omitempty"`
	New    bool   `yaml:"new,omitempty" json:"new,omitempty"`
	Did    string `yaml:"did,omitempty" json:"did,omitempty"`
	Body   string `yaml:"body" json:"body"`
}

// --- decoding and encoding ---------------------------------------------------

// IsJSON reports whether a path's document is JSON; anything else is YAML.
func IsJSON(path string) bool { return strings.HasSuffix(path, ".json") }

// decode reads a document by its path's extension, refusing unknown keys. An
// empty file is an empty document.
func decode(path string, data []byte, v any) error {
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	if IsJSON(path) {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		return dec.Decode(v)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Encode writes a document the way its path says: JSON for .json, YAML
// otherwise — multi-line text as block scalars, which is what agents and
// people read.
func Encode(path string, v any) ([]byte, error) {
	if IsJSON(path) {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- the review -------------------------------------------------------------

const reviewShape = "{summary, notes: [{path, line, kind, body, rationale}], replies: [{thread, body, resolve}]}"

// ParseReview reads a review_threads document and checks what a schema
// cannot say: a note's path stays in the repository, its kind is one of
// Kinds, its body and rationale say something; a reply names its thread
// once; the whole holds at most MaxItems. What a reply's thread must be —
// one the round loaded — is the engine's to check: it knows the round.
func ParseReview(path string, data []byte) (*Review, error) {
	var r Review
	if err := decode(path, data, &r); err != nil {
		return nil, fmt.Errorf("%s: not a review — %s: %v", path, reviewShape, err)
	}
	if n := len(r.Notes) + len(r.Replies); n > MaxItems {
		return nil, fmt.Errorf("%s: %d notes and replies; at most %d — put the rest in the summary", path, n, MaxItems)
	}
	r.Summary = strings.TrimSpace(r.Summary)
	for i := range r.Notes {
		n := &r.Notes[i]
		n.Path = CleanPath(n.Path)
		switch {
		case !LocalPath(n.Path):
			return nil, fmt.Errorf("notes[%d]: path %q must be a file of the repository, relative to its root", i, n.Path)
		case n.Line < 1:
			return nil, fmt.Errorf("notes[%d]: line must be 1 or more (the line in the changed file), got %d", i, n.Line)
		case !slices.Contains(Kinds, n.Kind):
			return nil, fmt.Errorf("notes[%d]: kind must be one of %s, got %q", i, strings.Join(Kinds, ", "), n.Kind)
		case strings.TrimSpace(n.Body) == "":
			return nil, fmt.Errorf("notes[%d]: body is empty", i)
		case strings.TrimSpace(n.Rationale) == "":
			return nil, fmt.Errorf("notes[%d]: rationale is required — say, for the person who decides whether to post it, what you read and why it matters (it is never posted)", i)
		}
	}
	seen := map[string]bool{}
	for i := range r.Replies {
		c := &r.Replies[i]
		c.Thread = strings.TrimSpace(c.Thread)
		switch {
		case c.Thread == "":
			return nil, fmt.Errorf("replies[%d]: thread is required — the id of a thread on the merge request, as the threads input gives it", i)
		case seen[c.Thread]:
			return nil, fmt.Errorf("replies[%d]: thread %q is answered twice", i, c.Thread)
		case strings.TrimSpace(c.Body) == "":
			return nil, fmt.Errorf("replies[%d]: body is empty", i)
		}
		seen[c.Thread] = true
	}
	return &r, nil
}

// Empty reports a review with nothing to post.
func (r *Review) Empty() bool {
	return r == nil || (r.Summary == "" && len(r.Notes) == 0 && len(r.Replies) == 0)
}

// Posted is a note's text as it goes on the merge request: its kind as a
// leading tag, then the body.
func (n Note) Posted() string {
	return "[" + n.Kind + "] " + strings.TrimSpace(n.Body)
}

// --- the replies ------------------------------------------------------------

const repliesShape = "a list of {thread, did, reply, resolve, rationale}"

// ParseReplies reads a review_replies document: every entry names a thread
// once, says what it did (Dids) and replies with something; a declined
// thread is not resolved and says why in its rationale. Whether the list
// covers the round's threads is CheckReplies, with the round.
func ParseReplies(path string, data []byte) ([]Reply, error) {
	var out []Reply
	if err := decode(path, data, &out); err != nil {
		return nil, fmt.Errorf("%s: not %s: %v", path, repliesShape, err)
	}
	seen := map[string]bool{}
	for i := range out {
		r := &out[i]
		r.Thread = strings.TrimSpace(r.Thread)
		switch {
		case r.Thread == "":
			return nil, fmt.Errorf("replies[%d]: thread is required", i)
		case seen[r.Thread]:
			return nil, fmt.Errorf("replies[%d]: thread %q is answered twice", i, r.Thread)
		case !slices.Contains(Dids, r.Did):
			return nil, fmt.Errorf("replies[%d]: did must be one of %s (what happened to the thread), got %q", i, strings.Join(Dids, ", "), r.Did)
		case strings.TrimSpace(r.Reply) == "":
			return nil, fmt.Errorf("replies[%d]: reply to %q is empty", i, r.Thread)
		case r.Did == DidDeclined && r.Resolve:
			return nil, fmt.Errorf("replies[%d]: a declined thread is not resolved — the reviewer closes a disagreement", i)
		case r.Did == DidDeclined && strings.TrimSpace(r.Rationale) == "":
			return nil, fmt.Errorf("replies[%d]: a declined thread needs a rationale — why the comment was not followed, for the person who posts the reply", i)
		}
		seen[r.Thread] = true
	}
	return out, nil
}

// CheckReplies holds a replies list against the threads a round loaded:
// every reply names one of them, and every one of them is answered — a
// declined one too, so no thread is left hanging.
func CheckReplies(replies []Reply, loaded []string) error {
	answered := map[string]bool{}
	for i, r := range replies {
		if !slices.Contains(loaded, r.Thread) {
			return fmt.Errorf("replies[%d]: thread %q is not one of this round's threads (%s)", i, r.Thread, strings.Join(loaded, ", "))
		}
		answered[r.Thread] = true
	}
	var missing []string
	for _, id := range loaded {
		if !answered[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("replies: no reply for thread(s) %s — every thread the round loaded is answered, with did: declined and why when nothing changed", strings.Join(missing, ", "))
	}
	return nil
}

// --- the threads ------------------------------------------------------------

// ParseThreads reads an mr_threads document.
func ParseThreads(path string, data []byte) (*Threads, error) {
	var t Threads
	if err := decode(path, data, &t); err != nil {
		return nil, fmt.Errorf("%s: not an mr_threads document: %v", path, err)
	}
	seen := map[string]bool{}
	for i, th := range t.Threads {
		switch {
		case th.ID == "":
			return nil, fmt.Errorf("threads[%d]: id is required", i)
		case seen[th.ID]:
			return nil, fmt.Errorf("threads[%d]: thread %q listed twice", i, th.ID)
		}
		seen[th.ID] = true
	}
	return &t, nil
}

// Thread returns the thread with that id, or nil.
func (t *Threads) Thread(id string) *Thread {
	if t == nil {
		return nil
	}
	for i := range t.Threads {
		if t.Threads[i].ID == id {
			return &t.Threads[i]
		}
	}
	return nil
}

// IDs lists the threads' ids, in order.
func (t *Threads) IDs() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.Threads))
	for _, th := range t.Threads {
		out = append(out, th.ID)
	}
	return out
}

// --- paths and text ---------------------------------------------------------

// CleanPath trims a leading "./" and surrounding space from a path.
func CleanPath(p string) string {
	return strings.TrimPrefix(strings.TrimSpace(p), "./")
}

// LocalPath reports whether a path stays inside the repository: not empty,
// not absolute, and not climbing out of it.
func LocalPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") && !strings.Contains(p, "/../") && p != ".."
}

// NormalizeBody reduces a posted text to what two readers would call the
// same note: apothecary's markers gone, a leading "[kind]" tag gone,
// whitespace collapsed, case folded. The engine's duplicate guards compare
// these; nothing posts one.
func NormalizeBody(s string) string {
	s = StripMarkers(s)
	s = strings.TrimSpace(s)
	for _, k := range Kinds {
		if tag := "[" + k + "]"; strings.HasPrefix(strings.ToLower(s), tag) {
			s = strings.TrimSpace(s[len(tag):])
			break
		}
	}
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// StripMarkers removes every apothecary marker (an HTML comment the
// repository host does not render) from a body.
func StripMarkers(s string) string {
	for {
		i := strings.Index(s, "<!-- apothecary ")
		if i < 0 {
			return strings.TrimSpace(s)
		}
		j := strings.Index(s[i:], "-->")
		if j < 0 {
			return strings.TrimSpace(s[:i])
		}
		s = s[:i] + s[i+j+len("-->"):]
	}
}
