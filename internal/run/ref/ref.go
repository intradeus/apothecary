// Package ref is the one naming standard for what a run holds (FLOWLANG
// §4, References): the readable name agents and people use for a stage's
// attempt and for what is inside one. Two characters, one meaning each:
// `#` is "number n of", `/` is "inside".
//
//	review#5             the fifth attempt of review
//	stage-2#3            the third attempt of stage-2
//	settle.resolve#2     an attempt of a sequence's stage
//	build#3/decision#1   the first ping raised in or about build#3
//	build#3/note#2       the second note in build#3's notes file
//	run                  the run as a whole
//	run/decision#1       the first ping raised outside any attempt
//
// A reference is not an id: ids (run, ping, snapshot) are the opaque keys
// the API and the database use; a reference is what a person or an agent
// reads, quotes and asks for. Every reference is a function of the run's
// log, so it is the same after a restart.
//
// `#` is reserved: no name the flow defines or the engine stores holds it
// (stage ids are slugs, port names and output paths refuse it, stored file
// names have it replaced), so a reference never collides with anything.
package ref

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Run is the run as a whole: every record of it, and the root of what
	// happened outside any attempt.
	Run = "run"
	// Decision is the kind of a ping raised in or about an attempt: an
	// agent's question, a gate's, a debug or impasse decision, a manual
	// stage's — whatever its type, numbered in the order they were raised.
	Decision = "decision"
	// Note is the kind of a note an agent left for the run journal.
	Note = "note"
)

// stageRe is a stage id as the flow has it: a slug, or a sequence path of
// slugs joined by dots (flow.idPathRe).
var stageRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// Ref is a parsed reference.
type Ref struct {
	// Stage and Attempt name the attempt; both empty for the run.
	Stage   string
	Attempt int
	// Kind is Decision or Note for an entry inside, "" for the attempt (or
	// the run) itself; N is its number.
	Kind string
	N    int
}

// Attempt is an attempt's reference: review#5.
func Attempt(stage string, n int) string { return stage + "#" + strconv.Itoa(n) }

// In is an entry inside an attempt — or, with attempt "", inside the run:
// build#3/decision#1, run/decision#2.
func In(attempt, kind string, n int) string {
	if attempt == "" {
		attempt = Run
	}
	return attempt + "/" + kind + "#" + strconv.Itoa(n)
}

// ErrMalformed: a string that is not a reference.
var ErrMalformed = errors.New("not a reference")

// Grammar is the reference grammar in words, for an error to show.
const Grammar = `<stage>#<n> (review#5), <stage>#<n>/decision#<k>, <stage>#<n>/note#<k>, run, or run/decision#<k>`

// Parse reads a reference back. It refuses anything the grammar does not
// produce: no number, a zero or a leading zero, an unknown kind, a note
// outside an attempt, a third segment.
func Parse(s string) (Ref, error) {
	bad := func() (Ref, error) { return Ref{}, fmt.Errorf("%w: %q (%s)", ErrMalformed, s, Grammar) }
	head, tail, inside := strings.Cut(s, "/")
	var r Ref
	if head != Run {
		stage, n, ok := numbered(head)
		if !ok || !stageRe.MatchString(stage) {
			return bad()
		}
		r.Stage, r.Attempt = stage, n
	}
	if !inside {
		return r, nil
	}
	kind, n, ok := numbered(tail)
	switch {
	case !ok, kind != Decision && kind != Note, kind == Note && head == Run:
		return bad()
	}
	r.Kind, r.N = kind, n
	return r, nil
}

// numbered splits name#n, n a positive decimal with no leading zero.
func numbered(s string) (string, int, bool) {
	name, num, ok := strings.Cut(s, "#")
	if !ok || name == "" || num == "" || num[0] == '0' || strings.Contains(num, "#") {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || strconv.Itoa(n) != num {
		return "", 0, false
	}
	return name, n, true
}

// IsRun reports whether the reference is the run's own, or inside it.
func (r Ref) IsRun() bool { return r.Stage == "" }

// AttemptRef is the attempt the reference is (or is inside): "" for the run.
func (r Ref) AttemptRef() string {
	if r.IsRun() {
		return ""
	}
	return Attempt(r.Stage, r.Attempt)
}

// String formats the reference: Parse(r.String()) is r.
func (r Ref) String() string {
	base := Run
	if !r.IsRun() {
		base = Attempt(r.Stage, r.Attempt)
	}
	if r.Kind == "" {
		return base
	}
	return base + "/" + r.Kind + "#" + strconv.Itoa(r.N)
}
