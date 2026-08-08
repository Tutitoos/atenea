package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A chat is the unit an MCP client gets, and tools/call is one capability
// against one repository -- which is orchestrator.Question, not a commission.
// Core.Ask exists and takes exactly that shape, but it is the console's door:
// it trusts the effects the caller hands it, because the caller standing at a
// terminal IS the user. A chat is not, and until a Session has its own Ask a
// client speaking for a chat has to go through the console's door and the
// isolation is not applied to a single tools/call.
//
// These tests are the two halves of that isolation, the same two Session.Do
// already has: what a chat may authorize going in, and what it may be told
// coming out.

// The grant is the ceiling, and it is checked before dispatch rather than at
// it. The gate on the dispatch path refuses a capability whose effects the
// COMMISSION does not cover -- but the commission here is built from what the
// caller asked for, so a chat that asked for write and was never granted it
// would sail through a gate that only ever compares a request with itself.
func TestAChatCannotAuthorizeAnEffectItWasNotGranted(t *testing.T) {
	atenea := build(t, catalog)
	defer func() { _ = atenea.Shutdown() }()

	chat, err := atenea.Open(core.SessionOptions{
		ID:      "reader",
		Client:  "test",
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, err = chat.Ask(context.Background(), orchestrator.Question{
		Capability: "code.search",
		Repository: "current",
		Payload:    map[string]any{"query": "func main"},
		Effects:    []contract.Effect{contract.EffectWrite},
	})
	if kind := contract.KindOf(err); kind != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied: err = %v", kind, err)
	}
	// Named, because a chat refused for holding too little has to be told what
	// it was short of. "Permission denied" alone sends the client back with
	// nothing to change.
	for _, want := range []string{"reader", "write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// Coming out, a chat is told only what it is entitled to know -- and Ask does
// that through the same withholding Do uses, not a copy of it. There is no test
// here that watches a fact being dropped on an Ask, and that absence is
// deliberate: an Ask's discoveries come from a runner reporting on its own
// answer, which no runner in this repository does yet, so such a test would pass
// whatever the code did. What is testable is that the two doors share one exit,
// which is why the withholding lives in Session.told and Do's own test exercises
// it. This test pins the half that IS reachable: an Ask returns a result whose
// discoveries have already been through the filter.
func TestAskReturnsNothingAChatMayNotRead(t *testing.T) {
	atenea := chats(t)

	chat, err := atenea.Open(core.SessionOptions{
		ID: "narrow", Client: "omp",
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	result, err := chat.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search",
		Repository: "work",
		Payload:    map[string]any{"query": "TODO"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, found := range result.Discoveries {
		if found.Level != contract.ContextRepository {
			t.Errorf("a %v fact reached a chat entitled to the repository only", found.Level)
		}
	}
}

// A commission counts against the chat that made it. A tools/call is one, so it
// counts too -- the Chats table on the status screen is the only place two
// clients at once is visible, and a column that stays at zero while work
// happens is worse than no column.
func TestAskCountsAgainstTheChatThatAsked(t *testing.T) {
	atenea := chats(t)

	chat, err := atenea.Open(core.SessionOptions{
		ID: "counter", Client: "omp",
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := chat.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search",
		Repository: "work",
		Payload:    map[string]any{"query": "TODO"},
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := chat.Runs(); got != 1 {
		t.Errorf("runs = %d, want 1: the ask did not count against the chat", got)
	}
}

// The count lives in memory and dies with the process. The receipt is where the
// attribution survives it, and "which chat asked for this" is the question the
// isolation exists to make answerable -- a shared history nobody can attribute
// is just a pile.
func TestAnAskIsAttributedToItsChatOnTheReceipt(t *testing.T) {
	atenea := chats(t)

	chat, err := atenea.Open(core.SessionOptions{
		ID: "attributed", Client: "omp",
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	result, err := chat.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search",
		Repository: "work",
		Payload:    map[string]any{"query": "TODO"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	run, err := atenea.Checkpoints().Load(result.RunID)
	if err != nil {
		t.Fatalf("reading the receipt back: %v", err)
	}
	if run.Session != "attributed" {
		t.Errorf("receipt session = %q, want %q: the run was written down as nobody's",
			run.Session, "attributed")
	}
}
