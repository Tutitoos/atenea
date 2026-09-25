//go:build darwin

package localipc

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func samePeer(conn *net.UnixConn) (bool, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false, err
	}
	var cred *unix.Xucred
	var readErr error
	if err := raw.Control(func(fd uintptr) { cred, readErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED) }); err != nil {
		return false, err
	}
	if readErr != nil {
		return false, readErr
	}
	return cred != nil && cred.Uid == uint32(os.Geteuid()), nil
}
