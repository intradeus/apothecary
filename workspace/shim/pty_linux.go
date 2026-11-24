//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// startPTY runs cmd on a fresh pseudo-terminal and returns its master side.
// Linux only, stdlib only: /dev/ptmx + the three ioctls a PTY needs.
//
// The ioctls go through the file's raw conn, never Fd(): Fd() would put the
// master in blocking mode, where a Close waits for a Read in flight instead
// of ending it, and a terminal session could not be hung up while a
// background job still holds its slave side.
func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var unlock int32
	if err := fileIoctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		master.Close()
		return nil, fmt.Errorf("unlock pty: %w", err)
	}
	var n uint32
	if err := fileIoctl(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		master.Close()
		return nil, fmt.Errorf("pty number: %w", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, err
	}
	defer slave.Close()
	_ = resizePTY(master, cols, rows)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	return master, nil
}

func resizePTY(master *os.File, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	ws := struct{ Row, Col, X, Y uint16 }{Row: uint16(rows), Col: uint16(cols)}
	return fileIoctl(master, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

// ptyForeground is the terminal's foreground process group (TIOCGPGRP on
// the master answers for the slave side).
func ptyForeground(master *os.File) (int, error) {
	var pgrp int32
	if err := fileIoctl(master, syscall.TIOCGPGRP, unsafe.Pointer(&pgrp)); err != nil {
		return 0, err
	}
	return int(pgrp), nil
}

// fileIoctl runs one ioctl on f, holding f open for the call: a Close racing
// it fails the call rather than reaching a reused descriptor.
func fileIoctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
			ierr = e
		}
	}); err != nil {
		return err
	}
	return ierr
}
