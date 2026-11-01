//go:build !linux

package main

import (
	"errors"
	"net"
)

// originalDst needs netfilter's SO_ORIGINAL_DST; the gateway only runs on Linux.
func originalDst(*net.TCPConn) (*net.TCPAddr, error) {
	return nil, errors.New("SO_ORIGINAL_DST is only available on linux")
}
