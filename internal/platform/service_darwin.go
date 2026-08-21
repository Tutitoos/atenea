//go:build darwin

package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const launchdLabel = "com.tutitoos.atenea"

func unitPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, "Library", "LaunchAgents", name+".plist")
}

func launchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)
}

func launchctl(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), contract.Fail(contract.FailureUnavailable,
			"launchctl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return "", contract.Fail(contract.FailureUnavailable,
		"launchctl cannot be run on this machine: %v", err)
}

func (s Service) Install() error {
	if err := writePlist(s.Unit, s.UnitText()); err != nil {
		return err
	}
	// bootstrap loads this plist and RunAtLoad starts it for the current login.
	// An older copy may already be loaded, so boot it out before replacing it.
	_, _ = launchctl("bootout", launchdTarget())
	if _, err := launchctl("bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), s.Unit); err != nil {
		return err
	}
	_, err := launchctl("enable", launchdTarget())
	return err
}

func (s Service) Uninstall() error {
	if _, err := launchctl("bootout", launchdTarget()); err != nil {
		// A missing job is the safe, idempotent uninstall case.
		if _, statErr := os.Stat(s.Unit); statErr != nil && errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
	}
	if err := os.Remove(s.Unit); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", s.Unit, err)
	}
	return nil
}

func Query(name string) (ServiceState, error) {
	state := ServiceState{Unit: unitPath(name)}
	if _, err := os.Stat(state.Unit); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			state.Detail = "no plist file at " + state.Unit
			return state, nil
		}
		return state, contract.Fail(contract.FailureUnavailable, "plist file %s: %v", state.Unit, err)
	}
	state.Installed = true
	out, err := launchctl("print", launchdTarget())
	if err != nil {
		state.Detail = "installed but not loaded: " + contract.MessageOf(err)
		return state, nil
	}
	state.Enabled = true
	state.Active = strings.Contains(out, "state = running") || strings.Contains(out, "pid = ")
	state.Detail = fmt.Sprintf("%s: loaded (%s)", launchdLabel, map[bool]string{true: "active", false: "inactive"}[state.Active])
	return state, nil
}

func LingerCommand() string { return "" }

func writePlist(path, text string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return contract.Fail(contract.FailureUnavailable, "plist directory %s: %v", dir, err)
	}
	temp, err := os.CreateTemp(dir, "atenea.*.tmp")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", path, err)
	}
	tmp := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := temp.WriteString(text); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", path, err)
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", path, err)
	}
	if err := temp.Close(); err != nil {
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", path, err)
	}
	keep = true
	return nil
}
