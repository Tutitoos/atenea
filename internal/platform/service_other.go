//go:build !linux && !darwin && !windows

package platform

import (
	"path/filepath"
	"runtime"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func unitPath(name string) string {
	return filepath.Join(filepath.Dir(ConfigDir()), "services", name+".service")
}

// Atenea knows how to install itself as a background service on Linux and
// macOS. Other systems say so rather than guessing.
func unsupported(verb string) error {
	return contract.Fail(contract.FailureUnavailable,
		"atenea cannot %s a background service on %s; supported managers are Linux systemd --user and macOS launchd",
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

// LingerCommand has no counterpart on unsupported systems, so there is no
// advice to give.
func LingerCommand() string { return "" }
