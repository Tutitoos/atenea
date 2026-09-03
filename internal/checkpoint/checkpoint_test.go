package checkpoint_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func run(id string) checkpoint.Run {
	task := "find every TODO"
	return checkpoint.Run{
		ID:                      id,
		Kind:                    checkpoint.KindTask,
		Session:                 "chat-1",
		SessionClient:           "codex",
		SessionName:             "Improve dashboard",
		SessionNameBasis:        "provided",
		SessionPrimaryProject:   "atenea",
		SessionOriginSurface:    "codex",
		SessionOriginTransport:  "mcp-stdio",
		SessionExternalObserved: true,
		Task:                    task,
		Repositories:            []string{"api"},
		Effects:                 []contract.Effect{contract.EffectWrite},
		BudgetUSD:               0.5,
		ContractVersion:         contract.Current.String(),
		Started:                 time.Now(),
		Plan: contract.Plan{Task: task, Steps: []contract.Step{{
			ID:         "explore-api",
			Capability: "code.search",
			Repository: "api",
			Payload:    map[string]any{"query": task},
			Permission: contract.Permission{Task: task, Effects: []contract.Effect{contract.EffectRead}},
		}}},
		Steps: []checkpoint.StepState{{
			ID:             "explore-api",
			Capability:     "code.search",
			Repository:     "api",
			Implementation: "ripgrep",
			Verdict:        "ok",
			Review:         "ok",
			DurationMS:     12,
			Discoveries: []contract.Discovery{
				{Level: contract.ContextRepository, Note: "api: 3 hit(s) for \"find every TODO\""},
			},
		}},
	}
}

func TestRoundTripKeepsWhatIsNeededToPickUpAgain(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	original := run("20260802T120000-abc123")
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	read, err := store.Load(original.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if read.Task != original.Task {
		t.Errorf("task = %q, want %q", read.Task, original.Task)
	}
	if read.Kind != checkpoint.KindTask {
		t.Errorf("kind = %q, want %q", read.Kind, checkpoint.KindTask)
	}
	if read.SessionName != "Improve dashboard" || read.SessionNameBasis != "provided" || read.SessionPrimaryProject != "atenea" || read.SessionOriginSurface != "codex" || read.SessionOriginTransport != "mcp-stdio" || !read.SessionExternalObserved {
		t.Errorf("session metadata did not survive: %+v", read)
	}
	if len(read.Steps) != 1 || read.Steps[0].ID != "explore-api" {
		t.Fatalf("steps did not survive the round trip: %v", read.Steps)
	}
	if read.Steps[0].Implementation != "ripgrep" {
		t.Error("which implementation ran is exactly what a resumed run needs")
	}
	if len(read.Steps[0].Discoveries) != 1 || read.Steps[0].Discoveries[0].Note != "api: 3 hit(s) for \"find every TODO\"" {
		t.Errorf("discoveries did not survive the round trip: %+v", read.Steps[0].Discoveries)
	}
	// The rest is what a resumed run rebuilds its exploration and its grant
	// from, and what it dispatches straight out of without replanning.
	if len(read.Repositories) != 1 || read.Repositories[0] != "api" {
		t.Errorf("repositories = %v, want [api]", read.Repositories)
	}
	if len(read.Effects) != 1 || read.Effects[0] != contract.EffectWrite {
		t.Errorf("effects = %v, want [write]", read.Effects)
	}
	if read.BudgetUSD != original.BudgetUSD {
		t.Errorf("budget_usd = %v, want %v", read.BudgetUSD, original.BudgetUSD)
	}
	if read.ContractVersion != contract.Current.String() {
		t.Errorf("contract_version = %q, want %q", read.ContractVersion, contract.Current.String())
	}
	if len(read.Plan.Steps) != 1 || read.Plan.Steps[0].ID != "explore-api" {
		t.Fatalf("plan did not survive the round trip: %v", read.Plan)
	}
	if read.Plan.Steps[0].Permission.Effects[0] != contract.EffectRead {
		t.Errorf("plan step permission = %v, want read", read.Plan.Steps[0].Permission)
	}
}

// A resumed run's failure display reads Failure and Raw off the dumped
// StepState (see cmd/atenea/main.go's cmdResume), so both have to survive the
// same disk round trip as everything else on the step.
func TestRoundTripKeepsTheFailureAndItsRawText(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	failed := run("20260802T130000-def456")
	failed.Closed = true
	failed.Verdict = "failed"
	failed.Steps[0].Verdict = "failed"
	failed.Steps[0].Review = ""
	failed.Steps[0].Failure = "serena did not answer"
	failed.Steps[0].Raw = "no symbol matching 'Frame/consistent' found"
	if err := store.Save(failed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	read, err := store.Load(failed.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(read.Steps) != 1 {
		t.Fatalf("steps did not survive the round trip: %v", read.Steps)
	}
	if read.Steps[0].Failure != "serena did not answer" {
		t.Errorf("failure = %q, want the summarized reason", read.Steps[0].Failure)
	}
	if read.Steps[0].Raw != "no symbol matching 'Frame/consistent' found" {
		t.Errorf("raw = %q, want the provider's own text", read.Steps[0].Raw)
	}
}

func TestSaveRedactsProviderTextOnlyInTheDurableCopy(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	record := run("20260802T131000-redact")
	record.Steps[0].Raw = "Authorization: Bearer live-token"
	if err := store.Save(record); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(record.Steps[0].Raw, "live-token") {
		t.Fatal("Save mutated the in-memory provider evidence")
	}
	read, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Contains(read.Steps[0].Raw, "live-token") || !strings.Contains(read.Steps[0].Raw, "[REDACTED]") {
		t.Fatalf("durable raw = %q, want redacted provider output", read.Steps[0].Raw)
	}
}

// A dump replaces the previous one: the paper copy is the current state, not
// an append-only log.
func TestSaveReplacesTheEarlierDump(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := run("20260802T120000-abc123")
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	second := first
	second.Closed = true
	second.Verdict = "ok"
	if err := store.Save(second); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("run files = %d, want 1", len(ids))
	}
	read, err := store.Load(first.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !read.Closed || read.Verdict != "ok" {
		t.Error("the second dump did not win")
	}
}

// A dump interrupted halfway is worse than no dump: it reads as a valid record
// of a run that never happened that way. Nothing partial may be left behind.
func TestNoTemporaryFilesSurviveASave(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Save(run("20260802T120000-abc123")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			t.Errorf("left behind %s", entry.Name())
		}
	}
}

// Steps close in parallel and each one asks for a dump.
func TestConcurrentSavesLeaveAReadableFile(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record := run("20260802T120000-abc123")
			record.Steps[0].DurationMS = int64(i)
			if err := store.Save(record); err != nil {
				t.Errorf("Save: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := store.Load("20260802T120000-abc123"); err != nil {
		t.Fatalf("the file left behind is not readable: %v", err)
	}
}

// A core that never receives a commission should not leave empty folders on
// the machine.
func TestTheDirectoryAppearsOnlyWhenThereIsSomethingToWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the directory was created before there was anything to put in it")
	}
	if err := store.Save(run("20260802T120000-abc123")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the directory was not created on the first dump: %v", err)
	}
}

func TestCheckpointingOffDropsEveryDump(t *testing.T) {
	store, err := checkpoint.New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if store.Enabled() {
		t.Fatal("an empty directory means checkpointing is off")
	}
	if err := store.Save(run("20260802T120000-abc123")); err != nil {
		t.Fatalf("a disabled store has to drop silently, got %v", err)
	}
	if _, err := store.Load("20260802T120000-abc123"); err == nil {
		t.Fatal("a disabled store has nothing to load")
	}
	ids, err := store.List()
	if err != nil || len(ids) != 0 {
		t.Fatalf("List = %v, %v; want empty and no error", ids, err)
	}
}

// The id becomes a file name, so anything that could climb out of the
// directory has to be refused before it is joined onto a path.
func TestUnsafeRunIdsAreRefused(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, id := range []string{"../escape", "a/b", "", "with space"} {
		record := run(id)
		if err := store.Save(record); err == nil {
			t.Errorf("Save accepted the unsafe id %q", id)
		} else if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("Save(%q) kind = %v, want invalid_input", id, got)
		}
		if _, err := store.Load(id); err == nil {
			t.Errorf("Load accepted the unsafe id %q", id)
		}
	}
}

func TestLoadingAnUnknownRunIsNotFound(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = store.Load("20260802T120000-abc123")
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

// Two runs started in the same second must not overwrite each other.
func TestNewIDIsUniqueWithinASecond(t *testing.T) {
	now := time.Now()
	seen := make(map[string]struct{}, 128)
	for range 128 {
		id := checkpoint.NewID(now)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate run id %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestDefaultDirFollowsTheStateConvention(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	if got := checkpoint.DefaultDir(); got != filepath.Join("/tmp/state", "atenea", "runs") {
		t.Fatalf("DefaultDir = %q", got)
	}
}
