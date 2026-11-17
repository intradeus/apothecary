//go:build !windows

package cli

import "os"

// createPrivate creates path for writing; it must not exist (O_EXCL: a
// planted symlink is refused too). The file is owner-only from its
// creation: the umask can only take bits from 0600.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// openPrivate opens the key at path for reading.
func openPrivate(path string) (*os.File, error) {
	return os.Open(path)
}

// restrictToOwner leaves the open file f readable and writable by its owner
// only (0600), reporting whether anyone else could read or write it before.
func restrictToOwner(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o077 != 0, f.Chmod(0o600)
}
