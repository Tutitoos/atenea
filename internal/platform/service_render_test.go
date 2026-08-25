package platform

// Both managers' texts are rendered from the same Service, on any machine, so
// both are checked from any machine. The platform-tagged test files check what
// the local manager does with the file; these check the bytes themselves,
// which is where the two silent-failure modes below live: a unit that installs
// and enables cleanly and never starts, and a plist that stops being honored
// halfway through the flush it was configured to allow.

import (
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
