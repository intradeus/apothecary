package workspace

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"testing"
)

// TestEmbedsNoBinaries guards against an in-tree `go build` output (e.g.
// workspace/gateway/gateway) being embedded: it would ship inside apothecaryd,
// land in the docker build context and change the egress-gw image hash.
// .gitignore can't prevent that — go:embed reads the working tree. Build into
// build/ instead (`task build:bin`).
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
				t.Errorf("%s embeds compiled binary %s — delete it and build into build/ (task build:bin)", name, path)
			}
			return nil
		})
	}
}

// TestTreeHoldsOnlyRegularFiles guards the docker build context the images
// are built from (deploy/images.sh builds shim, gateway and browser from this
// directory): go:embed skips a socket or a pipe, but `docker build` ships it as
// an empty file, so the image's shim hash stops matching the engine's. A test
// that binds a unix socket in its package directory leaves exactly that.
func TestTreeHoldsOnlyRegularFiles(t *testing.T) {
	_ = fs.WalkDir(os.DirFS("."), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			t.Errorf("workspace/%s is a %v, not a regular file — it would ship in the image build context; delete it", path, d.Type())
		}
		return nil
	})
}

func TestDockerfileReturnsContent(t *testing.T) {
	for _, name := range []string{"shim", "gateway", "browser", "tools"} {
		if Dockerfile(name) == "" {
			t.Errorf("Dockerfile(%q) is empty", name)
		}
	}
}

func TestDockerfileMissingPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Dockerfile of a missing image must panic")
		}
	}()
	Dockerfile("no-such-image")
}

func TestHashesStableAndDistinct(t *testing.T) {
	sh, tools := ShimSourceHash(), ToolsHash()
	if len(sh) != 16 || len(tools) != 16 {
		t.Errorf("hash lengths: shim %d tools %d", len(sh), len(tools))
	}
	if sh != ShimSourceHash() || tools != ToolsHash() {
		t.Error("hashes must be stable")
	}
	if sh == tools {
		t.Error("shim and tools hashes must differ")
	}
}

func TestHashTreeInvalidDirIsEmpty(t *testing.T) {
	// fs.Sub rejects an invalid path (e.g. one with ".."), so hashTree returns
	// the empty string rather than hashing anything.
	if got := hashTree(BaseContext, "../escape"); got != "" {
		t.Errorf("hashTree of an invalid dir = %q, want empty", got)
	}
}
