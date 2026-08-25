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

// Install writes the launchd agent and loads it. See Service.UnitText for what
// lands on disk; the plist is written before the load so a failed load leaves
// something an operator can read rather than nothing.
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

// Uninstall boots the agent out and removes its plist. A bootout that fails is
// reported rather than swallowed: removing the file under a still-loaded agent
// leaves launchd holding a definition nobody can see.
func (s Service) Uninstall() error {
	if out, err := launchctl("bootout", launchdTarget()); err != nil {
		// A missing job is the safe, idempotent uninstall case.
		if _, statErr := os.Stat(s.Unit); statErr != nil && errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		if !bootoutFoundNothing(out) {
			// The plist is still there and launchctl refused for some reason
			// other than the job not being loaded, so the agent is very
			// probably still running. Deleting the file now would leave a
			// live job with no plist behind it -- surviving until the next
			// logout, invisible to Query, and impossible to boot out again by
			// path -- while Uninstall reported success. The operator needs
			// launchctl's own words, which is what this returns.
			return err
		}
	}
	if err := os.Remove(s.Unit); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return contract.Fail(contract.FailureUnavailable, "plist file %s: %v", s.Unit, err)
	}
	return nil
}

// bootoutFoundNothing reports whether launchctl refused because there was no
// such job loaded, which is the one refusal an uninstall may ignore: nothing
// is running, so removing the plist finishes the job.
//
// It matches on launchctl's text because that is all launchctl offers. Its
// exit status is the same 3 for "no such process" as for several failures
// that do leave the job running, and it has spelled this case both ways --
// "Could not find specified service" on Big Sur and later, "No such process"
// before it -- so both are listed rather than the newest one only.
func bootoutFoundNothing(out string) bool {
	lowered := strings.ToLower(out)
	return strings.Contains(lowered, "could not find") ||
		strings.Contains(lowered, "no such process") ||
		strings.Contains(lowered, "not find service")
}

// Query asks launchd where the agent stands. It is the darwin half of the
// answer `atenea service status` prints, and it reports absence as a state
// rather than as an error: a service that was never installed is a fact about
// this machine, not a failure to look.
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

// LingerCommand is empty on macOS. A launchd agent with RunAtLoad starts at
// login without anything equivalent to systemd's linger, so there is no second
// command to tell an operator about -- and inventing one would send them
// looking for a setting that does not exist here.
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
