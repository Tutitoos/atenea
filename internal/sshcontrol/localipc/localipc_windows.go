//go:build windows

// Package localipc owns the Atenea SSH desktop/controller endpoint. Windows
// pipe ACLs and peer process tokens are checked before protocol handshakes.
package localipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"time"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

var (
	// ErrPrivateRoot means an invalid installation scope was passed.
	ErrPrivateRoot = errors.New("ssh local ipc: absolute installation root required")
	// ErrPeer means the connected process is not this Windows account and session.
	ErrPeer = errors.New("ssh local ipc: peer identity mismatch")
)

// Listener accepts named-pipe clients after a kernel process/token check.
type Listener struct {
	inner    net.Listener
	identity peerIdentity
	refused  atomic.Int64
}

type peerIdentity struct {
	sid     string
	session uint32
}

// Listen creates one instance per installation/account/logon session. The pipe
// DACL allows only the current user's SID; Accept independently verifies the
// client process token and session. A GUI cannot run in session zero by default.
func Listen(root string) (*Listener, error) {
	path, id, err := pipePath(root)
	if err != nil {
		return nil, err
	}
	config := &winio.PipeConfig{SecurityDescriptor: fmt.Sprintf("D:P(A;;GA;;;%s)", id.sid), InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10}
	inner, err := winio.ListenPipe(path, config)
	if err != nil {
		return nil, err
	}
	return &Listener{inner: inner, identity: id}, nil
}

// Accept discards connections whose pipe peer cannot be independently mapped
// to the current account and graphical logon session.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		if verifyPeer(conn, l.identity, false) == nil {
			return conn, nil
		}
		_ = conn.Close()
		l.refused.Add(1)
	}
}

// Close stops accepting and releases the pipe name.
func (l *Listener) Close() error { return l.inner.Close() }

// Addr identifies the named pipe.
func (l *Listener) Addr() net.Addr { return l.inner.Addr() }

// Refused reports connections rejected after the DACL check.
func (l *Listener) Refused() int64 { return l.refused.Load() }

// Dial verifies both the pipe's private name and the kernel-reported server
// process token/session. The process cannot authenticate itself with a hello.
func Dial(root string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		return nil, ErrPeer
	}
	path, id, err := pipePath(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := verifyPeer(conn, id, true); err != nil {
		_ = conn.Close()
		return nil, ErrPeer
	}
	return conn, nil
}

// Endpoint returns a name for diagnostics, never authority to connect.
func Endpoint(root string) string {
	path, _, err := pipePath(root)
	if err != nil {
		return ""
	}
	return path
}

// Windows named-pipe names have no Unix sun_path limit.
func ValidateEndpoint(string) error { return nil }

// CheckRoot validates the installation scope without opening a pipe.
func CheckRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return ErrPrivateRoot
	}
	return nil
}

func pipePath(root string) (string, peerIdentity, error) {
	if err := CheckRoot(root); err != nil {
		return "", peerIdentity{}, err
	}
	physicalID, err := rootIdentity(root)
	if err != nil {
		return "", peerIdentity{}, err
	}
	id, err := currentIdentity()
	if err != nil {
		return "", peerIdentity{}, err
	}
	// Directory file identity is stable across case, short-path and junction
	// aliases. Hashing the path text would let two controllers own one store.
	digest := sha256.Sum256([]byte(physicalID + ":" + id.sid))
	name := `\\.\pipe\atenea-ssh-` + hex.EncodeToString(digest[:12]) + fmt.Sprintf("-%d", id.session)
	return name, id, nil
}

func rootIdentity(root string) (string, error) {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPrivateRoot, err)
	}
	handle, err := windows.CreateFile(path, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPrivateRoot, err)
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", fmt.Errorf("%w: %v", ErrPrivateRoot, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		(info.VolumeSerialNumber == 0 && info.FileIndexHigh == 0 && info.FileIndexLow == 0) {
		return "", ErrPrivateRoot
	}
	return fmt.Sprintf("%08x-%08x-%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func currentIdentity() (peerIdentity, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return peerIdentity{}, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return peerIdentity{}, err
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return peerIdentity{}, err
	}
	if session == 0 {
		return peerIdentity{}, ErrPeer
	}
	return peerIdentity{sid: user.User.Sid.String(), session: session}, nil
}

func verifyPeer(conn net.Conn, want peerIdentity, server bool) error {
	withHandle, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return ErrPeer
	}
	handle := windows.Handle(withHandle.Fd())
	var pid uint32
	var err error
	if server {
		err = windows.GetNamedPipeServerProcessId(handle, &pid)
	} else {
		err = windows.GetNamedPipeClientProcessId(handle, &pid)
	}
	if err != nil || pid == 0 {
		return ErrPeer
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ErrPeer
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return ErrPeer
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || user.User.Sid.String() != want.sid {
		return ErrPeer
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(pid, &session); err != nil || session != want.session {
		return ErrPeer
	}
	var again uint32
	if server {
		err = windows.GetNamedPipeServerProcessId(handle, &again)
	} else {
		err = windows.GetNamedPipeClientProcessId(handle, &again)
	}
	if err != nil || again != pid {
		return ErrPeer
	}
	return nil
}

var _ net.Listener = (*Listener)(nil)
