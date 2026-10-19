//go:build !linux && !darwin

package main

import (
	"errors"
	"net"
)

func peerUID(*net.UnixConn) (int, error) {
	return -1, errors.New("the control socket runs on macOS and Linux only for now")
}

const sunPathMax = 108
