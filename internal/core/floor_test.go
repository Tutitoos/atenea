package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The standing grant is one line in the settings file and it reached everybody:
// the operator typing at a terminal and every client that connected, with no
// way for the file to say otherwise. Widening it for an afternoon's work was
// therefore a permanent widening of every chat, silently, and that is what
// these tests are here to stop.
//
// The floor is what a client-opened chat runs on, and the most one can be
// granted. It is deliberately not derived from the standing grant at dispatch:
// an absent key inherits it once, when the settings are read, and after that
// the two are separate numbers that happen to be equal.

// floors builds a core over a repository that really exists, so a question can
// be answered rather than merely planned. The orchestrator stanza is the
// parameter because that is the whole subject: the same catalog, the same
// repository, one line of policy different.
func floors(t *testing.T, policy string) *core.Core {
	t.Helper()
	repo := t.TempDir()
	body := "package main\n\n// TODO: the thing\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return build(t, fmt.Sprintf(`
contract = "3.0.0"

[core]
shutdown_grace = "2s"

[orchestrator]
runners = ["local"]
checkpoint_dir = %q
%s

  [orchestrator.local]
  implementations = ["ripgrep"]

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
# Faithful to the shipped catalog: code.search declares process because every
# implementation of it is a binary. A fixture that declared only read would be
# testing a capability this repository does not ship.
effects = ["read", "process"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

  [[capability.output]]
  name = "matches"
  type = "record_list"
  required = true

    [[capability.output.field]]
    name = "path"
    type = "string"
    required = true

    [[capability.output.field]]
    name = "line"
    type = "int"
    required = true

    [[capability.output.field]]
    name = "column"
    type = "int"
    required = true

    [[capability.output.field]]
    name = "snippet"
    type = "string"

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

  [implementation.health]
  state = "alive"

[[repository]]
id = "work"
path = %q
languages = ["go"]
scale = "small"
`, t.TempDir(), policy, repo))
}

// question is the same tools/call every one of these tests makes. It asks for
// nothing beyond reading, so whatever ends up on its permission came from a
// floor rather than from the request.
func question() orchestrator.Question {
	return orchestrator.Question{
		Capability: "code.search",
		Repository: "work",
		Payload:    map[string]any{"query": "TODO"},
	}
}

// granted returns the effects every step of a plan was stamped with, failing
// when they disagree. A commission splits into more than one step -- explore
// then search -- and reading only the first would let a floor apply to the
// cheap step and be forgotten on the one that does the work.
func granted(t *testing.T, result *orchestrator.Result) []contract.Effect {
	t.Helper()
	if result == nil || len(result.Plan.Steps) == 0 {
		t.Fatalf("result has no step to read a permission from: %+v", result)
	}
	first := result.Plan.Steps[0].Permission.Effects
	for _, step := range result.Plan.Steps[1:] {
		if !slices.Equal(step.Permission.Effects, first) {
			t.Fatalf("step %s was granted %v, step %s %v: one plan, two floors",
				result.Plan.Steps[0].ID, first, step.ID, step.Permission.Effects)
		}
	}
	return first
}

func openChat(t *testing.T, atenea *core.Core, grant ...contract.Effect) *core.Session {
	t.Helper()
	chat, err := atenea.Open(core.SessionOptions{
		ID: "client", Client: "test",
		Grant:   grant,
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return chat
}

// The defect, in one test. The operator widened their own floor to write; a
// client that connected afterwards used to inherit it without the settings file
// having any way to refuse. Both halves have to be checked on the same core, or
// a client refused because nothing was granted to anybody proves nothing.
func TestAClientDoesNotInheritTheOperatorsWiderFloor(t *testing.T) {
	atenea := floors(t, "effects = [\"process\", \"write\"]\nclient_effects = [\"process\"]")
	defer func() { _ = atenea.Shutdown() }()

	console, err := atenea.Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("console Ask: %v", err)
	}
	if got := granted(t, console); !slices.Contains(got, contract.EffectWrite) {
		t.Fatalf("console effects = %v, want write: the operator's own floor moved", got)
	}

	client, err := openChat(t, atenea).Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("client Ask: %v", err)
	}
	if got := granted(t, client); slices.Contains(got, contract.EffectWrite) {
		t.Errorf("client effects = %v, want no write: the client inherited the operator's floor", got)
	}
}

// Nothing breaks for a settings file written before the key existed: with no
// line about clients, a client runs on exactly what the operator does. This is
// the compatibility promise, and it is also the reason the status screen has to
// say when a floor was inherited -- inheriting is the case with the sharp edge.
func TestAnAbsentClientFloorIsTheOperatorFloor(t *testing.T) {
	atenea := floors(t, "effects = [\"process\", \"write\"]")
	defer func() { _ = atenea.Shutdown() }()

	result, err := openChat(t, atenea).Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := granted(t, result); !slices.Contains(got, contract.EffectWrite) {
		t.Errorf("effects = %v, want write: an absent key changed behavior", got)
	}
}

// A floor that can only narrow is a floor with a hidden ceiling in it. The
// operator may run a locked-down console and still hand clients more, and the
// file has to be able to say so.
func TestAClientFloorCanBeWiderThanTheOperators(t *testing.T) {
	atenea := floors(t, "effects = [\"process\"]\nclient_effects = [\"process\", \"write\"]")
	defer func() { _ = atenea.Shutdown() }()

	console, err := atenea.Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("console Ask: %v", err)
	}
	if got := granted(t, console); slices.Contains(got, contract.EffectWrite) {
		t.Fatalf("console effects = %v, want no write: the client floor leaked upwards", got)
	}

	client, err := openChat(t, atenea).Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("client Ask: %v", err)
	}
	if got := granted(t, client); !slices.Contains(got, contract.EffectWrite) {
		t.Errorf("client effects = %v, want write", got)
	}
}

// An empty list is a declaration and not an omission, which is the only reason
// read-only-for-clients is expressible at all. It costs the headline capability
// on this machine -- every implementation of code.search is a binary -- and
// that refusal is the honest consequence, not a bug: the operator asked for it
// in writing. It is not the default for exactly that reason.
func TestAnEmptyClientFloorLeavesOnlyReading(t *testing.T) {
	atenea := floors(t, "effects = [\"process\"]\nclient_effects = []")
	defer func() { _ = atenea.Shutdown() }()

	// A refused step is a verdict, not a transport error: the run happened and
	// its answer is "no". That is the shape a client sees, because the MCP
	// surface turns a failed step into a tool error.
	result, err := openChat(t, atenea).Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := granted(t, result); len(got) != 1 || got[0] != contract.EffectRead {
		t.Errorf("effects = %v, want read alone", got)
	}
	if len(result.Steps) != 1 || result.Steps[0].Review.Parent != contract.VerdictFailed {
		t.Fatalf("the step was not refused: %+v", result.Steps)
	}
	// Named, so the operator reading a client's complaint can find the line in
	// their own settings file that caused it.
	if failure := result.Steps[0].Failure; !strings.Contains(failure, "process") {
		t.Errorf("failure = %q, want it to name the effect that was short", failure)
	}

	// The console is unaffected on the same core, which is what proves the
	// empty list was read as a client policy rather than as a broken catalog.
	console, err := atenea.Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("console Ask: %v", err)
	}
	if len(console.Steps) != 1 || console.Steps[0].Review.Parent == contract.VerdictFailed {
		t.Errorf("the console was refused too: %+v", console.Steps)
	}
}

// The gate both grants rest on, exercised end to end from a settings file so
// it is not mistaken for a client rule: `commissioned` refuses any step whose
// permission does not carry an effect its capability declares, and it does so
// for the operator's own door exactly as it does for a chat's.
//
// This is what stops the client floor from being decoration. Narrowing a
// client to read would mean nothing if nothing compared a capability's
// declared effects against what was granted. The wrapper's own test covers the
// wrapper; this one covers the wiring reaching it from a real catalog, which
// is the part a settings key could silently bypass.
func TestACapabilityIsRefusedTheEffectNobodyGranted(t *testing.T) {
	atenea := floors(t, "")
	defer func() { _ = atenea.Shutdown() }()

	result, err := atenea.Ask(t.Context(), question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(result.Steps) != 1 || result.Steps[0].Review.Parent != contract.VerdictFailed {
		t.Fatalf("a capability needing process ran with no standing grant: %+v", result.Steps)
	}
	for _, want := range []string{"code.search", "process"} {
		if !strings.Contains(result.Steps[0].Failure, want) {
			t.Errorf("failure = %q, want it to name %q", result.Steps[0].Failure, want)
		}
	}
}

// The floor is the most a chat may hold, so a chat asking to be opened above it
// is refused at the door rather than at its first question. A client that
// learns this at initialize can say so; one that learns it mid-conversation has
// already told somebody the work was under way.
func TestAChatCannotOpenAboveTheClientFloor(t *testing.T) {
	atenea := floors(t, "effects = [\"process\", \"write\"]\nclient_effects = [\"process\"]")
	defer func() { _ = atenea.Shutdown() }()

	_, err := atenea.Open(core.SessionOptions{
		ID: "greedy", Client: "test",
		Grant:   []contract.Effect{contract.EffectWrite},
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if kind := contract.KindOf(err); kind != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied: err = %v", kind, err)
	}
	// Named for the same reason the entitled check names things: a client told
	// only "denied" has nothing to put in front of the person who has to fix it.
	for _, want := range []string{"greedy", "write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// A grant inside the floor is the ordinary case and must still work: the floor
// bounds a chat's ceiling, it does not replace the per-chat grant with itself.
func TestAChatMayStillBeGrantedInsideTheFloor(t *testing.T) {
	atenea := floors(t, "effects = [\"process\"]\nclient_effects = [\"process\", \"write\"]")
	defer func() { _ = atenea.Shutdown() }()

	chat := openChat(t, atenea, contract.EffectWrite)
	if !chat.Allows(contract.EffectWrite) {
		t.Error("the chat was opened inside the floor and still may not authorize write")
	}
}

// A chat has two doors and the floor has to be on both. `Session.Ask` is the
// one an MCP `tools/call` arrives at today, so it is the one every other test
// here uses -- which is exactly why this one exists. A commission from a chat
// goes through `Session.Do` and composes its permission somewhere else
// entirely, in Run rather than Ask, and a floor stamped on one and forgotten
// on the other would leave the wider door open with every test still green.
func TestACommissionFromAChatRunsOnTheClientFloorToo(t *testing.T) {
	atenea := floors(t, "effects = [\"process\", \"write\"]\nclient_effects = [\"process\"]")
	defer func() { _ = atenea.Shutdown() }()

	result, err := openChat(t, atenea).Do(t.Context(), orchestrator.Task{
		Text:         "TODO",
		Repositories: []string{"work"},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := granted(t, result); slices.Contains(got, contract.EffectWrite) {
		t.Errorf("effects = %v, want no write: the commission door skipped the floor", got)
	}
}
