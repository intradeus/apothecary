package engine

// Ports, checked (FLOWLANG §4): every file a stage reads must be there before
// it runs (unless the port is optional) and hold its port's shape — a type
// the engine checks with a schema, a flow schema — and every file it writes
// must hold its shape once it ends. A writer's check catches its own
// mistakes; a reader's catches what something outside the flow wrote.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/shell"
)

// structuredFile is one file a stage reads or writes, and the schemas it
// must satisfy (none: it only has to be there). A dir is a files port: a
// directory that has to hold at least one file, and no schema.
type structuredFile struct {
	path     string
	optional bool
	dir      bool
	schemas  []*flow.DataSchema
}

// checkFiles reads and validates each file against every one of its
// schemas: the paths that are not there (an optional one is let go), and
// every violation of those that are — all at once, for the next attempt to
// fix them together.
func (x *exec) checkFiles(files []structuredFile) (missing, invalid []string) {
	for _, f := range files {
		if f.dir {
			if held, _ := x.dirFiles(f.path); len(held) == 0 && !f.optional {
				missing = append(missing, emptyDir(f.path))
			}
			continue
		}
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.EnvID, f.path)
		if err != nil {
			if !f.optional {
				missing = append(missing, f.path)
			}
			continue
		}
		for _, s := range f.schemas {
			if err := s.Check(f.path, data); err != nil {
				invalid = append(invalid, err.Error())
			}
		}
	}
	return missing, invalid
}

// portFiles are the file ports of a list — engine sources dropped — with the
// schemas each must hold.
func (x *exec) portFiles(ports []flow.Port) []structuredFile {
	var out []structuredFile
	for _, p := range ports {
		if flow.IsEngineSource(p.Value) {
			continue
		}
		out = append(out, structuredFile{flow.FilePath(p.Value), p.Optional, p.Type == agentspec.TypeFiles, x.rs.Flow.PortSchemas(p)})
	}
	return out
}

// dirFiles is what a files port's directory holds. A directory that is not
// there holds nothing; one that cannot be listed (it resolves outside the
// checkout) is an error, and holds nothing either.
func (x *exec) dirFiles(dir string) ([]envport.FileEntry, error) {
	files, err := x.res.Envp.ListFiles(x.ctx, x.rs.EnvID, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return files, err
}

// emptyDir names a files port that holds nothing, the way a missing file is
// named by its path.
func emptyDir(dir string) string { return dir + " (no file in it)" }

// checkStructuredOutputs validates every output of a stage that declares a
// structure, after the existence check: what is wrong, or "".
func (x *exec) checkStructuredOutputs(st *flow.Stage) string {
	missing, invalid := x.checkFiles(x.portFiles(st.Outputs))
	for _, m := range missing { // unreachable after the existence check
		invalid = append(invalid, m+": not readable")
	}
	return strings.Join(invalid, "\n")
}

// checkStructuredInputs checks the files a stage reads before it starts:
// the reason (input_missing or input_invalid) and what is wrong, or "" when
// they hold.
func (x *exec) checkStructuredInputs(st *flow.Stage) (reason, msg string) {
	missing, invalid := x.checkFiles(x.portFiles(st.Inputs))
	switch {
	case len(missing) > 0:
		return event.ReasonInputMissing, fmt.Sprintf("declared input(s) not readable: %s", strings.Join(missing, ", "))
	case len(invalid) > 0:
		return event.ReasonInputInvalid, strings.Join(invalid, "\n")
	}
	return "", ""
}

// commitMessage is a commit_message file, checked against its built-in
// schema: a one-line subject, an optional body.
type commitMessage struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// text is the message git records: the subject, then the body after a
// blank line.
func (m commitMessage) text() string {
	if body := strings.TrimSpace(m.Body); body != "" {
		return strings.TrimSpace(m.Subject) + "\n\n" + body + "\n"
	}
	return strings.TrimSpace(m.Subject) + "\n"
}

func parseCommitMessage(path string, data []byte) (commitMessage, error) {
	var m commitMessage
	if err := flow.TypeSchema(agentspec.TypeCommitMessage).Check(path, data); err != nil {
		return m, err
	}
	v, _ := flow.DecodeStructured(path, data) // decoded once already by Check
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	return m, err
}

// commitScript stages everything and commits it with the message read from
// stdin — a checkout with nothing to commit is left alone, so a re-entry
// after the commit landed is a no-op.
const commitScript = `git add -A && if git diff --cached --quiet; then echo "nothing to commit"; else printf '%%s' %s | git commit -F -; fi`

// actionCommit commits the uncommitted work: the subject's checkout when the
// chain acts on one merge request, every checkout otherwise — what
// push_branch publishes next. The message lives in the primary checkout,
// where the agent that wrote it works.
func (x *exec) actionCommit(st *flow.Stage) (bool, error) {
	path := inputPath(st, "message")
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.EnvID, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return x.actionFail(st, event.ReasonInputMissing, "declared input(s) not readable: "+path)
		}
		return x.infraFail(st, "commit: "+err.Error())
	}
	msg, err := parseCommitMessage(path, data)
	if err != nil {
		return x.actionFail(st, event.ReasonInputInvalid, err.Error())
	}
	cos := x.rs.Checkouts
	if co := x.rs.SubjectCheckout(); co != nil {
		cos = []event.Checkout{*co}
	}
	script := fmt.Sprintf(commitScript, shell.Quote(msg.text()))
	for _, co := range cos {
		res, err := x.gitIn(co.Dir, script)
		if err != nil {
			return x.infraFail(st, "commit: "+err.Error())
		}
		if res.ExitCode != 0 {
			return x.actionFail(st, event.ReasonExitNonzero, fmt.Sprintf("committing in %s was refused: %s", co.Name, shell.Tail(res.Output, 2<<10)))
		}
	}
	return false, nil
}
