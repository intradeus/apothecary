package workspace

import (
	"bytes"
	"embed"
	"io/fs"
	"testing"
)

// TestEmbedsNoBinaries guards against an in-tree `go build` output (e.g.
// workspace/gateway/gateway) being embedded: it would ship inside apothecaryd,
// land in the docker build context and change the egress-gw image hash.
// .gitignore can't prevent that — go:embed reads the working tree. Build into
// build/ instead (`task build`).
func TestEmbedsNoBinaries(t *testing.T) {
	for name, fsys := range map[string]embed.FS{
		"BaseContext": BaseContext,
		"Images":      Images,
	} {
		_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				return err
			}
			if bytes.HasPrefix(data, []byte("\x7fELF")) {
				t.Errorf("%s embeds compiled binary %s — delete it and build into build/ (task build)", name, path)
			}
			return nil
		})
	}
}
