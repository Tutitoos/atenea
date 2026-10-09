//go:build darwin || linux

// Package localipc owns the Atenea SSH desktop/controller endpoint. Native
// credential checks are separate from the version handshake: values sent by a
// peer do not establish its OS identity.
package localipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/pidlock"
)

const socketName = "atenea-ssh.sock"

// The shared Unix listener uses the shortest supported sun_path limit.
const maxSocketPath = 103

var (
	// ErrPrivateRoot means the endpoint's state root is unsafe for a user-owned socket.
	ErrPrivateRoot = errors.New("ssh local ipc: private state root required")
	// ErrPeer means the connected process is not the expected OS user.
	ErrPeer = errors.New("ssh local ipc: peer identity mismatch")
)

// Listener accepts only same-UID peers. A separate controller must own this
// listener and its state for the current logged-in account.
type Listener struct {
	inner   *ipc.Listener
	address *net.UnixAddr
	release func()
	once    sync.Once
}

// Listen binds a private socket below root. The caller chooses a stable,
// absolute, user-owned state root; it must not contain an untrusted symlink.
func Listen(root string) (*Listener, error) {
	if err := ensureRoot(root); err != nil {
		return nil, err
	}
	if err := ValidateEndpoint(root); err != nil {
		return nil, err
	}
	run := filepath.Join(root, "run")
	if err := ensureRun(run); err != nil {
		return nil, err
	}
	lock := filepath.Join(run, "atenea-ssh.lock")
	if err := privateLock(lock); err != nil {
		return nil, err
	}
	path := filepath.Join(run, socketName)
	release, err := pidlock.Claim(lock)
	if err != nil {
		return nil, err
	}
	inner, err := ipc.Listen(path)
	if err != nil {
		release()
		return nil, err
	}
	return &Listener{inner: inner, address: &net.UnixAddr{Name: path, Net: "unix"}, release: release}, nil
}

// Accept returns a connection whose peer UID was verified by the kernel.
func (l *Listener) Accept() (net.Conn, error) { return l.inner.Accept() }

// Close stops listening and removes the endpoint.
func (l *Listener) Close() error {
	err := l.inner.Close()
	l.once.Do(l.release)
	return err
}

// Addr identifies this user's socket.
func (l *Listener) Addr() net.Addr { return l.address }

// Dial verifies the private path, socket owner and server peer UID. It never
// trusts an alias, path string or self-reported handshake identity as a login.
func Dial(root string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		return nil, ErrPeer
	}
	if err := ValidateEndpoint(root); err != nil {
		return nil, err
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	run := filepath.Join(root, "run")
	if err := privateDir(run); err != nil {
		return nil, err
	}
	path := filepath.Join(run, socketName)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 || !ownedByUs(info) {
		return nil, ErrPeer
	}
	conn, err := ipc.DialTimeout(path, timeout)
	if err != nil {
		return nil, err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, ErrPeer
	}
	same, err := samePeer(unixConn)
	if err != nil || !same {
		_ = conn.Close()
		return nil, ErrPeer
	}
	// Refuse a socket path swapped during the connection. The peer credential is
	// authoritative, and this second check preserves the private endpoint route.
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) {
		_ = conn.Close()
		return nil, ErrPeer
	}
	return conn, nil
}

func ensureRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return ErrPrivateRoot
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByUs(info) {
		return ErrPrivateRoot
	}
	// The dedicated root may have been created under a permissive umask. Tighten
	// it before the socket is bound; never chmod a symlink or another owner.
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	return privateDir(root)
}

func privateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ownedByUs(info) {
		return ErrPrivateRoot
	}
	return nil
}

func ensureRun(run string) error {
	if err := os.Mkdir(run, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// Lstat rejects a pre-existing symlink before pidlock or ipc can follow it.
	return privateDir(run)
}

func privateLock(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || !ownedByUs(info) || info.Mode().Perm()&0o077 != 0 || st.Nlink != 1 {
		return ErrPrivateRoot
	}
	return nil
}

func ownedByUs(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

// Endpoint returns the socket path for diagnostics and tests, not for access
// control. Dial still rechecks filesystem and kernel peer identity.
func Endpoint(root string) string { return filepath.Join(root, "run", socketName) }

// ValidateEndpoint rejects a path that the kernel cannot bind as a socket.
func ValidateEndpoint(root string) error {
	if len(Endpoint(root)) > maxSocketPath {
		return ErrEndpointTooLong
	}
	return nil
}

// Refused reports connections rejected by the native server peer check.
func (l *Listener) Refused() int64 { return l.inner.Refused() }

var _ net.Listener = (*Listener)(nil)

// CheckRoot reports configuration errors without creating an endpoint.
func CheckRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return ErrPrivateRoot
	}
	if err := privateDir(root); err != nil {
		return fmt.Errorf("ssh local ipc: %w", err)
	}
	return nil
}
