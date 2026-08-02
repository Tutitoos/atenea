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

// chats builds a core over a repository that really exists, because the
// isolation only shows itself on a run that produced something to withhold.
func chats(t *testing.T) *core.Core {
	t.Helper()
	repo := t.TempDir()
	body := "package main\n\n// TODO: the thing\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return build(t, fmt.Sprintf(`
contract = "1.0.0"

[core]
shutdown_grace = "2s"

[orchestrator]
runners = ["local"]
checkpoint_dir = %q

  [orchestrator.local]
  implementations = ["ripgrep"]

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
effects = ["read"]

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
`, t.TempDir(), repo))
}

// ---------------------------------------------------------------------------
// The permission ceiling
// ---------------------------------------------------------------------------

// A chat's grant is a ceiling, not a suggestion. Today the commission itself
// carries the effects it authorizes, so without a ceiling any client speaking
// for any chat could authorize anything by simply saying so.
func TestAChatCannotAuthorizeWhatItWasNotGranted(t *testing.T) {
	atenea := chats(t)
	chat, err := atenea.Open(core.SessionOptions{ID: "looker", Client: "omp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, err = chat.Do(t.Context(), orchestrator.Task{
		Text:    "TODO",
		Effects: []contract.Effect{contract.EffectWrite},
	})
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	if chat.Runs() != 0 {
		t.Errorf("a refused commission was counted as work: runs = %d", chat.Runs())
	}
}

func TestAChatCanAuthorizeWhatItHolds(t *testing.T) {
	atenea := chats(t)
	chat, err := atenea.Open(core.SessionOptions{
		ID:     "writer",
		Client: "claude-code",
		Grant:  []contract.Effect{contract.EffectWrite},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// The run itself may still fail on the way -- what must not happen is a
	// refusal at the gate for an effect the chat was handed.
	_, err = chat.Do(t.Context(), orchestrator.Task{
		Text:    "TODO",
		Effects: []contract.Effect{contract.EffectWrite},
	})
	if got := contract.KindOf(err); got == contract.FailurePermissionDenied {
		t.Fatalf("a granted effect was refused: %v", err)
	}
}

// Reading is free by default everywhere else in the design, and a chat that
// could not read could not do anything at all.
func TestAnEmptyGrantStillReads(t *testing.T) {
	atenea := chats(t)
	chat, err := atenea.Open(core.SessionOptions{ID: "fresh", Client: "omp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !chat.Allows(contract.EffectRead) {
		t.Error("a fresh chat cannot read, so it can do nothing at all")
	}
	if chat.Allows(contract.EffectExternal) {
		t.Error("a fresh chat can reach outside the machine")
	}
	if _, err := chat.Do(t.Context(), orchestrator.Task{Text: "TODO"}); err != nil {
		t.Fatalf("an ordinary search needs no grant: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The context entitlement
// ---------------------------------------------------------------------------

// What a chat is told is not what the run found. The run still discovers it
// and the history still records it; the chat is handed the heights it holds.
func TestAChatIsToldOnlyWhatItMayRead(t *testing.T) {
	atenea := chats(t)

	near, err := atenea.Open(core.SessionOptions{
		ID: "near", Client: "omp",
		Context: []contract.ContextLevel{contract.ContextRepository},
	})
	if err != nil {
		t.Fatalf("Open near: %v", err)
	}
	far, err := atenea.Open(core.SessionOptions{
		ID: "far", Client: "claude-code",
		Context: []contract.ContextLevel{contract.ContextGlobal},
	})
	if err != nil {
		t.Fatalf("Open far: %v", err)
	}

	inside, err := near.Do(t.Context(), orchestrator.Task{Text: "TODO"})
	if err != nil {
		t.Fatalf("near: %v", err)
	}
	if len(inside.Discoveries) == 0 {
		t.Fatal("a chat entitled to the repository level was told nothing about it")
	}
	for _, found := range inside.Discoveries {
		if found.Level != contract.ContextRepository {
			t.Errorf("a %v fact reached a chat entitled to the repository only", found.Level)
		}
	}

	outside, err := far.Do(t.Context(), orchestrator.Task{Text: "TODO"})
	if err != nil {
		t.Fatalf("far: %v", err)
	}
	if len(outside.Discoveries) != 0 {
		t.Errorf("a chat with no right to the repository level was told %d fact(s) about it",
			len(outside.Discoveries))
	}
	// The same commission, the same work: only the telling differed.
	if outside.Matches != inside.Matches {
		t.Errorf("withholding changed the work: %d matches vs %d",
			outside.Matches, inside.Matches)
	}
}

// The floor is shared on purpose. A fact withheld from one chat is still worth
// writing down, or the next commission pays to learn it again.
func TestWhatOneChatIsNotShownIsStillWrittenDown(t *testing.T) {
	atenea := chats(t)
	chat, err := atenea.Open(core.SessionOptions{
		ID: "blinkered", Client: "omp",
		Context: []contract.ContextLevel{contract.ContextGlobal},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	result, err := chat.Do(t.Context(), orchestrator.Task{Text: "TODO"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(result.Discoveries) != 0 {
		t.Fatalf("the fixture stopped withholding, so this proves nothing")
	}

	ids, err := atenea.Checkpoints().List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slices.Contains(ids, result.RunID) {
		t.Fatalf("the run is missing from the shared history: %v", ids)
	}
	run, err := atenea.Checkpoints().Load(result.RunID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if run.Session != "blinkered" {
		t.Errorf("the shared history cannot say whose run this was: %q", run.Session)
	}
	if len(run.Steps) == 0 {
		t.Error("the run was recorded with no steps, so nothing was learned")
	}
}

// ---------------------------------------------------------------------------
// Two clients at once
// ---------------------------------------------------------------------------

// The firebreak the whole brick exists to prove: omp and Claude Code open
// together, each with its own reach, and neither inheriting the other's.
func TestTwoClientsAtOnceDoNotShareTheirReach(t *testing.T) {
	atenea := chats(t)
	loose, err := atenea.Open(core.SessionOptions{
		ID: "omp-1", Client: "omp",
		Grant: []contract.Effect{contract.EffectWrite, contract.EffectExternal},
	})
	if err != nil {
		t.Fatalf("Open omp-1: %v", err)
	}
	tight, err := atenea.Open(core.SessionOptions{ID: "cc-1", Client: "claude-code"})
	if err != nil {
		t.Fatalf("Open cc-1: %v", err)
	}

	if !loose.Allows(contract.EffectExternal) {
		t.Error("the granted chat lost its own grant")
	}
	if tight.Allows(contract.EffectWrite) || tight.Allows(contract.EffectExternal) {
		t.Error("a chat inherited the reach of the one next to it")
	}

	// Widening the copy a caller is handed must not widen the chat.
	grant := loose.Grant()
	grant[0] = contract.EffectRead
	if !loose.Allows(contract.EffectWrite) {
		t.Error("the grant is handed out by reference, so a caller can edit it")
	}

	open := atenea.Sessions()
	if len(open) != 2 {
		t.Fatalf("sessions = %d, want both chats", len(open))
	}
	clients := []string{open[0].Client(), open[1].Client()}
	if !strings.Contains(strings.Join(clients, ","), "omp") ||
		!strings.Contains(strings.Join(clients, ","), "claude-code") {
		t.Errorf("the status screen cannot show two clients at once: %v", clients)
	}
}

// A chat that could take a name already in use would inherit that chat's
// grant by guessing it.
func TestAnOpenChatNameCannotBeTaken(t *testing.T) {
	atenea := chats(t)
	if _, err := atenea.Open(core.SessionOptions{ID: "shared", Client: "omp"}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err := atenea.Open(core.SessionOptions{
		ID: "shared", Client: "claude-code",
		Grant: []contract.Effect{contract.EffectExternal},
	})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

func TestAClosedChatFreesItsName(t *testing.T) {
	atenea := chats(t)
	first, err := atenea.Open(core.SessionOptions{ID: "seat", Client: "omp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first.Close()
	if len(atenea.Sessions()) != 0 {
		t.Fatal("a closed chat is still listed as open")
	}
	if _, err := atenea.Open(core.SessionOptions{ID: "seat", Client: "omp"}); err != nil {
		t.Fatalf("the name was not freed: %v", err)
	}
}

// A client with no id of its own still gets isolation, so the id is minted
// rather than left empty and shared by everyone who omitted it.
func TestAChatWithoutANameStillGetsOne(t *testing.T) {
	atenea := chats(t)
	first, err := atenea.Open(core.SessionOptions{Client: "omp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	second, err := atenea.Open(core.SessionOptions{Client: "omp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if first.ID() == "" || first.ID() == second.ID() {
		t.Fatalf("two anonymous chats share one identity: %q and %q", first.ID(), second.ID())
	}
}

// ---------------------------------------------------------------------------
// The lifecycle
// ---------------------------------------------------------------------------

// Handing out a session during a shutdown is a promise the core is about to
// break, for the same reason accepting work is.
func TestNoChatIsLetInDuringAShutdown(t *testing.T) {
	atenea := chats(t)
	if _, err := atenea.Open(core.SessionOptions{ID: "early", Client: "omp"}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_, err := atenea.Open(core.SessionOptions{ID: "late", Client: "omp"})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable", contract.KindOf(err))
	}
	if got := len(atenea.Sessions()); got != 0 {
		t.Errorf("%d chat(s) still listed as open after a clean stop", got)
	}
}

func TestABadGrantIsRefusedAtTheDoor(t *testing.T) {
	atenea := chats(t)
	_, err := atenea.Open(core.SessionOptions{
		ID: "bogus", Client: "omp",
		Grant: []contract.Effect{contract.Effect(99)},
	})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
	_, err = atenea.Open(core.SessionOptions{
		ID: "blank", Client: "omp",
		Context: []contract.ContextLevel{contract.ContextUnspecified},
	})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// The status screen is the only place anybody sees the isolation, so what it
// reports has to be the chat's real reach.
func TestTheStatusScreenShowsEveryOpenChat(t *testing.T) {
	atenea := chats(t)
	if _, err := atenea.Open(core.SessionOptions{
		ID: "visible", Client: "claude-code",
		Grant:   []contract.Effect{contract.EffectWrite},
		Context: []contract.ContextLevel{contract.ContextRepository, contract.ContextHistory},
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	status := atenea.Status()
	if len(status.Chats) != 1 {
		t.Fatalf("chats on screen = %d, want 1", len(status.Chats))
	}
	chat := status.Chats[0]
	if chat.ID != "visible" || chat.Client != "claude-code" {
		t.Errorf("chat = %+v", chat)
	}
	if strings.Join(chat.Grant, ",") != "write" {
		t.Errorf("grant on screen = %v", chat.Grant)
	}
	if strings.Join(chat.Context, ",") != "repository,history" {
		t.Errorf("context on screen = %v", chat.Context)
	}
}
