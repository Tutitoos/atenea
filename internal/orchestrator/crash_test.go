package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// exploding is a far side with a bug in it. Not a provider that is down --
// that has a bin and a receipt -- but one that breaks an invariant halfway
// through, which is the shape of fault this notebook exists for.
type exploding struct{}

func (exploding) ID() string                { return "exploding" }
func (exploding) Serves(string) bool        { return true }
func (exploding) Implementations() []string { return []string{"fast", "slow"} }
func (exploding) Capabilities() []string    { return []string{"code.search"} }
func (exploding) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	panic("the runner reached a state it does not have a name for")
}

// childEnv is how the parent tells the re-executed test binary to be the one
// that crashes. A panic in a dispatch goroutine cannot be caught by the test
// that provoked it -- it takes the whole process -- so proving what it leaves
// behind means watching a real process die from outside.
const childEnv = "ATENEA_CRASH_CHILD"

// The end-to-end the notebook was built for: an internal fault kills the
// process, and the entry is on the disk anyway, naming what was being done.
//
// Nothing here is a double. A real agent dispatches a real step to a runner
// that really panics, in a process that really dies of it, and the parent
// reads the file afterwards with no help from the child.
func TestAPanickingStepIsOnDiskAfterTheProcessDies(t *testing.T) {
	if os.Getenv(childEnv) == "1" {
		crashOnPurpose(t)
		t.Fatal("the child was supposed to be dead by now")
	}

	state := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestAPanickingStepIsOnDiskAfterTheProcessDies")
	child.Env = append(os.Environ(), childEnv+"=1", "XDG_STATE_HOME="+state)
	output, err := child.CombinedOutput()

	if err == nil {
		t.Fatalf("the child survived a panic:\n%s", output)
	}
	if !strings.Contains(string(output), "does not have a name for") {
		t.Errorf("the child died of something else:\n%s", output)
	}

	// The parent opens the file itself. Whatever the child had in memory when
	// it died is not available to anybody, which is the point.
	path := filepath.Join(state, "atenea", notebook.FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the crash left no notebook at %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("the notebook holds %d lines, want the one crash:\n%s", len(lines), raw)
	}
	var in notebook.Incident
	if err := json.Unmarshal([]byte(lines[0]), &in); err != nil {
		t.Fatalf("the line is not an incident: %v (%q)", err, lines[0])
	}

	if in.Op != "orchestrator.step" {
		t.Errorf("op = %q", in.Op)
	}
	if in.Capability != "code.search" || in.Repository != "api" {
		t.Errorf("the entry does not say what was being asked: %+v", in)
	}
	if in.Step == "" || in.RunID == "" {
		t.Errorf("the entry cannot be tied back to a run: step=%q run=%q", in.Step, in.RunID)
	}
	if !strings.Contains(in.Detail, "does not have a name for") {
		t.Errorf("detail = %q", in.Detail)
	}
	if !strings.Contains(in.Stack, "orchestrator") {
		t.Errorf("the stack does not reach the dispatch:\n%s", in.Stack)
	}
	if in.PID == 0 || in.PID == os.Getpid() {
		t.Errorf("pid = %d, want the child's", in.PID)
	}
	// Names went in, the query did not.
	if strings.Join(in.Fields, ",") != "query" {
		t.Errorf("fields = %v, want just the name", in.Fields)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Error("the payload value reached the notebook")
	}
}

// crashOnPurpose is the child half: a real agent, a real dispatch, a runner
// that panics. It never returns.
func crashOnPurpose(t *testing.T) {
	t.Helper()
	book, err := notebook.New(notebook.DefaultPath())
	if err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	checkpoints, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     twins(t),
		Chooser:     chooser,
		Runner:      exploding{},
		Checkpoints: checkpoints,
		Notebook:    book,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	//nolint:errcheck // the process is about to die of the panic inside.
	_, _ = agent.Ask(context.Background(), orchestrator.Question{
		Capability: "code.search",
		Repository: "api",
		Payload:    map[string]any{"query": "hunter2"},
	})
}
