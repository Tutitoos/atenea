package contract_test

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func orchestratorCard() contract.Agent {
	return contract.Agent{
		ID:           "orchestrator",
		Type:         contract.AgentOrchestrator,
		Summary:      "Explores, splits and hands out.",
		Capabilities: []string{"code.search"},
		Context: []contract.ContextLevel{
			contract.ContextRepository,
			contract.ContextHistory,
		},
	}
}

func TestAgentCardAcceptsBothFamilies(t *testing.T) {
	// One contract covers both families; the field is what tells them apart.
	for _, kind := range []contract.AgentType{contract.AgentOrchestrator, contract.AgentSpecialist} {
		card := orchestratorCard()
		card.Type = kind
		if err := card.Validate(); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

func TestAgentCardRefusesIncompleteCards(t *testing.T) {
	cases := map[string]func(*contract.Agent){
		"uppercase id":       func(a *contract.Agent) { a.ID = "Orchestrator" },
		"no type":            func(a *contract.Agent) { a.Type = contract.AgentUnspecified },
		"no summary":         func(a *contract.Agent) { a.Summary = "  " },
		"no capability":      func(a *contract.Agent) { a.Capabilities = nil },
		"undotted capabilit": func(a *contract.Agent) { a.Capabilities = []string{"search"} },
		"empty level":        func(a *contract.Agent) { a.Context = []contract.ContextLevel{contract.ContextUnspecified} },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			card := orchestratorCard()
			break_(&card)
			err := card.Validate()
			if err == nil {
				t.Fatal("expected the card to be refused")
			}
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input", got)
			}
		})
	}
}

// An agent with no capability could never be given work, so a card that
// declares none is a card nobody would notice was useless.
func TestAgentCardRefusingNoCapabilityIsNotCosmetic(t *testing.T) {
	card := orchestratorCard()
	card.Capabilities = nil
	if err := card.Validate(); err == nil {
		t.Fatal("a card with no capability has to be refused")
	}
}

func TestAgentDeclarationIsAPermissionNotADelivery(t *testing.T) {
	card := orchestratorCard()
	// Declaring the history level says the agent MAY read it. It says nothing
	// about anything having been handed over.
	if !card.Sees(contract.ContextHistory) {
		t.Fatal("history was declared and is not seen")
	}
	if card.Sees(contract.ContextWorkspace) {
		t.Fatal("workspace was never declared")
	}
	if !card.CanAsk("code.search") {
		t.Fatal("code.search was declared and cannot be asked for")
	}
	if card.CanAsk("file.write") {
		t.Fatal("an undeclared capability must not be askable")
	}
}

func TestAgentCloneDoesNotShareSlices(t *testing.T) {
	original := orchestratorCard()
	clone := original.Clone()
	clone.Capabilities[0] = "file.write"
	clone.Context[0] = contract.ContextGlobal
	if original.Capabilities[0] != "code.search" {
		t.Fatal("clone shared the capability slice")
	}
	if original.Context[0] != contract.ContextRepository {
		t.Fatal("clone shared the context slice")
	}
}

func TestAgentTypeRoundTrip(t *testing.T) {
	for _, name := range []string{"orchestrator", "specialist"} {
		parsed, err := contract.ParseAgentType(name)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if parsed.String() != name {
			t.Fatalf("round trip: %q -> %v -> %q", name, parsed, parsed.String())
		}
	}
	if _, err := contract.ParseAgentType("reviewer"); err == nil {
		t.Fatal("an unknown agent type has to be refused")
	}
	// The empty string is the zero value, not a family, so it never parses.
	if _, err := contract.ParseAgentType(""); err == nil {
		t.Fatal("the empty string is not an agent type")
	}
}

func TestContextLevelRoundTrip(t *testing.T) {
	for _, name := range []string{"repository", "workspace", "global", "history"} {
		parsed, err := contract.ParseContextLevel(name)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if parsed.String() != name {
			t.Fatalf("round trip: %q -> %v -> %q", name, parsed, parsed.String())
		}
	}
	if _, err := contract.ParseContextLevel("execution"); err == nil {
		t.Fatal("there are four levels, and that is not one of them")
	}
}

func TestVerdictNames(t *testing.T) {
	cases := map[contract.Verdict]string{
		contract.VerdictUnspecified: "unjudged",
		contract.VerdictOK:          "ok",
		contract.VerdictFailed:      "failed",
	}
	for verdict, want := range cases {
		if got := verdict.String(); got != want {
			t.Errorf("%d = %q, want %q", verdict, got, want)
		}
	}
}

func TestVerdictRoundTrip(t *testing.T) {
	for _, name := range []string{"ok", "failed", "canceled"} {
		parsed, err := contract.ParseVerdict(name)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if parsed.String() != name {
			t.Fatalf("round trip: %q -> %v -> %q", name, parsed, parsed.String())
		}
	}
	if _, err := contract.ParseVerdict("passed"); err == nil {
		t.Fatal("an unknown verdict has to be refused")
	}
}
