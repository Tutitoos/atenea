//go:build linux

package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	systemctlBin = "systemctl"
	loginctlBin  = "loginctl"
)

func unitPath(name string) string {
	return filepath.Join(filepath.Dir(ConfigDir()), "systemd", "user", name+".service")
}

// manager runs one of the two systemd command-line tools.
//
// The three outcomes are kept apart because only one of them ends the
// conversation. out is standard output. refused is the tool's own complaint
// when it ran and exited non-zero -- "Unit atenea.service not loaded" is the
// common one, and it is an answer, not a fault: Uninstall has to ignore it and
// Query has to report it. err is set for one thing only, that this machine has
// no systemd Atenea can drive, which is the case a developer laptop deserves a
// sentence for rather than a stack trace.
//
// Sorting on what the tool printed is exactly what this file is for. The core
// never reads vendor text; the adapter that owns the vendor does.
func manager(bin string, args ...string) (out, refused string, err error) {
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	switch runErr := cmd.Run(); {
	case runErr == nil:
		return stdout.String(), "", nil
	case errors.As(runErr, new(*exec.ExitError)):
		return stdout.String(), complaint(stderr.String(), runErr), nil
	default:
		return "", "", contract.Fail(contract.FailureUnavailable,
			"%s cannot be run on this machine, so atenea cannot manage itself as a service: %v",
			bin, runErr).WithRaw(stderr.String())
	}
}

// complaint reduces what the tool said to the one line worth showing. systemd
// writes several when it is unhappy and the first names the cause; the rest is
// advice aimed at a human sitting at a terminal.
func complaint(stderr string, err error) string {
	first, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	if first = strings.TrimSpace(first); first != "" {
		return first
	}
	return err.Error()
}

// systemctl runs one verb against the user manager and insists it worked.
//
// --user is passed on every call rather than left to the environment, because
// the difference between the two managers is the difference between a service
// owned by this account and one owned by root, and that is not a thing to let
// an inherited variable decide.
func systemctl(args ...string) error {
	_, refused, err := manager(systemctlBin, append([]string{"--user"}, args...)...)
	if err != nil {
		return err
	}
	if refused != "" {
		return contract.Fail(contract.FailureUnavailable,
			"systemctl --user %s: %s", strings.Join(args, " "), refused)
	}
	return nil
}

// properties splits `systemctl show` output into its Key=Value lines.
//
// The Key=Value form is asked for on purpose. --value prints bare values in
// systemd's own property order rather than the order they were requested, so
// adding a sixth property to a call would silently re-label the other five.
func properties(out string) map[string]string {
	props := make(map[string]string, 8)
	for rest := out; rest != ""; {
		line, tail, _ := strings.Cut(rest, "\n")
		rest = tail
		if key, value, ok := strings.Cut(line, "="); ok {
			props[key] = strings.TrimSpace(value)
		}
	}
	return props
}

// Install writes the unit and hands it to the manager. It does not start
// anything: making the service permanent and making it run now are two
// different questions, and the operator asks the second one when they are
// ready to watch it.
func (s Service) Install() error {
	if err := writeUnit(s.Unit, s.UnitText()); err != nil {
		return err
	}
	// Reload before enable, every time. enable reads the unit through the
	// manager's cache, and on a re-install that cache still holds the version
	// this call just replaced.
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	return systemctl("enable", s.Name+".service")
}

// Uninstall takes the service off the machine, and is safe on a machine that
// never had it.
//
// stop and disable are allowed to refuse: both are no-ops once the unit is
// gone, and systemd reports that as an error. Failing on it would break the
// second uninstall, which is precisely the one somebody runs when the first
// left a mess. Removing the file and reloading are the two steps that must
// actually happen, so they are the two that are checked.
func (s Service) Uninstall() error {
	unit := s.Name + ".service"
	// Disable before removing the file: it unlinks the wants-symlinks by
	// reading the unit, so the other order leaves them dangling in
	// default.target.wants and systemd complaining at every reload.
	for _, verb := range [...]string{"stop", "disable"} {
		if _, _, err := manager(systemctlBin, "--user", verb, unit); err != nil {
			return err
		}
	}
	if err := os.Remove(s.Unit); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return diskFailure(s.Unit, err)
	}
	return systemctl("daemon-reload")
}

// Query asks the manager where the service stands.
//
// The Unit path comes back even when the query fails. Where the unit would go
// is knowable without asking anybody, and a status screen that cannot say even
// that is not worth printing.
func Query(name string) (ServiceState, error) {
	state := ServiceState{Unit: unitPath(name)}

	out, refused, err := manager(systemctlBin, "--user", "show", name+".service",
		"-p", "LoadState", "-p", "UnitFileState", "-p", "ActiveState",
		"-p", "SubState", "-p", "Description")
	if err != nil {
		return state, err
	}
	if refused != "" {
		// show answers for units that do not exist and still exits zero, so a
		// non-zero exit here is never about the unit. It is the user manager
		// not answering at all, which is the one case where nothing below can
		// be said honestly.
		return state, contract.Fail(contract.FailureUnavailable,
			"the systemd user manager is not answering: %s", refused)
	}

	props := properties(out)
	// Absence is a value, not an error: an absent unit loads as not-found.
	load := props["LoadState"]
	state.Installed = load != "" && load != "not-found"
	// enabled, and nothing that merely looks like it. enabled-runtime is a
	// symlink under /run, which is a tmpfs: it reads as enabled right up to
	// the reboot it does not survive, and surviving the reboot is the entire
	// question this field answers.
	state.Enabled = props["UnitFileState"] == "enabled"
	state.Active = props["ActiveState"] == "active"
	state.Linger = lingering()

	if !state.Installed {
		state.Detail = "no unit file at " + state.Unit
		return state, nil
	}
	state.Detail = fmt.Sprintf("%s: %s (%s)",
		props["Description"], props["ActiveState"], props["SubState"])
	return state, nil
}

// lingering reports whether this user's services run without the user being
// logged in. Without it an enabled unit waits for a login that an unattended
// machine never performs.
//
// A loginctl that cannot answer counts as off. That is the safe direction: the
// cost is one line of advice the operator did not need, where the opposite
// mistake is a service that silently never starts.
func lingering() bool {
	out, refused, err := manager(loginctlBin, "show-user", currentUser(), "-p", "Linger")
	if err != nil || refused != "" {
		return false
	}
	return properties(out)["Linger"] == "yes"
}

// LingerCommand is the one step Atenea cannot take for the operator: enabling
// lingering needs a privilege the service does not have.
//
// It is a string built here rather than in the command that prints it, because
// this package is the only corner allowed to know how Atenea starts in the
// background, and a systemd command spelled out in the CLI would be a second
// copy of that knowledge sitting where it cannot be compiled per platform.
func LingerCommand() string {
	return "loginctl enable-linger " + currentUser()
}

// currentUser names the account whose manager owns the service. loginctl wants
// it spelled out; an empty answer is left to loginctl to reject, since a
// machine that cannot name its own user has a bigger problem than lingering.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// writeUnit puts text at path through a temporary file in the same directory
// and renames it into place.
//
// Same reason the run dumps do it: a write that dies halfway -- full disk, a
// signal -- leaves a truncated file behind, and a truncated unit is worse than
// no unit. systemd refuses to load it, so the service that worked five minutes
// ago becomes a parse error at the next reload, with no previous version left
// to fall back on.
func writeUnit(path, text string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return diskFailure(dir, err)
	}
	temp, err := os.CreateTemp(dir, "atenea.*.tmp")
	if err != nil {
		return diskFailure(dir, err)
	}
	name := temp.Name()
	// The temporary file leaves here in exactly one shape: renamed into place.
	// Every other exit takes it along.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()

	if _, err := temp.WriteString(text); err != nil {
		_ = temp.Close()
		return diskFailure(name, err)
	}
	// CreateTemp makes it 0600; a unit file holds no secret and being the one
	// unreadable file in that directory only invites somebody to "fix" it.
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return diskFailure(name, err)
	}
	if err := temp.Close(); err != nil {
		return diskFailure(name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return diskFailure(path, err)
	}
	renamed = true
	return nil
}

// diskFailure sorts a filesystem error. Under the user's own config root a
// refusal is almost always permissions, and that wants a different exit code
// from a disk that is simply gone.
func diskFailure(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return contract.Fail(contract.FailurePermissionDenied, "unit file %s: %v", path, err)
	}
	return contract.Fail(contract.FailureUnavailable, "unit file %s: %v", path, err)
}
