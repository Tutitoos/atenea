package platform

// Both managers' texts are rendered from the same Service, on any machine, so
// both are checked from any machine. The platform-tagged test files check what
// the local manager does with the file; these check the bytes themselves,
// which is where the two silent-failure modes below live: a unit that installs
// and enables cleanly and never starts, and a plist that stops being honored
// halfway through the flush it was configured to allow.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// systemd splits ExecStart on whitespace and expands `%` specifiers, so an
// unquoted path containing either starts a binary nobody named -- /opt/My
// with the argument Apps/atenea, or a path with the user's home spliced into
// the middle of it. Both install and enable without a word of complaint.
func TestTheUnitQuotesTheBinaryPathSystemdWouldOtherwiseSplitOrExpand(t *testing.T) {
	for _, tc := range []struct {
		name string
		exec string
		want string
	}{
		{"a path with a space", "/opt/My Apps/atenea", `ExecStart="/opt/My Apps/atenea" run`},
		{"a path with a systemd specifier", "/opt/100%/atenea", `ExecStart="/opt/100%%/atenea" run`},
		{"a path with a quote", `/opt/a"b/atenea`, `ExecStart="/opt/a\"b/atenea" run`},
		{"a path with a backslash", `/opt/a\b/atenea`, `ExecStart="/opt/a\\b/atenea" run`},
		{"an ordinary path", "/opt/atenea/bin/atenea", `ExecStart="/opt/atenea/bin/atenea" run`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewService(tc.exec, 10*time.Second)
			if err != nil {
				t.Fatalf("NewService(%q): %v", tc.exec, err)
			}
			if got := systemdText(s); !strings.Contains(got, tc.want) {
				t.Errorf("the unit carries no %q:\n%s", tc.want, got)
			}
		})
	}
}

// A newline in the path is the one case quoting cannot carry: it ends the
// ExecStart directive and turns the tail of the path into another directive of
// the [Service] section. Refused where somebody is still watching, rather than
// at the next boot.
func TestAServiceWhoseBinaryPathCarriesAControlCharacterIsRefused(t *testing.T) {
	for _, exec := range []string{"/opt/atenea\nExecStartPre=/bin/false", "/opt/atenea\tbin", "/opt/atenea\x00"} {
		if _, err := NewService(exec, time.Second); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("NewService(%q) was accepted, or refused as %v", exec, contract.KindOf(err))
		}
	}
}

// launchd kills at its own default of 20 seconds when the plist says nothing,
// so a grace longer than that was honored on Linux and cut short on macOS --
// SIGKILL in the middle of the very flush the grace exists to allow, and a
// stop that looks clean from the outside either way.
func TestThePlistWaitsAsLongForTheStopAsTheUnitDoes(t *testing.T) {
	for _, grace := range []time.Duration{
		0,
		time.Second,
		10 * time.Second,
		10500 * time.Millisecond,
		2 * time.Minute,
	} {
		s, err := NewService("/opt/atenea", grace)
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		exitTimeout := plistInteger(t, launchdText(s), "ExitTimeOut")
		if want := stopTimeout(t, systemdText(s)); exitTimeout != want {
			t.Errorf("grace %v gets ExitTimeOut %v on macOS and TimeoutStopSec %v on Linux", grace, exitTimeout, want)
		}
		if exitTimeout <= grace {
			t.Errorf("grace %v gets ExitTimeOut %v, which kills inside the grace", grace, exitTimeout)
		}
	}
}

// plistInteger reads back one <integer> value the plist sets for key.
func plistInteger(t *testing.T, text, key string) time.Duration {
	t.Helper()
	_, after, ok := strings.Cut(text, "<key>"+key+"</key><integer>")
	if !ok {
		t.Fatalf("the plist carries no %s:\n%s", key, text)
	}
	raw, _, _ := strings.Cut(after, "</integer>")
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s is not a number launchd can read: %q", key, raw)
	}
	return time.Duration(seconds) * time.Second
}

// stopTimeout reads TimeoutStopSec back out of a rendered unit.
func stopTimeout(t *testing.T, text string) time.Duration {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && name == "TimeoutStopSec" {
			seconds, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("TimeoutStopSec is not a number systemd can read: %q", value)
			}
			return time.Duration(seconds) * time.Second
		}
	}
	t.Fatalf("the unit carries no TimeoutStopSec:\n%s", text)
	return 0
}

// A user unit with no WorkingDirectory starts in $HOME.
//
// That mattered because the shipped settings declare `path = "."` for the
// `current` repository -- which is how a fresh install works against whatever
// tree you are standing in when you run the CLI, and is exactly wrong for a
// daemon standing nowhere. Together they meant a `code.search` from a chat
// raked the home directory: Documents, mail, .ssh, .aws. Pointing the service
// at the state root does not make `path = "."` correct for a daemon, but wrong
// and empty is a different thing from wrong and private.
func TestBothManagersStartTheServiceInADirectoryAteneaOwns(t *testing.T) {
	svc, err := NewService("/usr/local/bin/atenea", 10*time.Second)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	state := StateDir()

	unit := systemdText(svc)
	if !strings.Contains(unit, "WorkingDirectory=") {
		t.Error("the systemd unit declares no WorkingDirectory: it starts in $HOME")
	}
	if !strings.Contains(unit, state) {
		t.Errorf("the systemd unit does not point at the state root %q:\n%s", state, unit)
	}

	plist := launchdText(svc)
	if !strings.Contains(plist, "<key>WorkingDirectory</key>") {
		t.Error("the launchd agent declares no WorkingDirectory: it starts in $HOME")
	}
	if !strings.Contains(plist, state) {
		t.Errorf("the launchd agent does not point at the state root %q:\n%s", state, plist)
	}
}

// The working directory is user input the same way the binary path is, so it
// crosses the same escaping. A state root under a home directory with a space
// in it would otherwise become two directives systemd cannot read, and a `%h`
// in the path would expand to the home directory the value exists to avoid.
func TestTheWorkingDirectoryIsEscapedLikeTheBinaryPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", `/home/some one/%h state`)
	svc, err := NewService("/usr/local/bin/atenea", time.Second)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if unit := systemdText(svc); !strings.Contains(unit,
		`WorkingDirectory="/home/some one/%%h state/atenea"`) {
		t.Errorf("the working directory is not quoted and %%-escaped:\n%s", unit)
	}
}

// %s is the state root, which is where the service is pointed so that a
// relative repository path resolves somewhere Atenea owns instead of in $HOME.
// It is the one directive whose value is a property of the machine rather than
// of this file, so the test pins it with an environment variable rather than
// letting the golden lie about a path that moves.
const wantUnit = `[Unit]
Description=Atenea orchestration core
After=default.target

[Service]
Type=simple
ExecStart="/opt/atenea/bin/atenea" run
WorkingDirectory="%s"
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
`

// The unit a machine boots from, byte for byte.
//
// It lives here rather than in the linux-tagged file it was written in, and
// that move is the whole point: a mac never compiled that file, so the
// WorkingDirectory line added to unitTemplate broke this golden and the only
// machine that could notice was a CI runner. Both texts are rendered from the
// same Service on any machine -- that is what this file is for -- so both
// goldens belong where both can be checked.
func TestTheUnitFileIsRenderedExactlyAsItWillBeInstalled(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/var/lib/somebody")
	want := fmt.Sprintf(wantUnit, "/var/lib/somebody/atenea")

	svc, err := NewService("/opt/atenea/bin/atenea", 10*time.Second)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if got := systemdText(svc); got != want {
		t.Errorf("unit text drifted.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
