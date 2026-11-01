//go:build linux

package main

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

const soOriginalDst = 80 // SO_ORIGINAL_DST (linux netfilter, SOL_IP)

// originalDst recovers the pre-REDIRECT destination via getsockopt(SO_ORIGINAL_DST).
func originalDst(c *net.TCPConn) (*net.TCPAddr, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var out *net.TCPAddr
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		var b [16]byte // struct sockaddr_in
		sz := uint32(len(b))
		if _, _, e := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, uintptr(syscall.SOL_IP),
			uintptr(soOriginalDst), uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&sz)), 0); e != 0 {
			cerr = e
			return
		}
		out = &net.TCPAddr{IP: net.IPv4(b[4], b[5], b[6], b[7]), Port: int(b[2])<<8 | int(b[3])}
	}); err != nil {
		return nil, err
	}
	if cerr != nil {
		return nil, cerr
	}
	if out == nil {
		return nil, fmt.Errorf("no original dst")
	}
	return out, nil
}
