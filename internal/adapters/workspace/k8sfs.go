package workspace

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strings"

	"apothecary/internal/shell"
)

// execFS is sandboxFS inside a sandbox pod: each call is one small script run
// by `kubectl exec` in the sandbox container (bash, coreutils — the sandbox
// image's own tools, never the repo's).
type execFS struct {
	h    *k8sHost
	name string
}

func (e execFS) sh(ctx context.Context, script string, stdin []byte) ([]byte, int, error) {
	var full []byte
	if stdin != nil {
		// One script line, then the payload: bash reads the line, the
		// script's own reader takes the rest.
		full = append([]byte(script+"\n"), stdin...)
	} else {
		full = []byte(script + "\n")
	}
	out, stderr, code, err := e.h.runInner(ctx, e.name, "bash", []string{"-s"}, full, nil)
	if err == nil && code != 0 && code != 44 && code != 1 {
		err = fmt.Errorf("sandbox %s: exit %d: %s", e.name, code, shell.Tail(string(stderr), 512))
	}
	return out, code, err
}

func (e execFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	p := shell.Quote(path)
	out, code, err := e.sh(ctx, fmt.Sprintf(`if [ ! -f %s ]; then exit 44; fi; base64 < %s`, p, p), nil)
	if err != nil {
		return nil, err
	}
	if code == 44 {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	}
	if code != 0 {
		return nil, fmt.Errorf("read %s: exit %d", path, code)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(string(out), "\n", ""))
}

func (e execFS) WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	p := shell.Quote(path)
	script := fmt.Sprintf(`mkdir -p "$(dirname %s)" && base64 -d > %s && chmod %o %s`, p, p, mode.Perm(), p)
	_, code, err := e.sh(ctx, script, []byte(base64.StdEncoding.EncodeToString(data)+"\n"))
	if err == nil && code != 0 {
		err = fmt.Errorf("write %s: exit %d", path, code)
	}
	return err
}

func (e execFS) MkdirAll(ctx context.Context, path string) error {
	_, code, err := e.sh(ctx, "mkdir -p "+shell.Quote(path), nil)
	if err == nil && code != 0 {
		err = fmt.Errorf("mkdir %s: exit %d", path, code)
	}
	return err
}

func (e execFS) test(ctx context.Context, expr string) bool {
	_, code, err := e.sh(ctx, expr, nil)
	return err == nil && code == 0
}

func (e execFS) Exists(ctx context.Context, path string) bool {
	p := shell.Quote(path)
	return e.test(ctx, fmt.Sprintf(`[ -e %s ] || [ -L %s ]`, p, p))
}

func (e execFS) IsRegular(ctx context.Context, path string) bool {
	p := shell.Quote(path)
	return e.test(ctx, fmt.Sprintf(`[ -f %s ] && [ ! -L %s ]`, p, p))
}

func (e execFS) Walk(ctx context.Context, dir string) ([]string, error) {
	p := shell.Quote(dir)
	out, _, err := e.sh(ctx, fmt.Sprintf(`[ -d %s ] || exit 0; find %s -type f`, p, p), nil)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

var _ sandboxFS = execFS{}
