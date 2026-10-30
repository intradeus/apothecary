package engine

// Attachments (create_mr, ticket_comment): the files of a stage's
// attachments port — screenshots, mostly — go up to where the body is
// posted, and the body links them. The engine uploads, never the agent: the
// repository host's and the tracker's credentials stay engine-side, as for
// every other call those actions make.
//
// Uploading is not idempotent and need not be: a retry that uploads again
// leaves an unreferenced copy on the host, never a second merge request or
// comment — those are reconciled before anything is uploaded.

import (
	"fmt"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
)

// What one attachments port may carry: enough for a page's worth of
// screenshots, and bounded so a stray build directory is refused rather
// than uploaded. The same bounds cap a files output's snapshots.
const (
	attachmentMaxFiles = 20
	attachmentMaxBytes = 10 << 20
)

// attachmentFiles lists what a stage's attachments port holds, held to the
// limits. Nothing when the port is not bound or its directory holds nothing
// (a required one was checked before the stage ran). A refusal — over a
// limit, a directory that cannot be listed — is routed (on_fail) and
// routed=true returned.
func (x *exec) attachmentFiles(st *flow.Stage) (files []envport.FileEntry, routed bool, err error) {
	dir := inputPath(st, "attachments")
	if dir == "" {
		return nil, false, nil
	}
	files, lerr := x.dirFiles(dir)
	if lerr != nil {
		routed, err = x.actionFail(st, event.ReasonInputInvalid, fmt.Sprintf("attachments %s: %v", dir, lerr))
		return nil, routed, err
	}
	if len(files) > attachmentMaxFiles {
		routed, err = x.actionFail(st, event.ReasonInputInvalid,
			fmt.Sprintf("attachments %s holds %d files; at most %d go with one %s", dir, len(files), attachmentMaxFiles, st.Action))
		return nil, routed, err
	}
	var big []string
	for _, f := range files {
		if f.Size > attachmentMaxBytes {
			big = append(big, fmt.Sprintf("%s (%s)", f.Path, humanSize(f.Size)))
		}
	}
	if len(big) > 0 {
		routed, err = x.actionFail(st, event.ReasonInputInvalid,
			fmt.Sprintf("attachments over %s each: %s", humanSize(attachmentMaxBytes), strings.Join(big, ", ")))
		return nil, routed, err
	}
	return files, false, nil
}

// attach reads each file from the workspace, uploads it, and links the
// uploads in body (linkAttachments). One at a time: a file is held only
// while it goes up.
func (x *exec) attach(body string, files []envport.FileEntry, upload func(name, contentType string, data []byte) (repoport.Attachment, error)) (string, error) {
	linked := make([]linkedFile, 0, len(files))
	for _, f := range files {
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, f.Path)
		if err != nil {
			return "", fmt.Errorf("reading attachment %s: %w", f.Path, err)
		}
		name := path.Base(f.Path)
		ct := contentType(name, data)
		att, err := upload(name, ct, data)
		if err != nil {
			return "", fmt.Errorf("uploading %s: %w", f.Path, err)
		}
		linked = append(linked, linkedFile{path: f.Path, name: name, image: strings.HasPrefix(ct, "image/"), att: att})
	}
	return linkAttachments(body, linked), nil
}

// contentType is a file's media type: by its extension, else by its bytes.
func contentType(name string, data []byte) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return strings.TrimSpace(strings.Split(t, ";")[0])
	}
	return strings.TrimSpace(strings.Split(http.DetectContentType(data[:min(len(data), 512)]), ";")[0])
}

// linkedFile is one uploaded attachment: the workspace path a body names it
// by, and where it went.
type linkedFile struct {
	path  string
	name  string
	image bool
	att   repoport.Attachment
}

// markdown is how the body shows the file: the host's own markdown, else an
// image for an image and a link for the rest.
func (f linkedFile) markdown() string {
	if f.att.Markdown != "" {
		return f.att.Markdown
	}
	md := "[" + f.name + "](" + f.att.Target() + ")"
	if f.image {
		md = "!" + md
	}
	return md
}

// linkAttachments points the body at its uploads. Where the body names a
// file by its workspace path (artifacts/shots/home.png, or ./artifacts/…):
//   - as a markdown link or image target — ![home](artifacts/shots/home.png)
//     — the target becomes the upload's;
//   - anywhere else, bare or in backticks, the path becomes the upload's
//     markdown (the image itself, for an image).
//
// A file the body never names is listed after it: images under
// "## Screenshots", the rest under "## Attachments".
func linkAttachments(body string, files []linkedFile) string {
	var shots, others []string
	for _, f := range files {
		var n, m int
		body, n = replaceLinkTargets(body, f.path, f.att.Target())
		body, m = replaceBarePaths(body, f.path, f.markdown())
		if n+m > 0 {
			continue
		}
		if f.image {
			shots = append(shots, f.markdown())
		} else {
			others = append(others, "- "+f.markdown())
		}
	}
	body = strings.TrimRight(body, "\n")
	if len(shots) > 0 {
		body += "\n\n## Screenshots\n\n" + strings.Join(shots, "\n\n")
	}
	if len(others) > 0 {
		body += "\n\n## Attachments\n\n" + strings.Join(others, "\n")
	}
	return body + "\n"
}

// replaceLinkTargets rewrites markdown link and image targets naming p —
// `](p)`, `](./p)`, with or without a "title" — to target.
func replaceLinkTargets(body, p, target string) (string, int) {
	re := regexp.MustCompile(`(\]\(\s*)(?:\./)?` + regexp.QuoteMeta(p) + `((?:\s+"[^"]*")?\s*\))`)
	n := len(re.FindAllStringIndex(body, -1))
	if n == 0 {
		return body, 0
	}
	return re.ReplaceAllString(body, "${1}"+strings.ReplaceAll(target, "$", "$$")+"${2}"), n
}

// replaceBarePaths replaces each whole-path mention of p (optionally ./ in
// front, optionally in backticks) with repl. A mention inside a longer path
// is not one — artifacts/shots/home.png is not in artifacts/shots/home.png.bak
// — and a link's label naming the path is left as it is (counted, not
// replaced).
func replaceBarePaths(body, p, repl string) (string, int) {
	var b strings.Builder
	n := 0
	for i := 0; ; {
		j := strings.Index(body[i:], p)
		if j < 0 {
			b.WriteString(body[i:])
			break
		}
		start, end := i+j, i+j+len(p)
		if start-2 >= i && body[start-2:start] == "./" {
			start -= 2
		}
		before, after := byte(0), byte(0)
		if start > 0 {
			before = body[start-1]
		}
		if end < len(body) {
			after = body[end]
		}
		whole := !isPathByte(before) && (after == 0 || !isPathByte(after) ||
			(after == '.' && (end+1 == len(body) || !isPathByte(body[end+1]))))
		if !whole {
			b.WriteString(body[i:end])
			i = end
			continue
		}
		n++
		if before == '[' && after == ']' {
			b.WriteString(body[i:end]) // a link's label
			i = end
			continue
		}
		if before == '`' && after == '`' && start-1 >= i {
			start, end = start-1, end+1
		}
		b.WriteString(body[i:start])
		b.WriteString(repl)
		i = end
	}
	return b.String(), n
}

// isPathByte reports whether c can continue a path.
func isPathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-' || c == '.' || c == '/'
}
