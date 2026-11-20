//go:build linux

package main

// Landlock (Documentation/userspace-api/landlock.rst), by its three system
// calls: no library, so no licence to carry and nothing between the spec
// and the kernel. The rule set is built for the ABI the kernel reports: a
// right the kernel does not know is neither handled nor granted. Network
// rights are left unhandled: the gateway decides egress, not the session.

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1 << 0
	landlockRulePathBeneath      = 1

	// oPath is O_PATH, which package syscall does not name: the shim builds
	// on the standard library alone (workspace/images/shim).
	oPath = 0o10000000
)

// Filesystem access rights, by the ABI that introduced them.
const (
	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llReadFile   = 1 << 2
	llReadDir    = 1 << 3
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13 // ABI 2
	llTruncate   = 1 << 14 // ABI 3
	llIoctlDev   = 1 << 15 // ABI 5
	// llResolveUnix (ABI 9): connect to a pathname unix socket. Handled
	// and granted nowhere, a session reaches no socket in the pod but its
	// own (the agent host's is also behind a token, for older kernels).
	llResolveUnix = 1 << 16
)

// minLandlockABI is the oldest Landlock a session runs under: 6 (Linux
// 6.12), the first to scope signals and abstract sockets to the domain.
// Below it, sessions sharing the pod's user could signal each other and the
// agent host, and below 3 truncate each other's files.
const minLandlockABI = 6

// Scopes (ABI 6): a confined process may not signal, nor connect to an
// abstract unix socket of, a process outside its own domain — the shim and
// the other sessions included.
const (
	llScopeAbstractUnixSocket = 1 << 0
	llScopeSignal             = 1 << 1
)

// fileRights are the rights a rule on a regular file may carry; the rest
// only mean something on a directory.
const fileRights = llExecute | llWriteFile | llReadFile | llTruncate | llIoctlDev

// handledFS is every filesystem right the kernel's ABI knows: what the rule
// set forbids unless a rule grants it.
func handledFS(abi int) uint64 {
	h := uint64(llExecute | llWriteFile | llReadFile | llReadDir | llRemoveDir | llRemoveFile |
		llMakeChar | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeBlock | llMakeSym)
	if abi >= 2 {
		h |= llRefer
	}
	if abi >= 3 {
		h |= llTruncate
	}
	if abi >= 5 {
		h |= llIoctlDev
	}
	if abi >= 9 {
		h |= llResolveUnix
	}
	return h
}

// The rights each list of the spec grants, before the ABI masks them.
const (
	rightsRead    = llReadFile | llReadDir
	rightsWrite   = llReadFile | llReadDir | llWriteFile | llRemoveDir | llRemoveFile | llMakeDir | llMakeReg | llMakeSym | llMakeSock | llMakeFifo | llRefer | llTruncate
	rightsExec    = llExecute | llReadFile
	rightsDevices = llReadFile | llReadDir | llWriteFile | llTruncate | llIoctlDev
)

// landlockABI is the kernel's Landlock ABI version; 0 when it has none (or
// it is disabled).
func landlockABI() int {
	v, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// restrictAndExec confines this thread to spec and executes argv on it, so
// the program starts confined. It returns only on failure.
func restrictAndExec(spec confineSpec, argv, env []string) error {
	// Landlock and no_new_privs bind the calling thread; execve then
	// replaces the whole process from it. Both must be the same thread.
	runtime.LockOSThread()
	if err := restrictSelf(spec); err != nil {
		return err
	}
	return syscall.Exec(argv[0], argv, env)
}

// restrictSelf applies spec to the calling thread.
func restrictSelf(spec confineSpec) error {
	abi := landlockABI()
	if abi < minLandlockABI {
		return errNoLandlock
	}
	handled := handledFS(abi)
	attr := struct{ fs, net, scoped uint64 }{fs: handled}
	size := unsafe.Sizeof(attr.fs) // ABI 1-3: handled_access_fs alone
	if abi >= 4 {
		size += unsafe.Sizeof(attr.net)
	}
	if abi >= 6 {
		attr.scoped = llScopeAbstractUnixSocket | llScopeSignal
		size += unsafe.Sizeof(attr.scoped)
	}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), size, 0)
	runtime.KeepAlive(&attr)
	if errno != 0 {
		return fmt.Errorf("landlock: create the rule set: %w", errno)
	}
	defer syscall.Close(int(fd))
	for _, r := range []struct {
		paths  []string
		rights uint64
		files  bool // the list names files only (exec)
	}{
		{spec.Read, rightsRead, false},
		{spec.Write, rightsWrite, false},
		{spec.Exec, rightsExec, true},
		{spec.Devices, rightsDevices, false},
	} {
		for _, p := range r.paths {
			if err := addPathRule(int(fd), p, r.rights&handled, r.files); err != nil {
				return err
			}
		}
	}
	if err := prctlNoNewPrivs(); err != nil {
		return err
	}
	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock: restrict: %w", errno)
	}
	return nil
}

// addPathRule grants rights beneath path. A regular file takes only the
// rights a file can hold; the exec list must name files, so a directory
// there is an error rather than every program beneath it.
func addPathRule(ruleset int, path string, rights uint64, filesOnly bool) error {
	pfd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("landlock: %s: %w", path, err)
	}
	defer syscall.Close(pfd)
	var st syscall.Stat_t
	if err := syscall.Fstat(pfd, &st); err != nil {
		return fmt.Errorf("landlock: %s: %w", path, err)
	}
	isDir := st.Mode&syscall.S_IFMT == syscall.S_IFDIR
	switch {
	case filesOnly && isDir:
		return fmt.Errorf("landlock: %s is a directory; the exec list names programs", path)
	case !isDir:
		rights &= fileRights
	}
	// struct landlock_path_beneath_attr is packed: a u64 then an s32.
	var attr [12]byte
	binary.NativeEndian.PutUint64(attr[0:8], rights)
	binary.NativeEndian.PutUint32(attr[8:12], uint32(int32(pfd)))
	_, _, errno := syscall.Syscall6(sysLandlockAddRule, uintptr(ruleset), landlockRulePathBeneath,
		uintptr(unsafe.Pointer(&attr[0])), 0, 0, 0)
	runtime.KeepAlive(&attr)
	if errno != 0 {
		return fmt.Errorf("landlock: rule for %s: %w", path, errno)
	}
	return nil
}

func prctlNoNewPrivs() error {
	const prSetNoNewPrivs = 38
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("no_new_privs: %w", errno)
	}
	return nil
}

// landlockReport says what this kernel enforces, for the probe.
func landlockReport() string {
	abi := landlockABI()
	if abi < 1 {
		return "landlock: none"
	}
	if abi < minLandlockABI {
		return fmt.Sprintf("landlock: abi %d, under the %d sessions need", abi, minLandlockABI)
	}
	return fmt.Sprintf("landlock: abi %d (pid %d)", abi, os.Getpid())
}
