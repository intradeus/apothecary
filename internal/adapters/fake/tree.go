package fake

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"apothecary/internal/ports"
)

// TreeCall is one recorded WriteTree.
type TreeCall struct {
	Env   ports.EnvID
	Dir   string
	Count int
}

// WriteTree replaces dir's subtree in Files (and Modes) with files, applying
// the port's path rules so an engine bug that would escape the directory
// fails here the way it would on a real workspace.
func (p *EnvProvisioner) WriteTree(_ context.Context, id ports.EnvID, dir string, files []ports.TreeFile) error {
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("write tree %s: directory must be absolute", dir)
	}
	for _, f := range files {
		if err := checkTreePath(f.Path); err != nil {
			return fmt.Errorf("write tree %s: %w", dir, err)
		}
	}
	e, err := p.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	prefix := strings.TrimSuffix(dir, "/") + "/"
	for k := range e.Files {
		if strings.HasPrefix(k, prefix) {
			delete(e.Files, k)
			delete(e.Modes, k)
		}
	}
	if e.Modes == nil {
		e.Modes = map[string]fs.FileMode{}
	}
	for _, f := range files {
		mode := f.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		e.Files[prefix+f.Path] = append([]byte(nil), f.Data...)
		e.Modes[prefix+f.Path] = mode
	}
	p.Trees = append(p.Trees, TreeCall{Env: id, Dir: dir, Count: len(files)})
	return nil
}

func checkTreePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return fmt.Errorf("invalid path %q", p)
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path %q", p)
		}
	}
	return nil
}
