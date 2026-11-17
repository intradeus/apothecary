//go:build windows

package cli

import (
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ownerOnly is a security descriptor whose protected DACL (nothing inherited
// from the directory) grants the current user full control and nobody else
// anything: what Windows' OpenSSH asks of a private key.
func ownerOnly() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
}

// createPrivate creates path for writing; it must not exist (CREATE_NEW: a
// planted link is refused too). The file is owner-only from its creation,
// the descriptor being given to CreateFile, and nobody else may open it
// (no sharing) while this handle is open.
func createPrivate(path string) (*os.File, error) {
	sd, err := ownerOnly()
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// openPrivate opens the key at path for reading, with the right to replace
// its DACL (restrictToOwner).
func openPrivate(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.WRITE_DAC, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// restrictToOwner gives f, opened by openPrivate, ownerOnly's DACL, on
// that handle. Whether the old DACL was looser is not told: it is always
// replaced.
func restrictToOwner(f *os.File) (bool, error) {
	sd, err := ownerOnly()
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	return false, windows.SetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
