//go:build freebsd

package ipc

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// sameUser asks FreeBSD for LOCAL_PEERCRED, the same kernel-backed identity
// check used on macOS. File permissions remain the first barrier; the peer
// credential closes the gap if the socket path is widened later.
func sameUser(conn *net.UnixConn) (bool, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false, fmt.Errorf("reaching the connection: %w", err)
	}
	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return false, fmt.Errorf("asking FreeBSD for the peer: %w", err)
	}
	if credErr != nil {
		return false, fmt.Errorf("reading FreeBSD peer credentials: %w", credErr)
	}
	return uint32(os.Geteuid()) == cred.Uid, nil
}
