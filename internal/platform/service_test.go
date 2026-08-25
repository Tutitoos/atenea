//go:build linux

package platform_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The unit a machine boots from, byte for byte. It is pinned rather than
// sampled because an operator diffs what is installed against what this
// version would install, and a field that drifts by accident is a field
// nobody notices until the boot that needed it.
const wantUnit = `[Unit]
Description=Atenea orchestration core
After=default.target

[Service]
Type=simple
ExecStart="/opt/atenea/bin/atenea" run
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
`

func service(t *testing.T, exec string, grace time.Duration) platform.Service {
	t.Helper()
	s, err := platform.NewService(exec, grace)
	if err != nil {
		t.Fatalf("NewService(%q, %v): %v", exec, grace, err)
	}
	return s
}

// unitValue reads one directive back out of a rendered unit.
func unitValue(t *testing.T, text, key string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && name == key {
			return value
		}
	}
	t.Fatalf("the unit carries no %s:\n%s", key, text)
	return ""
}

func TestTheUnitFileIsRenderedExactlyAsItWillBeInstalled(t *testing.T) {
	got := service(t, "/opt/atenea/bin/atenea", 10*time.Second).UnitText()
	if got != wantUnit {
		t.Errorf("unit text drifted.\n--- got ---\n%s\n--- want ---\n%s", got, wantUnit)
	}
}

// The manager starts a binary, not a shell: whatever is written here is what
// runs, and `run` is the only verb that is a lifecycle rather than a report.
// The path is quoted because systemd splits the value on whitespace and
// expands `%` specifiers in it -- see systemdExec.
func TestTheUnitStartsTheGivenBinaryInRunMode(t *testing.T) {
	const binary = "/usr/local/lib/atenea/atenea"
	got := unitValue(t, service(t, binary, 10*time.Second).UnitText(), "ExecStart")

	if want := `"` + binary + `" run`; got != want {
		t.Errorf("ExecStart = %q, want %q", got, want)
	}
}

// The whole point of the unit is that nobody has to remember to start Atenea.
// Without this line it installs, enables cleanly, and never comes up.
func TestTheUnitIsWantedByTheDefaultTarget(t *testing.T) {
	if got := unitValue(t, service(t, "/opt/atenea", time.Second).UnitText(), "WantedBy"); got != "default.target" {
		t.Errorf("WantedBy = %q, want default.target", got)
	}
}

// The stop timeout has to outlast the shutdown grace, always. `run` spends
// that grace letting in-flight work finish and writing the measurement batch;
// a timeout inside it means SIGKILL lands in the middle of the flush the grace
// exists to allow, and systemd still calls the stop clean.
func TestTheStopTimeoutOutlivesTheShutdownGrace(t *testing.T) {
	for _, grace := range []time.Duration{
		0,
		time.Second,
		10 * time.Second,
		10500 * time.Millisecond,
		2 * time.Minute,
	} {
		text := service(t, "/opt/atenea", grace).UnitText()
		seconds, err := strconv.Atoi(unitValue(t, text, "TimeoutStopSec"))
		if err != nil {
			t.Fatalf("TimeoutStopSec is not a number systemd can read: %v", err)
		}
		if timeout := time.Duration(seconds) * time.Second; timeout <= grace {
			t.Errorf("grace %v gets TimeoutStopSec %v, which kills inside the grace", grace, timeout)
		}
	}
}

// The trap this guards. "Local and not exposed" is the network posture, and
// the obvious way to spell it in a unit is to cut the network off. Atenea
// listens on nothing -- it is not exposed by construction -- but it dials out
// to an MCP proxy on loopback and to a paid model on the internet. Either of
// these directives silently takes both providers down.
func TestTheUnitNeverBlocksTheNetworkItDependsOn(t *testing.T) {
	text := service(t, "/opt/atenea", 10*time.Second).UnitText()
	for _, forbidden := range []string{"IPAddressDeny", "PrivateNetwork"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the unit carries %s, which cuts off the providers atenea dials:\n%s", forbidden, text)
		}
	}
}

// A service manager starts a binary from no particular directory and with no
// PATH worth trusting, so a relative ExecStart does not fail when it is
// written. It fails at the next boot, months later, in a log nobody reads.
func TestAServiceWithoutAnAbsoluteBinaryIsRefused(t *testing.T) {
	for _, exec := range []string{"", "   ", "atenea", "./atenea", "bin/atenea", "../atenea"} {
		if _, err := platform.NewService(exec, 10*time.Second); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("NewService(%q) was accepted, or refused as %v", exec, contract.KindOf(err))
		}
	}
}

// A user unit, never a system one. Atenea drives CLIs already logged in as
// this user and writes under this user's XDG roots; a unit under /etc would
// start a process owning neither. The path following XDG_CONFIG_HOME is also
// what keeps every test off the real home.
func TestTheUnitGoesUnderTheUsersOwnConfigRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	got := service(t, "/opt/atenea", 10*time.Second).Unit
	want := filepath.Join(root, "systemd", "user", "atenea.service")
	if got != want {
		t.Errorf("unit at %q, want %q", got, want)
	}
}

// Nothing installed is an answer, not a fault: systemd reports an absent unit
// as loaded=not-found and exits zero, and a status screen that turned that
// into an error would be unusable on exactly the machine somebody is setting
// up.
func TestQueryCallsAnAbsentUnitAbsentRatherThanFailing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	// A name no installer would ever write, so the result cannot depend on
	// whether the developer running this happens to have atenea installed.
	const name = "atenea-absent-by-construction"
	state, err := platform.Query(name)
	if contract.KindOf(err) == contract.FailureUnavailable {
		t.Skipf("no systemd user manager on this machine: %v", err)
	}
	if err != nil {
		t.Fatalf("querying a unit nobody installed: %v", err)
	}

	if state.Installed {
		t.Error("a unit nobody installed is reported as installed")
	}
	if want := filepath.Join(root, "systemd", "user", name+".service"); state.Unit != want {
		t.Errorf("unit path %q, want %q", state.Unit, want)
	}
	if state.Detail == "" {
		t.Error("the status screen was handed nothing to print")
	}
}
