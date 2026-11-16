package main

// journal_read: the run journal's tool (FLOWLANG §9), served on the same MCP
// server as ask_human and offered to the same agents. The ledger in the
// agent's prompt lists the journal; this reads the rest of it:
//
//	GET /api/runs/{run}/journal          every record of the run
//	GET /api/runs/{run}/journal/{id}     a pass whole, or a decision
//
// Large content never goes back through the tool result (it would land in
// the agent's context): a pass or the records are written under the
// checkout's .apothecary/journal/ — git-excluded, like the attachments — and
// the result says where, how big and how many lines, for the agent to read
// with its own tools. A short decision comes back inline. No cache: every
// call writes the file again from the engine.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var journalToolDef = map[string]any{
	"name": "journal_read",
	"description": "Read the run journal: what has happened in this run so far. Your prompt's RUN " +
		"JOURNAL section lists it — the owner's decisions and each stage's passes, each with " +
		"an id. With a pass id (<stage>-<n>, e.g. \"review-2\") it writes everything that pass " +
		"left — its record, its verdict, its outputs and its full log — to one file and returns " +
		"the file's path; with a decision id (\"decision-3\") it returns that decision whole; " +
		"with no id it writes every record of the run (decisions and each pass with its " +
		"verdict, no logs) to one file. Read and search the files with your own tools. Each " +
		"call writes the file afresh.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "a pass (<stage>-<n>) or a decision (decision-<n>); omit for every record"},
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
	ID   string `json:"id"`
	File string `json:"file"`
	Text string `json:"text"`
}

// journalRead fetches one entry and either returns it or writes it to its
// file, returning what the agent reads.
func journalRead(id string) (string, error) {
	env := readRunEnv()
	path := "/api/runs/" + env["APOTHECARY_RUN_ID"] + "/journal"
	if id = strings.TrimSpace(id); id != "" {
		path += "/" + url.PathEscape(id)
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
		text, _ := io.ReadAll(io.LimitReader(res.Body, 300))
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
	return writeJournalFile(entry)
}

// writeJournalFile writes an entry under the checkout's journal directory,
// replacing what an earlier call (or the agent) left there.
func writeJournalFile(entry journalEntry) (string, error) {
	name := filepath.Base(entry.File)
	if name != entry.File || name == "." || strings.HasPrefix(name, ".") {
		return "", errors.New("journal_read failed: the engine named a file outside the journal")
	}
	dir := filepath.Join(repoRoot(), filepath.FromSlash(journalDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("journal_read failed: %v", err)
	}
	file := filepath.Join(dir, name)
	_ = os.Remove(file) // a read-only copy cannot be overwritten in place
	if err := os.WriteFile(file, []byte(entry.Text), 0o644); err != nil {
		return "", fmt.Errorf("journal_read failed: %v", err)
	}
	what := "pass " + entry.ID
	switch {
	case entry.ID == "records":
		what = "every record of the run"
	case strings.HasPrefix(entry.ID, "decision-"):
		what = entry.ID
	}
	return fmt.Sprintf("Wrote %s to %s (%s/%s in the checkout): %d bytes, %d lines. Read and search it with your own tools.",
		what, file, journalDir, name, len(entry.Text), strings.Count(entry.Text, "\n")), nil
}
