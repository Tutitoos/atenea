//go:build linux

package ipc

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// sameUser asks the kernel who opened this connection.
//
// SO_PEERCRED is answered by the kernel from the peer's own process, not from
// anything the peer sends, so there is nothing here for a caller to claim
// falsely. That is the whole reason this is the check and a token is not: a
// token is a secret two processes share, and this is a fact neither of them
// gets a say in.
//
// Root is refused along with everyone else. A root process can reach this
// socket whatever the mode says and can read the state directly anyway, so
// letting it in would buy nothing and would mean a service run by mistake
// under sudo answering for a user who never asked.
func sameUser(conn *net.UnixConn) (bool, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false, fmt.Errorf("reaching the connection: %w", err)
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return false, fmt.Errorf("asking the kernel for the peer: %w", err)
	}
	if credErr != nil {
		return false, fmt.Errorf("reading peer credentials: %w", credErr)
	}
	return int(cred.Uid) == os.Geteuid(), nil
}
