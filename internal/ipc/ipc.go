//go:build linux || darwin || freebsd

// Package ipc is the door a client on this machine knocks on.
//
// One unix socket, no port and no token. The kernel is the authenticator: a
// socket has an owner and a mode, and every connection carries the uid of
// whoever opened it, checked on arrival. A token would have to be stored
// somewhere a client could read, which on a single-user machine means storing
// it exactly where the thing it protects already is.
package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// dirMode keeps the directory private, and it is not belt and braces.
//
// A socket is created with the process umask, so between bind and chmod it can
// be briefly readable by others. Nothing can reach it through a directory it
// cannot traverse, so the directory is what closes that window. The socket mode
// below narrows it again for anyone who arrives later.
//
// Set with an explicit chmod, never left to MkdirAll, which does nothing at all
// to a directory that already exists and applies the umask when it creates one.
// Both of those were true here: the first shipped version asked for 0700 and
// got 0755 from a state root some earlier command had already made, and no test
// noticed because every test created the directory through this function.
const (
	dirMode    os.FileMode = 0o700
	socketMode os.FileMode = 0o600
)

// probeTimeout bounds the "is somebody already there?" check. It is a connect
// to a local socket: it either answers immediately or nothing is listening.
const probeTimeout = 250 * time.Millisecond

// maxPath is the smallest sun_path across the platforms this builds for --
// 104 on the BSDs, 108 on Linux -- minus room for the terminator. Checking
// against the smaller one means the same path works everywhere.
const maxPath = 103

// ErrInUse reports a socket that already has a live owner.
var ErrInUse = errors.New("another process is already listening")

// Endpoint returns the platform-neutral service endpoint for Unix systems.
func Endpoint(stateRoot string) string {
	return filepath.Join(stateRoot, "run", "core.sock")
}

// Dial connects to the service endpoint with a short local timeout.
func Dial(path string) (net.Conn, error) {
	return DialTimeout(path, 2*time.Second)
}

// DialTimeout connects to the service endpoint.
func DialTimeout(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

// Listener is a bound socket that only this user can reach.
type Listener struct {
	inner   *net.UnixListener
	path    string
	refused atomic.Int64
	once    sync.Once
	closefn error
}

// Listen binds the socket at path, replacing a dead owner's leftover.
//
// A socket file outliving its process is the ordinary case, not the exception:
// the file is not the socket, it is a name for one, and nothing removes it when
// the process holding it is killed outright. So the leftover is probed before
// it is trusted -- if something answers, this is a live owner and binding would
// steal a name in use; if nothing does, the name is dead and gets removed.
// Refusing on the mere presence of the file would mean one SIGKILL locks the
// machine out until somebody deletes it by hand.
func Listen(path string) (*Listener, error) {
	// A socket path is not a file path: the kernel keeps it in a fixed-size
	// field, and one byte over the edge fails as "invalid argument" with
	// nothing to say which argument or why. The state root comes from
	// XDG_STATE_HOME, so this is reachable by configuration rather than only
	// by accident.
	if len(path) > maxPath {
		return nil, fmt.Errorf("socket path is %d bytes and the kernel allows %d: %s",
			len(path), maxPath, path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("preparing the socket directory: %w", err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return nil, fmt.Errorf("closing the socket directory to other users: %w", err)
	}
	if err := clearStale(path); err != nil {
		return nil, err
	}
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", path, err)
	}
	inner, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("binding %s: %w", path, err)
	}
	if err := os.Chmod(path, socketMode); err != nil {
		_ = inner.Close()
		return nil, fmt.Errorf("closing the socket to other users: %w", err)
	}
	return &Listener{inner: inner, path: path}, nil
}

// clearStale removes a leftover socket file, and only a leftover.
func clearStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("looking at %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		// Not a socket at all. Removing whatever this is would be destroying
		// a file somebody meant to keep.
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, probeTimeout)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("%s: %w", path, ErrInUse)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing a dead socket at %s: %w", path, err)
	}
	return nil
}

// Accept returns the next connection from this user, dropping the rest.
//
// Dropped without an answer on purpose. A refusal that explains itself tells a
// caller who is not entitled to be here what is here, and the only caller who
// could see it is one that already failed the only check that matters.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.inner.AcceptUnix()
		if err != nil {
			return nil, err
		}
		allowed, err := sameUser(conn)
		if err != nil {
			// The kernel would not say who this is, so it does not get in.
			_ = conn.Close()
			l.refused.Add(1)
			continue
		}
		if !allowed {
			_ = conn.Close()
			l.refused.Add(1)
			continue
		}
		return conn, nil
	}
}

// Refused counts connections turned away for being somebody else's.
//
// Worth a number rather than a log line: on a single-user machine this should
// be zero forever, so any other value is the interesting kind of surprise.
func (l *Listener) Refused() int64 { return l.refused.Load() }

// Path is where the socket is bound.
func (l *Listener) Path() string { return l.path }

// Close stops listening, which takes the name with it.
//
// The unlink is Go's: a UnixListener removes the socket file it created. This
// used to remove it a second time, and a mutation proved that line could not
// fail a test -- because it could not do anything. What matters is tested
// instead: after a close, the path is gone, so the next start finds nothing to
// probe.
//
// Closing twice is not an error, because it is what ordinary code does: an
// explicit stop on the way out and a deferred one behind it. Reporting "use of
// closed network connection" to the second caller would be reporting success
// as a fault.
func (l *Listener) Close() error {
	l.once.Do(func() { l.closefn = l.inner.Close() })
	return l.closefn
}
