//go:build !linux

package platform

import (
	"runtime"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Atenea knows how to install itself as a background service on Linux and
// nowhere else yet, and says so rather than guessing.
//
// The next platform is macOS and the implementation is a launchd plist, which
// is deliberately not here. A plist written from the documentation and never
// run on a Mac would be worse than this error: it would install cleanly,
// report success, and then not start. This box exists so that whoever does
// have a Mac to test on has exactly one file to write, next to this one.
func unsupported(verb string) error {
	return contract.Fail(contract.FailureUnavailable,
		"atenea cannot %s a background service on %s; only linux is implemented, through systemd --user",
		verb, runtime.GOOS)
}

func (s Service) Install() error { return unsupported("install") }

func (s Service) Uninstall() error { return unsupported("uninstall") }

// Query reports nothing rather than an empty state that reads like an answer:
// "not installed" and "cannot tell" are different facts and only one of them
// is true here.
func Query(name string) (ServiceState, error) {
	return ServiceState{}, unsupported("look up")
}

// LingerCommand has no counterpart off systemd, so there is no advice to give.
func LingerCommand() string { return "" }
