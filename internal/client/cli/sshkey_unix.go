//go:build !windows

package cli

import "os"

// restrictToOwner leaves the open file f readable and writable by its owner
// only (0600), reporting whether anyone else could read or write it before.
func restrictToOwner(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o077 != 0, f.Chmod(0o600)
}
