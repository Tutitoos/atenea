package desktop_test

import (
	"slices"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/desktop"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func openRequest(t *testing.T) contract.RunRequest {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cfg.Capabilities, func(c contract.Capability) bool { return c.ID == desktop.CapabilityOpen })
	if i < 0 {
		t.Fatal("desktop.open is not declared")
	}
	req := request(t, cfg.Capabilities[i], map[string]any{"application": "com.apple.calculator"})
	req.Implementation.ID = desktop.ImplementationOpen
	return req
}

func openAnswer() map[string]any {
	return map[string]any{"action_sent": true, "application": "Calculator", "bundle_id": "com.apple.calculator", "pid": 42, "frontmost": true}
}

func TestOpenResolvesClosedAppsWithoutTrustingCallerTargets(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Session: fakeHelper(t, map[string]any{
			"health": map[string]any{"accessibility": true}, "open": openAnswer(),
		}),
		Allowed: []string{}, ActionAllowed: []string{"com.apple.calculator"},
		Responsible: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(t.Context(), openRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result["frontmost"] != true || out.Result["pid"] != 42 || out.Result["did"] != desktop.CapabilityOpen {
		t.Fatalf("unverified result: %v", out.Result)
	}
	if !out.SpentUSDKnown || out.SpentUSD != 0 {
		t.Fatalf("local open cost is not known zero: %+v", out)
	}
	for len(callsSeen) > 0 {
		call := <-callsSeen
		if call["name"] == "health" {
			continue
		}
		args := call["arguments"].(map[string]any)
		if call["name"] != "open" || len(args) != 2 || args["bundle_id"] != "com.apple.calculator" || args["visual_feedback"] != false {
			t.Fatalf("unexpected helper request: %v", call)
		}
	}
}

func TestOpenEnforcesIndependentActionAllowListAndDeniedPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		allowed, actions, denied []string
	}{
		{"read-only", []string{"*"}, []string{}, nil},
		{"unapproved", []string{"*"}, []string{"com.apple.finder"}, nil},
		{"denied-wins", []string{"*"}, []string{"*"}, []string{"com.apple.calculator"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := desktop.New(desktop.Options{
				Session: fakeHelper(t, map[string]any{"health": map[string]any{"accessibility": true}}),
				Allowed: tc.allowed, ActionAllowed: tc.actions, Denied: tc.denied,
				Responsible: func() bool { return true },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), openRequest(t))
			if contract.KindOf(err) != contract.FailurePermissionDenied {
				t.Fatalf("error = %v", err)
			}
			for len(callsSeen) > 0 {
				if call := <-callsSeen; call["name"] != "health" {
					t.Fatalf("denied target reached helper: %v", call)
				}
			}
		})
	}
}

func TestOpenRefusesUnconfirmedOrMismatchedReceiptsWithoutRetry(t *testing.T) {
	for _, field := range []string{"action_sent", "application", "bundle_id", "pid", "frontmost"} {
		t.Run(field, func(t *testing.T) {
			answer := openAnswer()
			delete(answer, field)
			if field == "bundle_id" {
				answer[field] = "com.apple.finder"
			}
			runner, err := desktop.New(desktop.Options{
				Session:       fakeHelper(t, map[string]any{"health": map[string]any{"accessibility": true}, "open": answer}),
				ActionAllowed: []string{"*"}, Responsible: func() bool { return true },
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = runner.Run(t.Context(), openRequest(t)); contract.KindOf(err) != contract.FailureUnavailable {
				t.Fatalf("error = %v", err)
			}
			opens := 0
			for len(callsSeen) > 0 {
				if (<-callsSeen)["name"] == "open" {
					opens++
				}
			}
			if opens != 1 {
				t.Fatalf("open requests = %d, want exactly one", opens)
			}
		})
	}
}
