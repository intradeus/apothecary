//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// writeUnder writes data at path, which must lie strictly below root and
// pass through no symbolic link. It walks from root one component at a
// time, each opened relative to the last with O_NOFOLLOW|O_DIRECTORY: a
// link a session swaps in for a folder between two steps is refused, never
// followed, so the sidecar's write cannot be steered out of the folder it
// names (a check of each folder, then an open by path, could be).
func writeUnder(root, path string, data []byte, mode os.FileMode) error {
	root = filepath.Clean(root)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return fmt.Errorf("%q is not a clean path under %s", path, root)
	}
	if mode == 0 {
		mode = 0o600
	}
	rel, _ := filepath.Rel(root, path)
	parts := strings.Split(rel, string(filepath.Separator))
	dir, err := syscall.Open(root, syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", root, err)
	}
	defer func() { syscall.Close(dir) }()
	for _, part := range parts[:len(parts)-1] {
		if err := syscall.Mkdirat(dir, part, 0o700); err != nil && err != syscall.EEXIST {
			return fmt.Errorf("%s: %w", part, err)
		}
		next, err := syscall.Openat(dir, part, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_RDONLY, 0)
		if err != nil {
			return fmt.Errorf("%s is not a directory (a link?): %w", part, err)
		}
		syscall.Close(dir)
		dir = next
	}
	fd, err := syscall.Openat(dir, parts[len(parts)-1], syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
