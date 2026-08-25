package platform

import (
	"fmt"
	"html"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// ServiceName is the one name the background service answers to, on the
// machine and on the command line. One copy, for the same reason the roots
// above have one: two spellings disagree the first time one of them is fixed.
const ServiceName = "atenea"

// Service is Atenea as a thing the machine starts on its own.
//
// On Linux it is a systemd *user* service, at ~/.config/systemd/user, and never
// a system one under /etc. On macOS it is a launchd per-user agent under
// ~/Library/LaunchAgents. The target machine forces that -- an unprivileged
// account with no sudo -- but it is also the only shape that works. Atenea
// drives CLIs that are already logged in as this user: Claude Code holds an
// OAuth session under this home, and every byte Atenea writes lands under this
// user's XDG roots. A root unit would start a process owning none of that: no
// session to reuse, no tokens, and every file it wrote afterwards owned by
// root in a directory the user then has to repair by hand.
type Service struct {
	Name string
	// Unit is where the description of the service goes on disk.
	Unit string
	// Exec is the binary the manager starts, absolute always. See NewService.
	Exec string
	// StopGrace is the margin `run` gives in-flight work on the way down: the
	// settings file's core.shutdown_grace, carried here so the unit can be
	// told to wait longer than that rather than cut it short.
	StopGrace time.Duration
}

// ServiceState is what the machine currently says about the service.
type ServiceState struct {
	Installed bool
	// Enabled means the manager has been told to start it. On a user manager
	// that is only half the answer -- see Linger.
	Enabled bool
	Active  bool
	// Linger means this user's services run without the user being logged in.
	// It is what turns Enabled into "starts at boot" rather than "starts at
	// next login", and Atenea cannot switch it on for itself.
	Linger bool
	Unit   string
	// Detail is one line from the manager for the status screen, in the
	// manager's own words: the screen shows a handful of booleans and the
	// booleans never say why.
	Detail string
}

// stopMargin is what TimeoutStopSec gets on top of StopGrace.
//
// `atenea run` stops cleanly and that stop is bounded by core.shutdown_grace:
// in-flight work is given that long to finish and the measurement batch is
// written on the way out. A TimeoutStopSec equal to or shorter than the grace
// would have systemd send SIGKILL in the middle of the very flush the grace
// exists to allow -- and it would look like a clean stop from the outside
// while losing data every single time. The margin covers the signal arriving,
// the grace running its full length, and the last write landing.
const stopMargin = 5 * time.Second

// NewService describes the service that would run exec. It touches nothing.
func NewService(exec string, stopGrace time.Duration) (Service, error) {
	if strings.TrimSpace(exec) == "" {
		return Service{}, contract.Fail(contract.FailureInvalidInput,
			"the service needs the path of the atenea binary it should start")
	}
	if !filepath.IsAbs(exec) {
		// A service manager starts a binary from no particular directory and
		// with no PATH worth relying on, so a relative path does not fail
		// here: it fails at the next boot, in a log nobody is reading.
		return Service{}, contract.Fail(contract.FailureInvalidInput,
			"the service needs an absolute path to the atenea binary, got %q", exec)
	}
	if strings.ContainsFunc(exec, isControl) {
		// Every other awkward character in a path can be written so that the
		// manager reads it back unchanged -- see systemdValue below. A control
		// character cannot: a newline inside ExecStart ends the directive and
		// turns the rest of the path into a second directive of the [Service]
		// section, which systemd either rejects or, worse, honors. The unit is
		// a single line per setting by construction, so this is refused here,
		// where a person is still watching, rather than at the next boot.
		return Service{}, contract.Fail(contract.FailureInvalidInput,
			"the path of the atenea binary must not contain control characters, got %q", exec)
	}
	if stopGrace < 0 {
		// A negative grace would eat into the margin and render a unit that
		// kills sooner than it asks. The settings file validates its own
		// value; this is the floor for everyone who does not.
		stopGrace = 0
	}
	return Service{
		Name:      ServiceName,
		Unit:      unitPath(ServiceName),
		Exec:      exec,
		StopGrace: stopGrace,
	}, nil
}

// The unit below is rendered here, in the portable half of this package, and
// not beside the code that installs it. It is pure text: no machine is asked
// anything to produce it. Keeping it here means the unit a Debian box will get
// can be printed, diffed and tested from any developer laptop -- including the
// one guard rail below, which is worth nothing if it only runs on the platform
// that would notice too late.
//
// What the unit does contain is deliberately modest. NoNewPrivileges and
// PrivateTmp cannot break a process that only spawns CLIs and writes under its
// own XDG roots, so they cost nothing.
//
// What it does NOT contain is the part worth explaining. The network posture
// is "local and not exposed", and the obvious way to spell that in a unit is
// IPAddressDeny=any or PrivateNetwork=yes. Both would break Atenea. The
// posture is already true by construction: searching this repository for
// net.Listen, ListenAndServe and Accept turns up nothing outside two
// httptest stubs in _test.go files, which never enter the binary. Atenea
// accepts a connection from nobody. But it dials out constantly.
// The Serena adapter posts JSON-RPC to an MCP proxy on 127.0.0.1:40010 and the
// Claude Code adapter reaches a paid model over the internet; denying egress
// takes both providers down, and the failure would read as two broken adapters
// rather than as one line in a unit file. Not exposed is a claim about
// connections accepted, never about connections made.
// WorkingDirectory is the line that stops a relative repository path from
// meaning the user's home directory.
//
// A user unit with no WorkingDirectory starts in $HOME, and the shipped
// settings declare `path = "."` for the `current` repository -- which is how a
// fresh install works against whatever tree you are standing in when you run
// the CLI, and is exactly wrong for a daemon that is standing nowhere. The
// combination meant a `code.search` from a chat raked the whole home
// directory: Documents, mail, .ssh, .aws. Pointed at the state root it
// resolves somewhere Atenea owns and that holds nothing to find.
//
// It does not make `path = "."` correct for a service -- a repository of
// nothing is still not the repository anybody meant -- but wrong and empty is
// a different thing from wrong and private.
const unitTemplate = `[Unit]
Description=Atenea orchestration core
After=default.target

[Service]
Type=simple
ExecStart=%s run
WorkingDirectory=%s
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=%d
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
`

// UnitText renders the unit file.
//
// The same Service always renders the same bytes. An operator has to be able
// to diff what is installed against what this version would install, and a
// unit carrying a timestamp or the order of a map walk differs from itself for
// no reason anyone can act on.
func (s Service) UnitText() string {
	if runtime.GOOS == "darwin" {
		return launchdText(s)
	}
	return systemdText(s)
}

// systemdText renders the systemd unit. It is split out from UnitText, which
// can only ever return one of the two managers' texts on any given machine,
// so that a test running on either platform still checks what the other one
// would install -- the same reason the template itself lives in the portable
// half of this package.
func systemdText(s Service) string {
	return fmt.Sprintf(unitTemplate, systemdValue(s.Exec), systemdValue(StateDir()), s.stopSeconds())
}

// stopSeconds is how long the manager must wait after SIGTERM before killing,
// in the whole seconds both managers count in.
//
// Rounded up, because rounding a 10.5s grace down to 10 would put the kill
// back inside the window the margin exists to clear. Shared by both branches
// of UnitText so the two managers cannot drift apart: the grace is a property
// of what `run` does on the way down, not of the machine it runs on.
func (s Service) stopSeconds() int64 {
	return int64((s.StopGrace + stopMargin + time.Second - 1) / time.Second)
}

// isControl reports whether r would break the one-line-per-setting shape a
// unit file has. See NewService, which refuses a path carrying one.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// systemdValue renders a path the way systemd's own parser reads it back,
// because an absolute path is still user input -- the same reason launchdText
// escapes it for XML. Both ExecStart and WorkingDirectory go through it: they
// are the same kind of value and systemd reads them the same way.
//
// Three things happen to an unquoted value. systemd splits it on whitespace,
// so /opt/My Apps/atenea becomes the binary /opt/My with the argument
// Apps/atenea. It expands `%` specifiers, so a path containing %h silently
// becomes the home directory. And a `"` or `\` inside the value has its own
// meaning to the parser. Quoting handles the first, doubling `%` handles the
// second, and escaping the two metacharacters handles the third. All of it is
// invisible until the boot that does not come up: the unit installs and
// enables without complaint either way.
func systemdValue(path string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(path)
	return `"` + escaped + `"`
}

// launchdText is kept beside the common renderer so Service.UnitText remains
// the one public inspection point on every supported platform. launchd plist
// values are escaped because an absolute binary path is still user input.
//
// WorkingDirectory is here for the reason unitTemplate gives: without it a
// launchd agent starts in $HOME, and the shipped `path = "."` then names the
// user's home directory as a repository to search.
//
// ExitTimeOut is the launchd counterpart of systemd's TimeoutStopSec, and it
// is spelled out for the same reason: launchd's own default is 20 seconds,
// after which it sends SIGKILL. A shutdown grace longer than that -- the
// settings file allows one -- would be cut short mid-flush on macOS while the
// same configuration is honored on Linux, and the stop would look clean from
// the outside both times.
func launchdText(s Service) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.tutitoos.atenea</string>
	<!-- Interactive and Aqua, not Background, and this is not about priority.
	     A capability that reads the screen needs a window server connection,
	     and a Background agent outside the Aqua session has none: measured,
	     ScreenCaptureKit aborts the process inside SkyLight's
	     SLSGetDisplaysWithRect rather than returning an error. Everything else
	     Atenea does works either way, so the cost of the stronger session is
	     nothing and the cost of the weaker one is a crash somebody has to read
	     a panic log to understand. -->
	<key>ProgramArguments</key>
	<array><string>%s</string><string>run</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>5</integer>
	<key>ExitTimeOut</key><integer>%d</integer>
	<key>ProcessType</key><string>Interactive</string>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>WorkingDirectory</key><string>%s</string>
</dict>
</plist>
`, html.EscapeString(s.Exec), s.stopSeconds(), html.EscapeString(StateDir()))
}
