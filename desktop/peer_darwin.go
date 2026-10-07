//go:build darwin

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the user id of the account at the other end of a unix socket
// connection, as the kernel knows it.
func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, cerr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			cerr = err
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return -1, err
	}
	return uid, cerr
}

// sunPathMax is the size of a unix socket address's path on this system,
// its terminating byte included: a path that long or longer cannot be
// listened on or dialed.
const sunPathMax = 104
