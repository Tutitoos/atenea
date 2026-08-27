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
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/internal/platform"
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
	CapabilityListApps       = "desktop.apps"
	ImplementationListApps   = "macos.apps"
	CapabilityInspect        = "desktop.inspect"
	ImplementationInspect    = "macos.inspect"
	CapabilityScreenshot     = "desktop.screenshot"
	ImplementationScreenshot = "macos.screenshot"
)

// The mutating half. Each capability is one act, named the way both vendors'
// computer-use APIs name it, so a model that learned the vocabulary elsewhere
// does not have to be taught this one.
//
// mutations maps a capability to the helper tool that performs it and the
// typed fields the adapter copies across. Nothing outside this table reaches
// the far side: that is what keeps a caller from naming an action.
var mutations = map[string]struct {
	implementation string
	tool           string
	fields         []string
}{
	"desktop.click":  {"macos.click", "click", []string{"x", "y", "clicks"}},
	"desktop.move":   {"macos.move", "move", []string{"x", "y"}},
	"desktop.drag":   {"macos.drag", "drag", []string{"from_x", "from_y", "to_x", "to_y"}},
	"desktop.scroll": {"macos.scroll", "scroll", []string{"x", "y", "dx", "dy"}},
	"desktop.type":   {"macos.type", "type", []string{"text"}},
	"desktop.key":    {"macos.key", "key", []string{"key", "modifiers"}},
}

// Ceilings for one inspect call, and the first of them is the one that binds.
//
// Measured on the machine this was written for: Finder's whole tree was 349
// nodes and 17KB in 122ms; Chrome's was 1513 nodes and 91KB in 609ms. The
// bytes are nothing next to what this transport already carries. The
// milliseconds are the cost, because a tree is walked over IPC one message per
// node and one unresponsive application can hold the walk.
//
// So the budget is time, sized at roughly three times the slowest thing
// measured, and the rest are a second net under it -- seven times the node
// count of a real browser, eleven times its bytes, twice its depth. Numbers
// with a reading behind them rather than round ones.
const (
	defaultBudget   = 2 * time.Second
	defaultMaxNodes = 10_000
	defaultMaxBytes = 1 << 20
	defaultMaxDepth = 40
)

// DefaultImplementations is what this adapter answers for when a settings file
// does not narrow it.
func DefaultImplementations() []string {
	out := []string{ImplementationListApps, ImplementationInspect, ImplementationScreenshot}
	for _, m := range mutations {
		out = append(out, m.implementation)
	}
	slices.Sort(out)
	return out
}

// capabilityFor maps an implementation back to what it answers, so dispatch
// needs one table rather than two that can disagree.
func capabilityFor(implementation string) (string, bool) {
	for capability, m := range mutations {
		if m.implementation == implementation {
			return capability, true
		}
	}
	return "", false
}

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
	// Allowed is which applications may be looked at, by bundle identifier.
	// EMPTY DENIES EVERYTHING, which is the shipped posture and the opposite
	// of the usual reading: a capability that can read every window on
	// somebody's machine must not be enabled by a settings file that forgot
	// to mention it.
	Allowed []string
	// Denied always wins over Allowed. Two lists rather than one because a
	// single list would make "never look at my password manager" a thing you
	// state by omission, and omission is what happens when somebody adds an
	// entry in a hurry.
	Denied []string
	// SignatureStable reports whether this binary's signature will keep a
	// permission across a rebuild. Injected for the same reason Responsible
	// is: so the warning can be tested on a machine that is not a Mac.
	SignatureStable func() (bool, string)
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
	signature       func() (bool, string)
	allowed, denied []string
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
	known := DefaultImplementations()
	for _, id := range implementations {
		if !slices.Contains(known, id) {
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
	signature := opts.SignatureStable
	if signature == nil {
		signature = platform.SelfSignedStably
	}
	return &Runner{
		implementations: implementations,
		timeout:         timeout,
		session:         opts.Session,
		responsible:     responsible,
		signature:       signature,
		allowed:         slices.Clone(opts.Allowed),
		denied:          slices.Clone(opts.Denied),
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
	surface := "shell:borrowed-permissions"
	if r.responsible() {
		surface = "service:own-permissions"
	}
	// The second half is here rather than on a startup line because this is
	// where somebody already looks, and because the failure it warns about is
	// one you only notice when something stops working for no visible reason.
	if stable, _ := r.signature(); !stable {
		surface += " (ad-hoc: grant dies on next build)"
	}
	return surface
}

// Serves reports whether this runner answers that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Implementations is what this runner declares itself the far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Capabilities is what its code can actually dispatch, which the wiring above
// checks against Implementations before anything runs.
func (r *Runner) Capabilities() []string {
	out := []string{CapabilityListApps, CapabilityInspect, CapabilityScreenshot}
	for capability := range mutations {
		out = append(out, capability)
	}
	slices.Sort(out)
	return out
}

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
	case ImplementationInspect:
		return r.inspect(ctx, req)
	case ImplementationScreenshot:
		return r.screenshot(ctx, req)
	default:
		if _, ok := capabilityFor(req.Implementation.ID); ok {
			return r.mutate(ctx, req)
		}
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
	CapabilityListApps:   {},
	CapabilityInspect:    {accessibility: true},
	CapabilityScreenshot: {screenRecording: true},
	"desktop.click":      {accessibility: true},
	"desktop.move":       {accessibility: true},
	"desktop.drag":       {accessibility: true},
	"desktop.scroll":     {accessibility: true},
	"desktop.type":       {accessibility: true},
	"desktop.key":        {accessibility: true},
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
	// Two different remedies, because the two causes look identical from here
	// and sending somebody to the wrong one costs an afternoon. Reaching this
	// from `atenea ask` means the service may be perfectly well installed and
	// the problem is the door: ask runs the core in the CLI process, which was
	// started from a shell. Reaching it inside the service would mean the
	// service is not running at all.
	return contract.Fail(contract.FailurePermissionDenied,
		"desktop: %s causes the device effect, and this process is not the one the system grants "+
			"that permission to -- started from a shell it borrows the terminal's screen and input "+
			"access instead, which Atenea could neither revoke nor switch off. If you ran `atenea "+
			"ask`, that runs here rather than in the service: use `atenea desktop %s`, or call it "+
			"through an MCP client. If you are already in the service, it is not running as a "+
			"launchd agent -- `atenea service install`", capability.ID,
		strings.TrimPrefix(capability.ID, "desktop."))
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
	// Named against what THIS capability needs, not against everything the
	// machine happens to be missing. The helper reports the whole picture;
	// repeating it here would send somebody to grant Screen Recording for a
	// capability that only ever reads the accessibility tree, and a permission
	// granted for no reason is the one nobody remembers to take back.
	var missing []string
	if needs.accessibility && !state.Accessibility {
		missing = append(missing, "Accessibility")
	}
	if needs.screenRecording && !state.ScreenRecording {
		missing = append(missing, "Screen Recording")
	}
	if len(missing) > 0 {
		return contract.Fail(contract.FailurePermissionDenied,
			"desktop: %s needs %s, which Atenea does not have -- grant it in System Settings > "+
				"Privacy & Security > %s, to Atenea itself and not to a terminal",
			capability, strings.Join(missing, " and "), missing[0])
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
		return "", helperFailure(tool, err)
	}
	if text == "" {
		return "", contract.Fail(contract.FailureUnavailable,
			"desktop: %s answered with nothing", tool)
	}
	return text, nil
}

// AllApplications is the token an operator writes to widen the allow-list to
// every application Denied does not name.
//
// It lives here, where it is enforced, and internal/config reads it from here:
// the settings package already depends on the adapters for their defaults, and
// the spelling of a permission token must have exactly one home.
const AllApplications = "*"

// mayLookAt applies the allow-list. Denied first, and the order is the rule
// rather than an optimization: a settings file naming the same application in
// both lists is a mistake, and the safe reading of a mistake is the refusal.
//
// The wildcard widens the second half only. An empty bundle id is still no
// answer -- the token says which applications are allowed, not that the target
// no longer has to be resolved -- and Denied is still read first, so the widest
// allow-list an operator can write still cannot reach a password manager.
func (r *Runner) mayLookAt(bundleID string) bool {
	if bundleID == "" || slices.Contains(r.denied, bundleID) {
		return false
	}
	if slices.Contains(r.allowed, AllApplications) {
		return true
	}
	return slices.Contains(r.allowed, bundleID)
}

// target resolves which application a call is about and refuses one the
// allow-list does not name.
//
// The refusal lives here, in Go, and not in the helper. Policy belongs where
// the settings file is read; the helper is the mechanism and is deliberately
// given no opinion about what it may look at. Splitting it the other way would
// put the allow-list in a process that has to be rebuilt to change it.
//
// Resolved through list_apps rather than trusting a pid the caller supplied.
// A pid is a number anybody can type, and this is the seam where "which
// application" stops being the caller's claim and becomes a fact the machine
// checked.
func (r *Runner) target(ctx context.Context, bundleID string) (int, string, error) {
	// Two refusals, because the two causes are different facts and the remedy
	// for one does not work on the other. Denied is checked first and named on
	// its own: telling somebody to add an application they already allowed --
	// which is what "*" does -- sends them to edit a list that was never the
	// thing refusing, and the edit they make will not help.
	if slices.Contains(r.denied, bundleID) {
		return 0, "", contract.Fail(contract.FailurePermissionDenied,
			"desktop: %q is on [desktop] denied, which wins over every allow-list including %q -- "+
				"adding it to applications will not change this; remove it from denied if that is "+
				"deliberate", bundleID, AllApplications)
	}
	if !r.mayLookAt(bundleID) {
		return 0, "", contract.Fail(contract.FailurePermissionDenied,
			"desktop: %q is not in the desktop allow-list -- add it to [desktop] applications "+
				"in the settings file, or %q for every application denied does not name",
			bundleID, AllApplications)
	}
	text, err := r.call(ctx, "list_apps", map[string]any{})
	if err != nil {
		return 0, "", err
	}
	var answer struct {
		Apps []struct {
			PID      int    `json:"pid"`
			Name     string `json:"name"`
			BundleID string `json:"bundle_id"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return 0, "", contract.Fail(contract.FailureUnavailable,
			"desktop: the helper's answer to list_apps is not the shape this expects: %v", err)
	}
	for _, app := range answer.Apps {
		if app.BundleID == bundleID {
			return app.PID, app.Name, nil
		}
	}
	return 0, "", contract.Fail(contract.FailureNotFound,
		"desktop: %q is allowed but is not running", bundleID)
}

// inspect answers desktop.inspect.
func (r *Runner) inspect(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	started := time.Now()
	bundleID, _ := req.Payload["application"].(string)
	pid, name, err := r.target(ctx, bundleID)
	if err != nil {
		return contract.Outcome{}, err
	}
	// Built here from typed fields, never forwarded. The ceilings are this
	// adapter's to set even when the caller named none, because a call with
	// no ceiling is the one that hangs.
	args := map[string]any{
		"pid":       pid,
		"bundle_id": bundleID,
		"app":       name,
		"budget_ms": int(defaultBudget / time.Millisecond),
		"max_nodes": defaultMaxNodes,
		"max_bytes": defaultMaxBytes,
		"max_depth": defaultMaxDepth,
	}
	if roles, ok := req.Payload["roles"].([]any); ok && len(roles) > 0 {
		filter := make([]string, 0, len(roles))
		for _, role := range roles {
			if text, ok := role.(string); ok {
				filter = append(filter, text)
			}
		}
		args["roles"] = filter
	}
	text, err := r.call(ctx, "inspect", args)
	if err != nil {
		return contract.Outcome{}, err
	}
	var answer struct {
		Nodes     []map[string]any `json:"nodes"`
		Count     int              `json:"count"`
		Truncated string           `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"desktop: the helper's answer to inspect is not the shape this expects: %v", err)
	}
	result := map[string]any{
		"nodes": answer.Nodes,
		"count": answer.Count,
		// Marked on the result rather than left for a reader to infer. Screen
		// content is written by whoever controls the window, and a caller that
		// forgets that is one instruction away from acting on somebody else's
		// text. Every node carries its own app and bundle_id too, so the
		// provenance survives a result that mixes several.
		"untrusted": true,
	}
	if answer.Truncated != "" {
		result["truncated"] = answer.Truncated
	}
	return contract.Outcome{
		Result: result, Verdict: contract.VerdictOK,
		Spent:         contract.Sample{Duration: time.Since(started)},
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}

// screenshot answers desktop.screenshot.
func (r *Runner) screenshot(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	started := time.Now()
	bundleID, _ := req.Payload["application"].(string)
	pid, name, err := r.target(ctx, bundleID)
	if err != nil {
		return contract.Outcome{}, err
	}
	text, err := r.call(ctx, "screenshot", map[string]any{"pid": pid})
	if err != nil {
		return contract.Outcome{}, err
	}
	var answer struct {
		PNG    string  `json:"png_base64"`
		Width  int     `json:"width"`
		Height int     `json:"height"`
		Scale  float64 `json:"scale"`
		Bytes  int     `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"desktop: the helper's answer to screenshot is not the shape this expects: %v", err)
	}
	return contract.Outcome{
		Result: map[string]any{
			"png_base64": answer.PNG,
			// The image's own dimensions, which is the coordinate space
			// anything read off it is expressed in. The helper already
			// reduced from the display's real pixels; scale says by how
			// much, so a reader can tell a small window from a heavily
			// reduced one without needing it to interpret anything.
			"width":  answer.Width,
			"height": answer.Height,
			// A string because the contract has no float type, and inventing
			// one for a diagnostic would be the tail wagging the dog. Nothing
			// reads it to compute with: coordinates are already in the
			// returned image's own space.
			"scale":       strconv.FormatFloat(answer.Scale, 'g', -1, 64),
			"bytes":       answer.Bytes,
			"application": name,
			"bundle_id":   bundleID,
			"untrusted":   true,
		},
		Verdict:       contract.VerdictOK,
		Spent:         contract.Sample{Duration: time.Since(started)},
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}

// helperFailure sorts what the helper said into the right bin, and the
// distinction it draws is not cosmetic.
//
// Atenea's funnel marks an implementation DOWN when a call comes back
// unavailable, and a provider marked down stops being chosen until its health
// goes stale. So sorting a refusal as unavailable takes the capability out of
// service for everybody: measured, asking to capture a window that was not
// open disabled desktop.screenshot entirely, and the receipt said the provider
// was down when the provider had answered correctly.
//
// A refusal the helper labeled is an answer about the request. Anything it did
// not label is the provider's own problem, which is the safe reading for a
// failure nobody classified.
func helperFailure(tool string, err error) error {
	var answer struct {
		Error string `json:"error"`
		Kind  string `json:"kind"`
	}
	// The text carries the helper's JSON when it refused; when the transport
	// itself broke there is nothing to parse and the default stands.
	if start := strings.Index(err.Error(), "{"); start >= 0 {
		_ = json.Unmarshal([]byte(err.Error()[start:]), &answer)
	}
	switch answer.Kind {
	case "denied":
		// The request was understood and refused: no window open, a secure
		// field, an application that will not answer. Nothing is wrong with
		// the helper.
		return contract.Fail(contract.FailureNotFound, "desktop: %s: %s", tool, answer.Error)
	case "invalid":
		return contract.Fail(contract.FailureInvalidInput, "desktop: %s: %s", tool, answer.Error)
	}
	return contract.Fail(contract.FailureUnavailable, "desktop: %s: %v", tool, err)
}

// credential matches text that should never be typed by an automated caller.
//
// The same patterns the contract already uses to keep secrets out of durable
// storage, reused here to keep them out of somebody's keyboard. It is a
// refusal and not a filter: a caller that meant to fill in a password has to
// be told no, rather than quietly handed a redacted string that looks like it
// worked and leaves a half-typed credential in a field.
//
// It is deliberately not the only defense. The helper refuses to type into a
// field macOS itself marks as secure, which catches the case this cannot --
// a password that looks like an ordinary word.
func credential(text string) bool {
	// The marker, not inequality. RedactRaw also trims and bounds its input,
	// so comparing before and after reports a leading space as a credential --
	// measured, on " Y la ruta confirmada sigue viva.", which refused an
	// obviously harmless sentence and would have taught somebody to work
	// around this check rather than trust it. A refusal that fires on nothing
	// is worse than no refusal, because it is the one people learn to bypass.
	return strings.Contains(contract.RedactRaw(text), "[REDACTED")
}

// mutate performs one act on the desktop.
//
// Every argument is copied field by field from the capability's declared
// inputs into the helper's own shape. A caller cannot name the tool, cannot
// add a field, and cannot reach anything not in the table above: the schema
// refuses undeclared input before this runs, and this copies only what it
// knows. Two defenses rather than one, because either alone is a single point
// of failure on the surface that can type.
func (r *Runner) mutate(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	started := time.Now()
	capability, ok := capabilityFor(req.Implementation.ID)
	if !ok {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"desktop: nothing here answers implementation %q", req.Implementation.ID)
	}
	spec := mutations[capability]

	// The allow-list applies to acting exactly as it applies to reading, and
	// the target is resolved rather than taken on the caller's word.
	bundleID, _ := req.Payload["application"].(string)
	pid, name, err := r.target(ctx, bundleID)
	if err != nil {
		return contract.Outcome{}, err
	}

	args := map[string]any{}
	for _, field := range spec.fields {
		value, present := req.Payload[field]
		if !present {
			continue
		}
		if field == "text" {
			text, _ := value.(string)
			if credential(text) {
				return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
					"desktop: that text looks like a credential, and this refuses to type one. "+
						"If it is not, rephrase it so it does not read as one; if it is, type it yourself")
			}
		}
		args[field] = value
	}

	text, err := r.call(ctx, spec.tool, args)
	if err != nil {
		return contract.Outcome{}, err
	}
	var acknowledged map[string]any
	if err := json.Unmarshal([]byte(text), &acknowledged); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"desktop: the helper's answer to %s is not the shape this expects: %v", spec.tool, err)
	}
	// What was done and to whom, and nothing else.
	//
	// The helper's own acknowledgement is read -- it has to parse, which is
	// what proves the far side did the thing rather than merely accepting the
	// message -- and then dropped. Two reasons. The contract refuses output
	// fields a capability did not declare, and it is right to: a result that
	// grew a field per implementation would stop being a capability. And what
	// it acknowledges for `type` is a character count, which is a fact about
	// somebody's keystrokes that has no business in a receipt.
	_ = acknowledged
	_ = pid
	result := map[string]any{
		"did":         capability,
		"application": name,
		"bundle_id":   bundleID,
	}
	return contract.Outcome{
		Result: result, Verdict: contract.VerdictOK,
		Spent:         contract.Sample{Duration: time.Since(started)},
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}
