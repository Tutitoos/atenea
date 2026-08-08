//go:build !linux

package ipc

import "net"

// sameUser has no kernel answer to ask for here, so the file mode is the whole
// guard: a socket at 0600 inside a directory at 0700 is reachable by this user
// and by root, and by nobody else.
//
// That is weaker than Linux in one specific way. The mode is checked when the
// socket is created and never again, so a mode changed afterwards -- by hand,
// or by a tool that widens a directory -- goes unnoticed here, where a peer
// check would still turn the stranger away at the door. Said plainly rather
// than papered over: this returns that it allowed the connection, and it is
// the directory that did the work.
func sameUser(*net.UnixConn) (bool, error) { return true, nil }
