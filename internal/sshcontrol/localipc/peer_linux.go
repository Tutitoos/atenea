//go:build linux

package localipc

import (
	"net"
	"os"
	"syscall"
)

func samePeer(conn *net.UnixConn) (bool, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false, err
	}
	var cred *syscall.Ucred
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		cred, readErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return false, err
	}
	if readErr != nil {
		return false, readErr
	}
	return cred != nil && int(cred.Uid) == os.Geteuid(), nil
}
