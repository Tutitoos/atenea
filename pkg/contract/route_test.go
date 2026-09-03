package contract_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A route exists so a decision survives being handed to somebody else, so the
// shape it must refuse is the one that reads as a decision and resolves to
// nothing: a named backend with no model says which machinery runs the turn
// and stays silent about what it runs, and the launched agent fills that
// silence from whatever the machine's defaults say today -- the fallback Route
// was added to stop.
func TestARouteNamingABackendMustNameItsModel(t *testing.T) {
	silent := contract.Route{Backend: "opencode"}
	if err := silent.Validate(); err == nil {
		t.Fatal("a backend with no model was accepted as a recorded decision")
	}
	if got := contract.KindOf(silent.Validate()); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}

	// A model with no backend is fine: the direct agent path names the model
	// and lets the runner decide how to reach it.
	named := contract.Route{Model: "claude-opus-4"}
	if err := named.Validate(); err != nil {
		t.Fatalf("a model with no backend was refused: %v", err)
	}
}

// Everything a route lists is a name something later has to resolve. An empty
// entry resolves to nothing and a capability id the catalog could not have
// declared resolves to nothing either, so both are a decision about nothing
// wearing the shape of a decision.
func TestARouteRefusesNamesNothingCanResolve(t *testing.T) {
	cases := map[string]contract.Route{
		"an empty tool":            {Tools: []string{"read", ""}},
		"an empty fallback":        {Model: "a", Fallbacks: []string{""}},
		"an undotted capability":   {Capabilities: []string{"search"}},
		"an uppercase capability":  {Capabilities: []string{"Code.Search"}},
		"a provider keyed by junk": {Providers: map[string]string{"search": "ripgrep"}},
		"a capability routed nowhere": {
			Providers: map[string]string{"code.search": "  "},
		},
	}
	for name, route := range cases {
		t.Run(name, func(t *testing.T) {
			if err := route.Validate(); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}

	whole := contract.Route{
		Model:        "claude-opus-4",
		Fallbacks:    []string{"claude-sonnet-4"},
		Backend:      "claude",
		Binary:       "/usr/local/bin/claude",
		Capabilities: []string{"code.search", "symbol.definition"},
		Providers:    map[string]string{"code.search": "ripgrep"},
		Tools:        []string{"read", "grep"},
	}
	if err := whole.Validate(); err != nil {
		t.Fatalf("a fully resolved route was refused: %v", err)
	}
}

// Route rode on the assignment with a Clone and no Validate, so the one field
// on the card that exists to stop a silent fallback was the one field nothing
// checked. A route is only worth carrying if the card it arrives on is refused
// when the route is unusable.
func TestAnUnusableRouteIsRefusedWithTheCardItRodeIn(t *testing.T) {
	card := testRoot(t)
	card.Route = &contract.Route{Backend: "opencode"}

	err := card.Validate()
	if err == nil {
		t.Fatal("a card carrying an unusable route validated")
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
	if !strings.Contains(err.Error(), card.ID) {
		t.Fatalf("error %q does not name the card the route arrived on", err)
	}

	card.Route = &contract.Route{Backend: "opencode", Model: "grok-code"}
	if err := card.Validate(); err != nil {
		t.Fatalf("a card carrying a resolved route was refused: %v", err)
	}
}

// A cloned route must survive validation unchanged and share nothing: the
// route is the record of a decision, and two cards editing one map is two runs
// disagreeing about which provider was chosen.
func TestRouteCloneDoesNotShareState(t *testing.T) {
	original := contract.Route{
		Model:        "claude-opus-4",
		Fallbacks:    []string{"claude-sonnet-4"},
		Capabilities: []string{"code.search"},
		Providers:    map[string]string{"code.search": "ripgrep"},
		Tools:        []string{"read"},
	}
	copied := original.Clone()
	if err := copied.Validate(); err != nil {
		t.Fatalf("the clone of a valid route does not validate: %v", err)
	}

	copied.Fallbacks[0] = "something-else"
	copied.Capabilities[0] = "symbol.definition"
	copied.Tools[0] = "write"
	copied.Providers["code.search"] = "fixture"

	if original.Fallbacks[0] != "claude-sonnet-4" {
		t.Fatal("clone shares the fallbacks array")
	}
	if original.Capabilities[0] != "code.search" {
		t.Fatal("clone shares the capabilities array")
	}
	if original.Tools[0] != "read" {
		t.Fatal("clone shares the tools array")
	}
	if original.Providers["code.search"] != "ripgrep" {
		t.Fatal("clone shares the providers map")
	}
}
