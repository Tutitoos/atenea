package core

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The gate this exists for, stated once: everything Atenea ships in its own
// catalog has to be something the orchestrator can actually be asked for.
//
// symbol.search shipped for days without it. The capability was declared, an
// implementation was bound to it, the adapter had the code, the funnel picked
// the winner and printed the four stages it survived -- and every call came
// back "agent orchestrator may not ask for symbol.search". Nothing failed
// loudly enough to be noticed, because the only tests touching the card asked
// it about two capabilities somebody had chosen by hand.
//
// This one asks it about all of them, from the file that declares them.
func TestEveryShippedCapabilityIsAskable(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("config.Defaults: %v", err)
	}
	if len(cfg.Capabilities) == 0 {
		t.Fatal("the shipped catalog declares no capability: this test would pass on nothing")
	}
	card := orchestrator.Card()
	for _, capability := range cfg.Capabilities {
		if !card.CanAsk(capability.ID) {
			t.Errorf("the shipped catalog declares %s but agent %s may not ask for it: "+
				"add it to the card in internal/orchestrator", capability.ID, card.ID)
		}
	}
}

// The same invariant from the other side: a settings file that declares a
// capability nothing can ask for is stopped at the door, the way checkReach and
// checkDispatch stop their own impossible declarations.
func TestCheckAskableRefusesACapabilityTheCardDoesNotName(t *testing.T) {
	card := orchestrator.Card()
	caps := []contract.Capability{
		{ID: card.Capabilities[0]},
		{ID: "code.rewrite"},
	}
	err := checkAskable("test", caps, card)
	if err == nil {
		t.Fatal("checkAskable accepted code.rewrite: a capability the card cannot ask for " +
			"reaches clients as a tool that fails on every call")
	}
	if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
		t.Errorf("KindOf = %v, want invalid_input: it is the settings file that is wrong", kind)
	}
	// The message has to name the capability. A refusal that only says the
	// catalog is wrong sends the reader back to a file with two dozen blocks
	// in it to find out which one.
	if !strings.Contains(err.Error(), "code.rewrite") {
		t.Errorf("error %q does not name the offending capability", err)
	}
}

// A catalog the card covers passes, including the empty one. Without this the
// check could be a function that refuses everything and the test above would
// still be green.
func TestCheckAskableAcceptsACatalogTheCardCovers(t *testing.T) {
	card := orchestrator.Card()
	full := make([]contract.Capability, 0, len(card.Capabilities))
	for _, id := range card.Capabilities {
		full = append(full, contract.Capability{ID: id})
	}
	for name, caps := range map[string][]contract.Capability{
		"every capability the card names": full,
		"an empty catalog":                nil,
	} {
		if err := checkAskable("test", caps, card); err != nil {
			t.Errorf("checkAskable(%s) = %v, want nil", name, err)
		}
	}
}
