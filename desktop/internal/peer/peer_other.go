//go:build !linux && !darwin

package peer

import (
	"errors"
	"net"
)

func UID(*net.UnixConn) (int, error) {
	return -1, errors.New("the control socket runs on macOS and Linux only for now")
}

const SunPathMax = 108
