package desktop_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/desktop"
	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// fakeHelper is an MCP server over pipes, standing in for the Swift binary.
//
// It is here rather than a build tag around the real helper because the real
// helper only exists on macOS, and the logic being tested -- what gets sent,
// what comes back, what is refused -- is the same everywhere. A test that only
// ran on one leg of the matrix would leave that logic uncovered on the other
// three, which is exactly the hole the whole separate-process design exists to
// avoid.
func fakeHelper(t *testing.T, answers map[string]any) func(context.Context) (*mcpstdio.Session, error) {
	t.Helper()
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	seen := make(chan map[string]any, 8)

	go func() {
		defer func() { _ = fromServer.Close() }()
		decoder := json.NewDecoder(toServer)
		for {
			var msg map[string]any
			if err := decoder.Decode(&msg); err != nil {
				return
			}
			id, hasID := msg["id"]
			if !hasID {
				continue // a notification answers nobody
			}
			var result any
			switch msg["method"] {
			case "initialize":
				result = map[string]any{"protocolVersion": "2025-06-18",
					"serverInfo": map[string]any{"name": "fake", "version": "0"}}
			case "tools/call":
				params, _ := msg["params"].(map[string]any)
				seen <- params
				name, _ := params["name"].(string)
				body, _ := json.Marshal(answers[name])
				result = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": string(body)}},
				}
			default:
				result = map[string]any{}
			}
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			_, _ = fromServer.Write(append(out, '\n'))
		}
	}()

	session := mcpstdio.New(fromClient, toClient, mcpstdio.Options{})
	t.Cleanup(func() { _ = session.Close() })
	t.Cleanup(func() { close(seen) })
	callsSeen = seen
	return func(context.Context) (*mcpstdio.Session, error) { return session, nil }
}

// callsSeen carries what the fake was asked, so a test can assert on the
// arguments the adapter BUILT rather than on the answer it got back. That is
// the property worth pinning: a client's payload must never reach the far side.
var callsSeen chan map[string]any

func request(t *testing.T, capability contract.Capability, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: desktop.ImplementationListApps, Capability: capability.ID},
		Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
		Payload:        payload,
		Permission:     contract.Permission{Task: "list what is open", Effects: capability.Effects},
	}
}

func appsCapability(effects ...contract.Effect) contract.Capability {
	if len(effects) == 0 {
		effects = []contract.Effect{contract.EffectRead, contract.EffectDevice}
	}
	return contract.Capability{
		ID: desktop.CapabilityListApps, Version: contract.Version{Major: 1},
		Summary: "List the applications with a user interface running on this machine.",
		Effects: effects,
		Outputs: []contract.Field{{Name: "apps", Type: contract.TypeRecordList, Required: true,
			Summary: "The running applications.",
			Fields: []contract.Field{
				{Name: "pid", Type: contract.TypeInt, Required: true, Summary: "Process id."},
				{Name: "name", Type: contract.TypeString, Required: true, Summary: "Display name."},
			}}},
	}
}

func TestAppsAreReadBackFromTheHelper(t *testing.T) {
	session := fakeHelper(t, map[string]any{"list_apps": map[string]any{
		"apps": []any{map[string]any{
			"pid": 42, "name": "Finder", "bundle_id": "com.apple.finder", "frontmost": true,
		}},
	}})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := runner.Run(t.Context(), request(t, appsCapability(), nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	apps, ok := out.Result["apps"].([]map[string]any)
	if !ok || len(apps) != 1 {
		t.Fatalf("result = %+v", out.Result)
	}
	if apps[0]["name"] != "Finder" || apps[0]["bundle_id"] != "com.apple.finder" {
		t.Errorf("app = %+v", apps[0])
	}
	// Free work says so, rather than staying silent and being read as
	// "nobody said". The two are different facts; contract 3.3.0 exists to
	// keep them apart.
	if out.SpentUSD != 0 || !out.SpentUSDKnown {
		t.Errorf("spent = %v known = %v, want a declared zero", out.SpentUSD, out.SpentUSDKnown)
	}
}

// The gate has two layers and both are pinned, because either alone is a
// single point of failure.
//
// Layer one: a field the capability never declared is refused before the
// adapter runs at all. Layer two, below: what does reach the helper is built
// here from typed inputs rather than forwarded. A passthrough has neither --
// internal/passthrough filters on the tool NAME and hands the arguments over
// untouched, which is survivable for a search tool and is not survivable for
// one that can type.
func TestAnUndeclaredPayloadIsRefusedBeforeAnythingRuns(t *testing.T) {
	session := fakeHelper(t, map[string]any{"list_apps": map[string]any{"apps": []any{}}})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	smuggled := map[string]any{"action": "type", "text": "rm -rf /"}
	_, err = runner.Run(t.Context(), request(t, appsCapability(), smuggled))
	if err == nil {
		t.Fatal("a payload naming fields this capability never declared was accepted")
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("failure = %v, want invalid_input", got)
	}
}

func TestTheHelperIsSentArgumentsThisAdapterBuilt(t *testing.T) {
	session := fakeHelper(t, map[string]any{"list_apps": map[string]any{"apps": []any{}}})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(t.Context(), request(t, appsCapability(), nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	params := <-callsSeen
	if name, _ := params["name"].(string); name != "list_apps" {
		t.Errorf("helper was asked for %q", name)
	}
	args, _ := params["arguments"].(map[string]any)
	if len(args) != 0 {
		t.Fatalf("the helper was sent %+v; this capability takes no input and must send none", args)
	}
}

func TestDeviceIsRefusedWhenAteneaIsNotTheResponsibleProcess(t *testing.T) {
	session := fakeHelper(t, map[string]any{"list_apps": map[string]any{"apps": []any{}}})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(t.Context(), request(t, appsCapability(), nil))
	if err == nil {
		t.Fatal("a device capability ran on a process the system attributes nobody's permission to")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("failure = %v, want permission_denied", got)
	}
	// The remedy has to be in the message. A refusal that does not say what
	// to do next is a refusal somebody works around.
	if !strings.Contains(err.Error(), "service") {
		t.Errorf("refusal = %q, want it to name the remedy", err)
	}
}

// A capability that causes no device effect is not this adapter's business to
// gate, and gating it anyway would make the refusal above look like a general
// suspicion of the caller rather than a statement about one specific
// permission.
func TestACapabilityWithoutDeviceIsNotGatedOnResponsibility(t *testing.T) {
	session := fakeHelper(t, map[string]any{"list_apps": map[string]any{"apps": []any{}}})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plain := appsCapability(contract.EffectRead)
	if _, err := runner.Run(t.Context(), request(t, plain, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAnUnknownImplementationIsRefusedAtConstruction(t *testing.T) {
	_, err := desktop.New(desktop.Options{
		Implementations: []string{"macos.nonesuch"},
		Session:         fakeHelper(t, nil),
	})
	if err == nil {
		t.Fatal("an implementation nothing answers was accepted")
	}
}

func TestTheRunnerDeclaresWhatItCanActuallyDispatch(t *testing.T) {
	runner, err := desktop.New(desktop.Options{Session: fakeHelper(t, nil)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runner.ID() != "desktop" {
		t.Errorf("ID = %q", runner.ID())
	}
	// Implementations is what settings told it to answer for; Capabilities is
	// what its code can dispatch. The wiring above compares the two, so an
	// adapter that disagreed with itself would be caught at startup.
	if !runner.Serves(desktop.ImplementationListApps) {
		t.Errorf("does not serve its own implementation")
	}
	for _, want := range []string{desktop.CapabilityListApps, desktop.CapabilityInspect, desktop.CapabilityScreenshot} {
		if !slices.Contains(runner.Capabilities(), want) {
			t.Errorf("capabilities = %v, missing %s", runner.Capabilities(), want)
		}
	}
}

// A helper that answers something this adapter cannot read is a provider
// problem, not a caller's mistake, and the bin has to say so: sorted as
// invalid_input it would send somebody to re-check a payload that was fine.
func TestAnAnswerTheAdapterCannotReadIsAProviderFailure(t *testing.T) {
	session := fakeHelper(t, map[string]any{"list_apps": "not an object at all"})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(t.Context(), request(t, appsCapability(), nil))
	if err == nil {
		t.Fatal("a malformed answer was accepted")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("failure = %v, want unavailable", got)
	}
}

// A helper that will not start is the ordinary case on a machine where the
// binary was never built, and it must read as unavailable rather than as a
// crash: the difference is whether the funnel may try somebody else.
func TestAHelperThatCannotBeReachedIsUnavailable(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session: func(context.Context) (*mcpstdio.Session, error) {
			return nil, errors.New("no such file or directory")
		},
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(t.Context(), request(t, appsCapability(), nil))
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("failure = %v, want unavailable", got)
	}
}

func TestImplementationsIsWhatSettingsAskedFor(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Implementations: []string{desktop.ImplementationListApps},
		Session:         fakeHelper(t, nil),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := runner.Implementations()
	if len(got) != 1 || got[0] != desktop.ImplementationListApps {
		t.Fatalf("Implementations = %v", got)
	}
	// Handed out as a copy: a caller that sorted or truncated the returned
	// slice would otherwise be editing what the runner answers with.
	got[0] = "mutated"
	if runner.Implementations()[0] != desktop.ImplementationListApps {
		t.Error("the returned slice shares the runner's own")
	}
}

// Surface reports whose permissions would be used, not whether they are
// granted. On a shell-started process the honest sentence is that the terminal
// holds them and Atenea is standing behind it, and a status screen that said
// "granted" instead would be describing somebody else's authority as Atenea's.
func TestSurfaceSaysWhosePermissionsTheseWouldBe(t *testing.T) {
	for _, tc := range []struct {
		responsible bool
		want        string
	}{
		{true, "service:own-permissions"},
		{false, "shell:borrowed-permissions"},
	} {
		// The signature is pinned rather than read, so this test is about
		// responsibility alone. Left to the real check it would read this
		// test binary's own signature -- which is ad-hoc, because `go test`
		// builds it that way -- and fail on a machine rather than on a bug.
		runner, err := desktop.New(desktop.Options{
			Session:         fakeHelper(t, nil),
			Responsible:     func() bool { return tc.responsible },
			SignatureStable: func() (bool, string) { return true, "" },
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		reporter, ok := any(runner).(interface{ Surface() string })
		if !ok {
			t.Fatal("the runner no longer reports a surface")
		}
		if got := reporter.Surface(); got != tc.want {
			t.Errorf("responsible=%v surface = %q, want %q", tc.responsible, got, tc.want)
		}
	}
}

// A capability that needs no operating-system permission must not be gated on
// one. desktop.apps asks the window server what is running; refusing it for a
// missing Accessibility grant would deny work that functions perfectly, and
// would teach people to grant a permission they do not need.
func TestACapabilityThatNeedsNoPermissionIsNotGatedOnOne(t *testing.T) {
	session := fakeHelper(t, map[string]any{
		"list_apps": map[string]any{"apps": []any{}},
		"health": map[string]any{
			"accessibility": false, "screen_recording": false,
			"missing": "neither Accessibility nor Screen Recording is granted",
		},
	})
	runner, err := desktop.New(desktop.Options{
		Session:     session,
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(t.Context(), request(t, appsCapability(), nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// And it did not even ask: a capability needing nothing skips the round
	// trip rather than making it and ignoring the answer.
	params := <-callsSeen
	if name, _ := params["name"].(string); name != "list_apps" {
		t.Errorf("first call was %q, want list_apps with no health probe before it", name)
	}
}

func inspectCapability() contract.Capability {
	return contract.Capability{
		ID: desktop.CapabilityInspect, Version: contract.Version{Major: 1},
		Summary: "Read one application's accessibility tree.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectDevice},
		Inputs: []contract.Field{{Name: "application", Type: contract.TypeString, Required: true,
			Summary: "Bundle identifier."}},
		Outputs: []contract.Field{{Name: "count", Type: contract.TypeInt, Required: true,
			Summary: "How many nodes."}},
	}
}

func inspectRequest(t *testing.T, bundle string) contract.RunRequest {
	t.Helper()
	declared := inspectCapability()
	return contract.RunRequest{
		Capability:     declared,
		Implementation: contract.Implementation{ID: desktop.ImplementationInspect, Capability: declared.ID},
		Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
		Payload:        map[string]any{"application": bundle},
		Permission:     contract.Permission{Task: "read a window", Effects: declared.Effects},
	}
}

func inspectHelper(t *testing.T) func(context.Context) (*mcpstdio.Session, error) {
	return fakeHelper(t, map[string]any{
		"health": map[string]any{"accessibility": true, "screen_recording": true, "missing": ""},
		"list_apps": map[string]any{"apps": []any{map[string]any{
			"pid": 42, "name": "Notes", "bundle_id": "com.apple.Notes", "frontmost": true}}},
		"inspect": map[string]any{
			"nodes": []any{map[string]any{"role": "AXButton", "depth": 1,
				"app": "Notes", "bundle_id": "com.apple.Notes", "title": "New"}},
			"count": 1,
		},
	})
}

// The allow-list is the whole security posture of these two capabilities, and
// an empty one denies rather than permits. That inversion is the thing most
// likely to be "simplified" by somebody later, so it is pinned by name.
func TestAnApplicationNobodyAllowedIsRefused(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session:     inspectHelper(t),
		Responsible: func() bool { return true },
		Allowed:     nil, // the shipped posture
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(t.Context(), inspectRequest(t, "com.apple.Notes"))
	if err == nil {
		t.Fatal("an application on no allow-list was read")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("failure = %v, want permission_denied", got)
	}
	if !strings.Contains(err.Error(), "allow-list") {
		t.Errorf("refusal = %q, want it to name what is missing", err)
	}
}

// The wildcard reaches an application nobody named, which is what an operator
// asks for when they write it: drive whatever is on this desktop.
func TestTheWildcardReachesAnApplicationNobodyNamed(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session:     inspectHelper(t),
		Responsible: func() bool { return true },
		Allowed:     []string{desktop.AllApplications},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(t.Context(), inspectRequest(t, "com.apple.Notes")); err != nil {
		t.Fatalf("the wildcard refused an application: %v", err)
	}
}

// And it does not reach past Denied. This is the property that makes the
// widest allow-list survivable: the seeded password-manager list has to
// outrank the token that means "everything", or "everything" would quietly
// include the one place a single screenshot is a credential.
func TestTheWildcardDoesNotReachPastDenied(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session:     inspectHelper(t),
		Responsible: func() bool { return true },
		Allowed:     []string{desktop.AllApplications},
		Denied:      []string{"com.1password.1password"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(t.Context(), inspectRequest(t, "com.1password.1password"))
	if err == nil {
		t.Fatal("the wildcard read a denied application")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("failure = %v, want permission_denied", got)
	}
}

// Denied wins over allowed, because a settings file naming the same
// application in both is a mistake and the safe reading of a mistake is the
// refusal. Getting this backwards would turn the seeded password-manager list
// into decoration.
func TestDeniedBeatsAllowed(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session:     inspectHelper(t),
		Responsible: func() bool { return true },
		Allowed:     []string{"com.apple.Notes", "com.1password.1password"},
		Denied:      []string{"com.1password.1password"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(t.Context(), inspectRequest(t, "com.apple.Notes")); err != nil {
		t.Fatalf("an allowed application was refused: %v", err)
	}
	_, err = runner.Run(t.Context(), inspectRequest(t, "com.1password.1password"))
	if err == nil {
		t.Fatal("an application on both lists was read; denied must win")
	}
}

// The result says it is untrusted, and every node carries where it came from.
// Screen content is written by whoever controls the window; a caller that
// forgets that is one instruction away from acting on somebody else's text.
func TestInspectMarksItsResultUntrustedAndKeepsProvenance(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session:     inspectHelper(t),
		Responsible: func() bool { return true },
		Allowed:     []string{"com.apple.Notes"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := runner.Run(t.Context(), inspectRequest(t, "com.apple.Notes"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Result["untrusted"] != true {
		t.Error("the result does not declare itself untrusted")
	}
	nodes, ok := out.Result["nodes"].([]map[string]any)
	if !ok || len(nodes) == 0 {
		t.Fatalf("nodes = %+v", out.Result["nodes"])
	}
	if nodes[0]["bundle_id"] != "com.apple.Notes" {
		t.Errorf("node lost its provenance: %+v", nodes[0])
	}
}

// A pid is a number anybody can type. The adapter resolves the target through
// list_apps instead, so "which application" stops being the caller's claim.
func TestTheTargetIsResolvedRatherThanTakenFromTheCaller(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session:     inspectHelper(t),
		Responsible: func() bool { return true },
		Allowed:     []string{"com.apple.Notes"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(t.Context(), inspectRequest(t, "com.apple.Notes")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Three calls in order: health, because inspect declares it needs
	// Accessibility; list_apps, to resolve the target; then the walk itself.
	var args map[string]any
	for range 3 {
		params := <-callsSeen
		if name, _ := params["name"].(string); name == "inspect" {
			args, _ = params["arguments"].(map[string]any)
			break
		}
	}
	if args == nil {
		t.Fatal("the helper was never asked to inspect anything")
	}
	if pid, _ := args["pid"].(float64); int(pid) != 42 {
		t.Errorf("inspect was sent pid %v, want the one list_apps reported", args["pid"])
	}
	// And the ceilings the adapter set, which no caller supplied.
	for _, key := range []string{"budget_ms", "max_nodes", "max_bytes", "max_depth"} {
		if _, ok := args[key]; !ok {
			t.Errorf("the helper was sent no %s; a call with no ceiling is the one that hangs", key)
		}
	}
}

func screenshotCapability() contract.Capability {
	return contract.Capability{
		ID: desktop.CapabilityScreenshot, Version: contract.Version{Major: 1},
		Summary: "Capture one application's frontmost window.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectDevice},
		Inputs: []contract.Field{{Name: "application", Type: contract.TypeString, Required: true,
			Summary: "Bundle identifier."}},
		Outputs: []contract.Field{{Name: "width", Type: contract.TypeInt, Required: true,
			Summary: "Image width."}},
	}
}

// The Retina trap, pinned. A display holds twice the pixels it reports in
// points, and both vendors' computer-use APIs document the same failure:
// capture at 2x, forget to reduce, and every click lands at double the offset.
//
// The rule that keeps it out of here is that the helper reduces and reports
// what it did, and this side never multiplies anything. So what is asserted is
// an absence: whatever scale comes back, the dimensions pass through unchanged.
func TestScaleIsReportedAndCoordinatesAreNotTransformed(t *testing.T) {
	for _, tc := range []struct{ scale float64 }{{1.0}, {0.5}, {0.25}} {
		session := fakeHelper(t, map[string]any{
			"health": map[string]any{"accessibility": true, "screen_recording": true, "missing": ""},
			"list_apps": map[string]any{"apps": []any{map[string]any{
				"pid": 7, "name": "Notes", "bundle_id": "com.apple.Notes"}}},
			"screenshot": map[string]any{
				"png_base64": "iVBORw0KGgo=", "width": 1568, "height": 980,
				"scale": tc.scale, "bytes": 4096},
		})
		runner, err := desktop.New(desktop.Options{
			Session: session, Responsible: func() bool { return true },
			Allowed: []string{"com.apple.Notes"},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		declared := screenshotCapability()
		out, err := runner.Run(t.Context(), contract.RunRequest{
			Capability:     declared,
			Implementation: contract.Implementation{ID: desktop.ImplementationScreenshot, Capability: declared.ID},
			Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
			Payload:        map[string]any{"application": "com.apple.Notes"},
			Permission:     contract.Permission{Task: "look", Effects: declared.Effects},
		})
		if err != nil {
			t.Fatalf("scale %v: Run: %v", tc.scale, err)
		}
		if out.Result["width"] != 1568 || out.Result["height"] != 980 {
			t.Errorf("scale %v: dimensions were transformed: %v x %v",
				tc.scale, out.Result["width"], out.Result["height"])
		}
		// Reported as text because the contract has no float type. What
		// matters is that it is carried through, not that it arrives as a
		// number: nothing computes with it.
		if got := out.Result["scale"]; got != strconv.FormatFloat(tc.scale, 'g', -1, 64) {
			t.Errorf("scale = %v, want %v reported unchanged", got, tc.scale)
		}
	}
}

// A truncation nobody is told about is a lie by omission about what is on
// somebody's screen: the caller sees a tidy list and no reason to doubt it.
func TestATruncatedWalkSaysSo(t *testing.T) {
	session := fakeHelper(t, map[string]any{
		"health": map[string]any{"accessibility": true, "screen_recording": true, "missing": ""},
		"list_apps": map[string]any{"apps": []any{map[string]any{
			"pid": 7, "name": "Notes", "bundle_id": "com.apple.Notes"}}},
		"inspect": map[string]any{
			"nodes": []any{}, "count": 0, "truncated": "time budget reached"},
	})
	runner, err := desktop.New(desktop.Options{
		Session: session, Responsible: func() bool { return true },
		Allowed: []string{"com.apple.Notes"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := runner.Run(t.Context(), inspectRequest(t, "com.apple.Notes"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Result["truncated"] != "time budget reached" {
		t.Errorf("truncated = %v, want the reason carried through", out.Result["truncated"])
	}
}

// Nothing platform-shaped crosses the seam. The helper owns every macOS type
// and every pixel ratio; what arrives here is numbers and strings, and a
// result that started carrying something else would mean the scaling had
// leaked out of the one file that is allowed to know about it.
func TestNothingPlatformShapedCrossesTheSeam(t *testing.T) {
	session := fakeHelper(t, map[string]any{
		"health": map[string]any{"accessibility": true, "screen_recording": true, "missing": ""},
		"list_apps": map[string]any{"apps": []any{map[string]any{
			"pid": 7, "name": "Notes", "bundle_id": "com.apple.Notes"}}},
		"screenshot": map[string]any{"png_base64": "iVBOR", "width": 800,
			"height": 600, "scale": 0.5, "bytes": 12},
	})
	runner, err := desktop.New(desktop.Options{
		Session: session, Responsible: func() bool { return true },
		Allowed: []string{"com.apple.Notes"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	declared := screenshotCapability()
	out, err := runner.Run(t.Context(), contract.RunRequest{
		Capability:     declared,
		Implementation: contract.Implementation{ID: desktop.ImplementationScreenshot, Capability: declared.ID},
		Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
		Payload:        map[string]any{"application": "com.apple.Notes"},
		Permission:     contract.Permission{Task: "look", Effects: declared.Effects},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for key, value := range out.Result {
		switch value.(type) {
		case string, int, float64, bool:
		default:
			t.Errorf("%s carries %T across the seam; only plain values may", key, value)
		}
	}
}

// The warning that turns a silent failure into a visible one.
//
// An ad-hoc binary's TCC grant is pinned to its exact contents, so it dies on
// the next build -- while System Settings goes on showing the permission as
// granted, because the entry is still there. Somebody hits it as "this worked
// yesterday" with nothing anywhere to explain why. It is surfaced on the status
// screen because that is where they will already be looking.
func TestAnAdHocBinarySaysItsGrantWillNotSurvive(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stable bool
		want   string
	}{
		{"signed", true, "service:own-permissions"},
		{"ad-hoc", false, "service:own-permissions (ad-hoc: grant dies on next build)"},
	} {
		runner, err := desktop.New(desktop.Options{
			Session:         fakeHelper(t, nil),
			Responsible:     func() bool { return true },
			SignatureStable: func() (bool, string) { return tc.stable, "" },
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		reporter, ok := any(runner).(interface{ Surface() string })
		if !ok {
			t.Fatal("the runner no longer reports a surface")
		}
		if got := reporter.Surface(); got != tc.want {
			t.Errorf("%s: surface = %q, want %q", tc.name, got, tc.want)
		}
	}
}
