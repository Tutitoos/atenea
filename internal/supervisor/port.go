package supervisor

import (
	"fmt"
	"net"
	"strconv"
)

// portPlaceholder is the literal token a spec's Args may carry in place of
// the port. It is filled in once the port is chosen, whether that came from
// the settings file or from the OS.
const portPlaceholder = "{{port}}"

// choosePort returns the port a server listens on. A fixed port is returned
// as-is; zero asks the OS for a free one.
//
// The listener is opened and closed rather than merely asked about: on this
// machine, right now, is the only claim worth making, and closing it before
// the real server binds is the standard trade for that -- a race against
// something else grabbing the same port in between, accepted because the
// alternative is a supervisor that also has to speak the child's protocol
// just to ask it which port it picked.
func choosePort(host string, fixed int) (int, error) {
	if fixed != 0 {
		return fixed, nil
	}
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("choosing a port: %w", err)
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("choosing a port: unexpected listener address %v", l.Addr())
	}
	return addr.Port, nil
}

// withPort substitutes portPlaceholder for the chosen port in a copy of args.
// Args that do not carry the placeholder are returned unchanged: a fixed
// port a user typed directly into the command line needs no substitution at
// all.
func withPort(args []string, port int) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	portText := strconv.Itoa(port)
	for i, arg := range args {
		if arg == portPlaceholder {
			out[i] = portText
			continue
		}
		out[i] = arg
	}
	return out
}
