// Package desktop answers the `desktop.*` capabilities by driving this
// machine's own screen, pointer and accessibility tree.
//
// # Why an adapter and not a passthrough
//
// Atenea can already re-offer somebody else's MCP tools verbatim, and that is
// deliberately not what happens here. A passthrough forwards the caller's
// arguments unexamined -- internal/passthrough filters on the tool NAME and
// nothing else -- which is survivable for a search tool and is not survivable
// for one that can type. Every request this package sends is BUILT here from
// the capability's typed inputs; no list of actions supplied by a client is
// ever forwarded. That sentence is the whole gate, and it only holds while
// nothing in this file passes a caller's map straight through.
//
// It also means the funnel buys nothing: one implementation per capability, so
// there is no competitor to rank and a measurement would be a number with
// nothing to compare it against. The adapter exists to construct arguments and
// to close a door, not to compete.
//
// # Why the far side is a separate process
//
// Everything macOS exposes here lives behind Objective-C and Swift APIs. Bound
// through cgo they would sit inside the Go packages, be compiled on the macOS
// CI legs, be unreachable there -- a GitHub runner has no graphical session
// and no way to grant TCC -- and drag the coverage profile under the target
// the gate enforces. Out in helper/ they are not in the Go profile at all, and
// everything above this seam stays testable against a double on every leg.
package desktop

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// DefaultTimeout bounds one call to the helper.
//
// Sized for the slowest thing measured rather than the average: walking a
// browser's accessibility tree took 609ms on the machine this was written on,
// and the walk is one IPC message per node, so the number that matters is
// latency and not payload. Ten seconds leaves room for a tree several times
// that size while still failing fast enough that a stuck helper is reported
// rather than waited on.
const DefaultTimeout = 10 * time.Second

// Capability and implementation ids this adapter answers.
//
// The capability says WHAT and the implementation says HOW, which is why the
// implementation carries the platform in its name. `desktop.apps` is a
// question any desktop can be asked; `macos.apps` is the one answer this
// machine has, and naming it so leaves the obvious room for another.
//
// One word per dotted segment, matching every capability already shipped --
// graph.status, repository.index, symbol.overview. The id pattern refuses an
// underscore, and conforming is cheaper than widening a rule that has kept
// the vocabulary readable.
const (
	CapabilityListApps     = "desktop.apps"
	ImplementationListApps = "macos.apps"
)

// DefaultImplementations is what this adapter answers for when a settings file
// does not narrow it.
func DefaultImplementations() []string { return []string{ImplementationListApps} }

// Options configures the adapter.
type Options struct {
	// Implementations narrows what this runner claims. Empty takes
	// DefaultImplementations.
	Implementations []string
	// Timeout bounds one call. Zero takes DefaultTimeout.
	Timeout time.Duration
	// Session hands over a live session with the helper. It is a function
	// rather than a value because the process behind it is the supervisor's,
	// and may not have been started when this adapter was built.
	Session func(ctx context.Context) (*mcpstdio.Session, error)
	// Responsible reports whether Atenea is the process macOS will attribute
	// a device permission to. Injected so the refusal below can be tested on
	// a machine that is not a Mac -- and so a caller that knows better than
	// UnderLaunchd can say so.
	Responsible func() bool
}

// Runner is the far side of the desktop capabilities.
type Runner struct {
	implementations []string
	timeout         time.Duration
	session         func(ctx context.Context) (*mcpstdio.Session, error)
	responsible     func() bool
}

// New prepares the adapter. Nothing is dialed here: the helper is started by
// the supervisor on the first call that needs it.
func New(opts Options) (*Runner, error) {
	if opts.Session == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"desktop: no session to the helper was supplied")
	}
	implementations := slices.Clone(opts.Implementations)
	if len(implementations) == 0 {
		implementations = DefaultImplementations()
	}
	for _, id := range implementations {
		if id != ImplementationListApps {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"desktop: nothing here answers implementation %q", id)
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	responsible := opts.Responsible
	if responsible == nil {
		responsible = UnderLaunchd
	}
	return &Runner{
		implementations: implementations,
		timeout:         timeout,
		session:         opts.Session,
		responsible:     responsible,
	}, nil
}

// ID names the runner.
func (r *Runner) ID() string { return "desktop" }

// Surface says whose screen permissions this adapter would be using, which is
// the one fact about it worth a line on the status screen.
//
// It reports responsibility rather than whether the permissions are granted,
// and the choice is deliberate: a granted/denied line would be read as "Atenea
// may drive this machine", when what it actually means on a shell-started
// process is "the terminal may, and Atenea is standing behind it". The second
// sentence is the one somebody needs before deciding anything, and it is
// answerable here without asking the helper or the operating system.
func (r *Runner) Surface() string {
	if r.responsible() {
		return "service:own-permissions"
	}
	return "shell:borrowed-permissions"
}

// Serves reports whether this runner answers that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Implementations is what this runner declares itself the far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Capabilities is what its code can actually dispatch, which the wiring above
// checks against Implementations before anything runs.
func (r *Runner) Capabilities() []string { return []string{CapabilityListApps} }

// Run executes one step.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if err := r.permitted(req.Capability); err != nil {
		return contract.Outcome{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.granted(ctx, req.Capability.ID); err != nil {
		return contract.Outcome{}, err
	}

	switch req.Implementation.ID {
	case ImplementationListApps:
		return r.listApps(ctx)
	default:
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"desktop: nothing here answers implementation %q", req.Implementation.ID)
	}
}

// osNeeds says which operating-system permissions a capability actually
// requires, so a missing one becomes a sentence naming the pane to open rather
// than whatever error the API happens to raise ten frames later.
//
// Declared per capability rather than assumed for the whole adapter, and that
// distinction is load-bearing already: desktop.apps asks the window server
// what is running and needs neither permission, so gating it on Accessibility
// would refuse work that functions perfectly. The capabilities that read the
// accessibility tree or the screen do need them, and say so here.
var osNeeds = map[string]struct{ accessibility, screenRecording bool }{
	CapabilityListApps: {},
}

// permitted refuses a device capability on a process the system will not
// attribute the permission to.
//
// This is not a second permission gate -- the floor already refused anyone who
// was not granted `device`, and a second gate on the same seam is how the
// first stops being load-bearing. It is a refusal of a DIFFERENT thing: a
// grant that exists but belongs to somebody else. Run from a shell, Atenea
// borrows that shell's Accessibility, so the work would succeed while the
// settings file, the status screen and the receipt all describe a permission
// nobody gave Atenea and nobody can revoke through it.
//
// Refusing costs a real capability on a real machine, and it is still the
// right answer: succeeding here would make every other statement this program
// makes about device permissions false.
func (r *Runner) permitted(capability contract.Capability) error {
	if !slices.Contains(capability.Effects, contract.EffectDevice) {
		return nil
	}
	if r.responsible() {
		return nil
	}
	return contract.Fail(contract.FailurePermissionDenied,
		"desktop: %s causes the device effect, and Atenea is not running as a service -- "+
			"started from a shell it borrows that shell's screen and input permissions rather "+
			"than holding its own, so the grant could not be revoked through Atenea and its own "+
			"kill switch would not reach it. Install the service with `atenea service install` "+
			"and grant the permission to Atenea itself", capability.ID)
}

// granted refuses a capability whose operating-system permission is missing,
// naming which one and where it lives.
//
// Asked of the helper rather than assumed, because only the process that will
// actually make the call can answer it -- and asked at call time rather than
// probed on a schedule, because that is when somebody is present to read the
// answer and act on it. A capability that needs nothing skips the round trip
// entirely.
func (r *Runner) granted(ctx context.Context, capability string) error {
	needs, declared := osNeeds[capability]
	if !declared || (!needs.accessibility && !needs.screenRecording) {
		return nil
	}
	text, err := r.call(ctx, "health", map[string]any{})
	if err != nil {
		return err
	}
	var state struct {
		Accessibility   bool   `json:"accessibility"`
		ScreenRecording bool   `json:"screen_recording"`
		Missing         string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(text), &state); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"desktop: the helper's health answer is not the shape this expects: %v", err)
	}
	if needs.accessibility && !state.Accessibility || needs.screenRecording && !state.ScreenRecording {
		return contract.Fail(contract.FailurePermissionDenied,
			"desktop: %s cannot run because %s", capability, state.Missing)
	}
	return nil
}

// listApps answers desktop.apps.
//
// The payload sent to the helper is built here and is empty by construction:
// this capability takes no input, so there is nothing a caller could put in it.
// The shape is deliberate -- every capability added below should build its own
// arguments the same way, from typed fields, never by forwarding req.Payload.
func (r *Runner) listApps(ctx context.Context) (contract.Outcome, error) {
	started := time.Now()
	text, err := r.call(ctx, "list_apps", map[string]any{})
	if err != nil {
		return contract.Outcome{}, err
	}
	var answer struct {
		Apps []struct {
			PID       int    `json:"pid"`
			Name      string `json:"name"`
			BundleID  string `json:"bundle_id"`
			Frontmost bool   `json:"frontmost"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"desktop: the helper's answer to list_apps is not the shape this expects: %v", err)
	}
	apps := make([]map[string]any, 0, len(answer.Apps))
	for _, app := range answer.Apps {
		apps = append(apps, map[string]any{
			"pid":       app.PID,
			"name":      app.Name,
			"bundle_id": app.BundleID,
			"frontmost": app.Frontmost,
		})
	}
	return contract.Outcome{
		Result:  map[string]any{"apps": apps},
		Verdict: contract.VerdictOK,
		// Duration only. No tokens and no memory: the far side is a process
		// the supervisor owns rather than one this call spawned, and asking
		// the window server what is running is not a model turn. Inventing
		// either figure would poison the baseline the selector ranks on.
		Spent: contract.Sample{Duration: time.Since(started)},
		// Zero WITH Known, which are two different statements. Nothing here
		// charges anything, and saying so is not the same as staying silent:
		// contract 3.3.0 exists precisely because a bare zero read as both
		// "free" and "nobody said".
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}

// call reaches the helper and returns its text answer.
func (r *Runner) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	session, err := r.session(ctx)
	if err != nil {
		// The remedy in the message, because the ordinary cause is that
		// nobody built the helper yet and the operating system's own words
		// for that are "no such file or directory".
		//
		// It is built on the machine that runs it rather than shipped, and
		// that is not an omission. Distributing a macOS binary that drives
		// the screen needs a Developer ID signature and notarization;
		// measured on macOS 26.6, an Apple Development signature is rejected
		// by Gatekeeper on any other machine exactly as an unsigned one is.
		// Locally compiled code carries no quarantine attribute, so building
		// it here works with no certificate at all.
		return "", contract.Fail(contract.FailureUnavailable,
			"desktop: the helper is not reachable: %v -- build it with "+
				"`swift build -c release --package-path helper` and point "+
				"orchestrator.desktop.process.command at the binary in "+
				"helper/.build/release/", err)
	}
	text, err := session.Call(ctx, tool, args)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"desktop: %s: %v", tool, err)
	}
	if text == "" {
		return "", contract.Fail(contract.FailureUnavailable,
			"desktop: %s answered with nothing", tool)
	}
	return text, nil
}
