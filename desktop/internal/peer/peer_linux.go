//go:build linux

package peer

import (
	"net"

	"golang.org/x/sys/unix"
)

// UID is the user id of the account at the other end of a unix socket
// connection, as the kernel knows it.
func UID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, cerr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
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

// SunPathMax is the size of a unix socket address's path on this system,
// its terminating byte included: a path that long or longer cannot be
// listened on or dialed.
const SunPathMax = 108
