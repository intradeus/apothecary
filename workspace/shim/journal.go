package main

// journal_read: the run journal's tool (FLOWLANG §9), served on the same MCP
// server as ask_human and offered to the same agents. The ledger in the
// agent's prompt lists the journal, each entry with its reference; this
// reads the rest of it:
//
//	GET /api/runs/{run}/journal          every record of the run
//	GET /api/runs/{run}/journal/{ref}    an attempt whole, a decision, a note
//
// The reference's # goes as %23. Large content never goes back through the
// tool result (it would land in the agent's context): an attempt or the
// records are written under the checkout's .apothecary/journal/ —
// git-excluded, like the attachments — at the path the reference names
// (review#5.md, build#3/decision#1.md, run.md), and the result says where,
// how big and how many lines, for the agent to read with its own tools. A
// short decision or note comes back inline. An attempt's outputs that are
// not text (screenshots) are fetched one at a time as bytes
// (GET …/journal/{ref}?file=<name>) and written as themselves in the folder
// named after the attempt (review#5/shot.png), for the agent to open with
// its own tools. No cache: every call writes the files again from the
// engine.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var journalToolDef = map[string]any{
	"name": "journal_read",
	"description": "Read the run journal: what has happened in this run so far. Your prompt's RUN " +
		"JOURNAL section lists it — the owner's decisions, the agents' notes and each stage's " +
		"attempts — each entry ending with its reference in brackets. An attempt's reference is " +
		"its stage and number (\"review#2\" is attempt 2 of review): with one, this writes " +
		"everything that attempt left — its record, its verdict, its outputs and its full log — " +
		"to one file and returns the file's path; its outputs that are not text, such as " +
		"screenshots, are written as themselves in a folder beside it. A decision or a note is inside its attempt " +
		"(\"build#1/decision#3\", \"build#1/note#1\"; \"run/decision#1\" for one outside any " +
		"attempt): this returns it whole, or writes it to a file when it is long. With no " +
		"reference it writes every record of the run (decisions, notes and each attempt with its " +
		"verdict, no logs) to one file. Read and search the files with your own tools. Each call " +
		"writes the file afresh.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{"type": "string", "description": "a reference from the RUN JOURNAL section: " +
				"<stage>#<n>, <stage>#<n>/decision#<k>, <stage>#<n>/note#<k> or run/decision#<k>; omit for every record"},
		},
	},
}

// repoRoot is the primary checkout, where the journal's files are written
// (the sidecar's default; a var for tests).
var repoRoot = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "repo")
}

// journalDir is where the files go, inside the checkout.
const journalDir = ".apothecary/journal"

// journalEntry is the engine's answer (engine.JournalEntry).
type journalEntry struct {
	Ref    string `json:"ref"`
	File   string `json:"file"`
	Text   string `json:"text"`
	Beside []struct {
		Name string `json:"name"`
	} `json:"beside"`
}

// journalRead fetches one entry and either returns it or writes it to its
// file, returning what the agent reads.
func journalRead(ref string) (string, error) {
	env := readRunEnv()
	path := "/api/runs/" + env["APOTHECARY_RUN_ID"] + "/journal"
	if ref = strings.TrimSpace(ref); ref != "" {
		for seg := range strings.SplitSeq(ref, "/") {
			path += "/" + url.PathEscape(seg) // # as %23
		}
	}
	res, err := callEngine("GET", path, nil, env)
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		// The engine restarted and rotated the run token.
		drain(res)
		res, err = callEngine("GET", path, nil, readRunEnv())
	}
	if err != nil {
		return "", fmt.Errorf("journal_read unavailable: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(text, &e) == nil && e.Error != "" {
			text = []byte(e.Error)
		}
		return "", fmt.Errorf("journal_read failed: engine returned %d: %s", res.StatusCode, strings.TrimSpace(string(text)))
	}
	var entry journalEntry
	if err := json.NewDecoder(res.Body).Decode(&entry); err != nil {
		return "", fmt.Errorf("journal_read failed: bad engine response: %v", err)
	}
	if entry.File == "" {
		return entry.Text, nil
	}
	out, err := writeJournalFile(entry)
	if err != nil || len(entry.Beside) == 0 {
		return out, err
	}
	return out + writeBeside(path, entry), nil
}

// writeBeside fetches an attempt's outputs that are not text, one at a
// time, and writes each as itself in the folder named after the attempt.
// One that cannot be written is said so; it never fails the call, whose
// file is written already.
func writeBeside(path string, entry journalEntry) string {
	if !journalSegment.MatchString(entry.Ref) {
		return " Its outputs that are not text were not written: the engine named a folder outside the journal."
	}
	dir := filepath.Join(repoRoot(), filepath.FromSlash(journalDir), entry.Ref)
	var wrote, failed []string
	for _, f := range entry.Beside {
		if err := fetchBeside(path, dir, f.Name); err != nil {
			failed = append(failed, f.Name+" ("+err.Error()+")")
			continue
		}
		wrote = append(wrote, f.Name)
	}
	var b strings.Builder
	if len(wrote) > 0 {
		fmt.Fprintf(&b, " Its outputs that are not text are written as themselves in %q: %s.", dir, strings.Join(wrote, ", "))
	}
	if len(failed) > 0 {
		b.WriteString(" Could not write: " + strings.Join(failed, "; ") + ".")
	}
	return b.String()
}

// fetchBeside fetches one output's bytes and writes them to dir/name.
func fetchBeside(path, dir, name string) error {
	if !journalSegment.MatchString(name) {
		return errors.New("a name outside the journal")
	}
	q := path + "?file=" + url.QueryEscape(name)
	res, err := callEngine("GET", q, nil, readRunEnv())
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		drain(res)
		res, err = callEngine("GET", q, nil, readRunEnv())
	}
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(text, &e) == nil && e.Error != "" {
			text = []byte(e.Error)
		}
		return fmt.Errorf("engine returned %d: %s", res.StatusCode, strings.TrimSpace(string(text)))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(dir, name)
	_ = os.Remove(file)
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// journalSegment is one segment of a path the engine names: what a
// reference is made of, never a leading dot.
var journalSegment = regexp.MustCompile(`^[A-Za-z0-9_#-][A-Za-z0-9._#-]*$`)

// journalPath checks a path the engine named for a file: relative, at most
// two segments (build#3/decision#1.md), each of journalSegment — nothing
// that could leave the journal's directory.
func journalPath(file string) (string, bool) {
	segs := strings.Split(file, "/")
	if len(segs) > 2 {
		return "", false
	}
	for _, seg := range segs {
		if !journalSegment.MatchString(seg) {
			return "", false
		}
	}
	return filepath.FromSlash(file), true
}

// writeJournalFile writes an entry under the checkout's journal directory,
// replacing what an earlier call (or the agent) left there.
func writeJournalFile(entry journalEntry) (string, error) {
	rel, ok := journalPath(entry.File)
	if !ok {
		return "", errors.New("journal_read failed: the engine named a file outside the journal")
	}
	file := filepath.Join(repoRoot(), filepath.FromSlash(journalDir), rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", fmt.Errorf("journal_read failed: %v", err)
	}
	_ = os.Remove(file) // a read-only copy cannot be overwritten in place
	if err := os.WriteFile(file, []byte(entry.Text), 0o644); err != nil {
		return "", fmt.Errorf("journal_read failed: %v", err)
	}
	what := "everything " + entry.Ref + " left"
	switch {
	case entry.Ref == "run":
		what = "every record of the run"
	case strings.Contains(entry.Ref, "/"):
		what = entry.Ref
	}
	// Quoted: a reference's # starts a comment in a shell when it begins a
	// word, and a quoted path is one word in any shell.
	return fmt.Sprintf("Wrote %s to %q (%s/%s in the checkout): %d bytes, %d lines. Read and search it with your own tools.",
		what, file, journalDir, entry.File, len(entry.Text), strings.Count(entry.Text, "\n")), nil
}
