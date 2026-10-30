package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/shell"
)

// WriteTree lays a whole directory down in ONE exec: a tar built here rides
// stdin behind a one-line script — bash consumes the first line, `tar`
// inherits the rest (the same trick WriteFile plays with base64). One round
// trip per tree instead of one per file is what makes a 50-file skill
// affordable over `kubectl exec`. The archive is extracted as the remote
// user with no ownership flags, so every entry is owned by that user.
func (a *Adapter) WriteTree(ctx context.Context, env envport.EnvID, dir string, files []envport.TreeFile) error {
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("write tree %s: directory must be absolute", dir)
	}
	archive, err := buildTar(files)
	if err != nil {
		return fmt.Errorf("write tree %s: %w", dir, err)
	}
	q := shell.Quote(dir)
	script := fmt.Sprintf(`rm -rf %s && mkdir -p %s && base64 -d | tar -xf - -C %s`, q, q, q)
	stdin := script + "\n" + base64.StdEncoding.EncodeToString(archive) + "\n"
	_, stderr, code, err := a.inWorkspace(ctx, string(env), nil, []string{"bash", "-eo", "pipefail", "-s"}, []byte(stdin), nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("write tree %s: exit %d: %s", dir, code, shell.Tail(string(stderr), 512))
	}
	return nil
}

// buildTar renders files as a plain archive of regular files: sorted paths,
// uid/gid 0 (ignored on extraction), modes as given (0 = 0644). Go picks the
// simplest header format each entry fits (ustar first), which both GNU and
// busybox tar read.
func buildTar(files []envport.TreeFile) ([]byte, error) {
	sorted := append([]envport.TreeFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	now := time.Now()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	seen := map[string]bool{}
	for _, f := range sorted {
		if err := CheckTreePath(f.Path); err != nil {
			return nil, err
		}
		if seen[f.Path] {
			return nil, fmt.Errorf("duplicate path %q", f.Path)
		}
		seen[f.Path] = true
		mode := f.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     f.Path,
			Mode:     int64(mode),
			Size:     int64(len(f.Data)),
			ModTime:  now,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckTreePath is the port's rule for a TreeFile path: relative, forward
// slashes, no empty, "." or ".." segments, no control characters. Tree
// paths are user-authored (skill files), so an escape here would write
// anywhere the remote user can.
func CheckTreePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return fmt.Errorf("invalid path %q", p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid path %q", p)
		}
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path %q", p)
		}
	}
	return nil
}
