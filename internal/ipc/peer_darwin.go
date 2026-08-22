//go:build darwin

package ipc

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// sameUser asks macOS for the peer credentials attached to the Unix socket.
// File permissions remain the first barrier; LOCAL_PEERCRED closes the gap
// where a socket's mode is widened after it was created.
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
		return false, fmt.Errorf("asking macOS for the peer: %w", err)
	}
	if credErr != nil {
		return false, fmt.Errorf("reading macOS peer credentials: %w", credErr)
	}
	return uint32(os.Geteuid()) == cred.Uid, nil
}
