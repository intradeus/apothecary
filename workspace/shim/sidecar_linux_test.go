//go:build linux

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// socketOwner is the pid serving a unix socket.
func socketOwner(t *testing.T, sock string) int {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.(*net.UnixConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || cerr != nil {
		t.Fatal(err, cerr)
	}
	return int(cred.Pid)
}

// --daemon returns once the detached sidecar serves, in its own session and
// with its output in the log; its caller's exit does not touch it.
func TestRunSidecarDaemon(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "sidecar.sock")
	logPath := filepath.Join(dir, "logs", "sidecar.log")
	r := shim(t, "", "sidecar", "--daemon", "--listen", sock, "--repo", dir, "--repos", dir, "--log", logPath, "--ssh", "off")
	if r.code != 0 {
		t.Fatalf("sidecar --daemon = %+v", r)
	}
	pid := socketOwner(t, sock)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGUSR2) })
	if h := sidecarHealth(t, sock); h["repo"] != dir {
		t.Errorf("health = %v", h)
	}
	if sid, _, e := syscall.RawSyscall(syscall.SYS_GETSID, uintptr(pid), 0, 0); e != 0 || int(sid) != pid {
		t.Errorf("session of %d = %d, %v", pid, sid, e)
	}
	waitFor(t, func() bool {
		log, _ := os.ReadFile(logPath)
		return strings.Contains(string(log), "sidecar: serving on "+sock)
	})
}
