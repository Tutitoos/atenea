package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stub writes a fake agent: a shell script that answers whatever the test
// tells it to answer. It stands in for a model harness for the same reason
// the Claude Code adapter's stub does -- everything this package is about
// happens between the spawn and the report, and none of it needs a real
// model to be exercised. Real model runs are done by hand.
func stub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return path
}

// answers builds a stub that reads its assignment and prints one report.
func answers(t *testing.T, report string) string {
	t.Helper()
	return stub(t, "cat >/dev/null\ncat <<'REPORT'\n"+report+"\nREPORT")
}

func declared(command string, args ...string) config.AgentType {
	return config.AgentType{
		Spec: contract.AgentTypeSpec{
			Name: "reader",
			Kind: contract.AgentSpecialized,
			Result: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true, Summary: "what was read"},
			},
		},
		Summary: "a fake agent that answers fixed output",
		Command: command,
		Args:    args,
		Context: []contract.ContextLevel{contract.ContextRepository},
		Effects: []contract.Effect{contract.EffectRead},
		Limits:  contract.Limits{MaxDuration: 10 * time.Second, MaxTokens: 100},
	}
}

func runner(t *testing.T, declared config.AgentType) (*agent.Runner, *trace.Store) {
	t.Helper()
	ctx := t.Context()
	store, err := trace.Open(ctx, filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r, err := agent.New(agent.Options{
		Types:     []config.AgentType{declared},
		Store:     store,
		Self:      "/nonexistent/atenea",
		Workspace: agent.Workspace{RepositoryID: "current", RepositoryRoot: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, store
}

func task() contract.Task {
	return contract.Task{Objective: "read one file", Criterion: "it answers the declared shape"}
}

// A report on stdout is the answer, and the row closes with the verdict the
// agent itself reached.
func TestFinishedAgentAnswers(t *testing.T) {
	r, store := runner(t, declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`)))
	report, assignment, err := r.Run(t.Context(), "reader", task(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", report.Verdict)
	}
	if report.Result["path"] != "a.txt" {
		t.Fatalf("result = %v, want path a.txt", report.Result)
	}
	rows, err := store.List(t.Context(), trace.Filter{ID: assignment.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Verdict != contract.VerdictOK || rows[0].EndedAt.IsZero() {
		t.Fatalf("trace row = %+v, want one closed ok row", rows)
	}
}

// The distinction this package exists for: exiting clean is not answering.
// An agent that says nothing has died, however politely it exited.
func TestCleanExitWithoutAReportIsADeath(t *testing.T) {
	r, store := runner(t, declared(stub(t, "cat >/dev/null\nexit 0")))
	report, assignment, err := r.Run(t.Context(), "reader", task(), nil)
	if err == nil {
		t.Fatal("Run: want an error for an agent that answered nothing")
	}
	if report.Verdict != contract.VerdictIncomplete {
		t.Fatalf("verdict = %v, want incomplete", report.Verdict)
	}
	if report.Reason.Kind == contract.FailureUnspecified {
		t.Fatalf("reason = %+v, want a bin naming what happened", report.Reason)
	}
	rows, _ := store.List(t.Context(), trace.Filter{ID: assignment.ID})
	if len(rows) != 1 || rows[0].Verdict != contract.VerdictIncomplete {
		t.Fatalf("trace row = %+v, want one incomplete row", rows)
	}
	if rows[0].Swept {
		t.Fatal("a run that reported its own incompleteness must not read as swept")
	}
}

// A non-zero exit with no report is the same absence of an answer, and the
// reason has to carry what the process said on the way out -- that text is
// the only thing an operator has to go on.
func TestFailedExitKeepsStderr(t *testing.T) {
	r, _ := runner(t, declared(stub(t, "cat >/dev/null\necho 'the model refused' >&2\nexit 3")))
	report, _, err := r.Run(t.Context(), "reader", task(), nil)
	if err == nil {
		t.Fatal("Run: want an error for a failed exit")
	}
	if report.Verdict != contract.VerdictIncomplete {
		t.Fatalf("verdict = %v, want incomplete", report.Verdict)
	}
	if !strings.Contains(report.Reason.Text, "the model refused") {
		t.Fatalf("reason %q does not carry what the process said", report.Reason.Text)
	}
}

// An agent past its ceiling is a timeout, not a failure: nobody judged the
// work, the clock ran out.
func TestPastItsCeilingIsATimeout(t *testing.T) {
	spec := declared(stub(t, "cat >/dev/null\nsleep 30"))
	spec.Limits.MaxDuration = 200 * time.Millisecond
	r, _ := runner(t, spec)

	start := time.Now()
	report, _, err := r.Run(t.Context(), "reader", task(), nil)
	if err == nil {
		t.Fatal("Run: want an error for an agent that never answered")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the ceiling took %s to fire; it is not bounding anything", took)
	}
	if report.Reason.Kind != contract.FailureTimeout {
		t.Fatalf("reason kind = %v, want timeout", report.Reason.Kind)
	}
}

// A well-formed answer in a shape nobody declared is refused at the boundary.
// Incomplete, not failed: the run got somewhere and stopped short.
func TestAnswerInTheWrongShapeIsRefused(t *testing.T) {
	r, _ := runner(t, declared(answers(t, `{"result":{"nope":1},"verdict":"ok"}`)))
	report, _, err := r.Run(t.Context(), "reader", task(), nil)
	if err == nil {
		t.Fatal("Run: want an error for an undeclared result field")
	}
	if report.Verdict != contract.VerdictIncomplete {
		t.Fatalf("verdict = %v, want incomplete", report.Verdict)
	}
	if !strings.Contains(report.Reason.Text, "nope") {
		t.Fatalf("reason %q does not name the field that was refused", report.Reason.Text)
	}
}

// Garbage on stdout is not an answer either, and the failure has to say so
// rather than pass a zero report off as a result.
func TestUnparseableOutputIsADeath(t *testing.T) {
	r, _ := runner(t, declared(answers(t, "not json at all")))
	_, _, err := r.Run(t.Context(), "reader", task(), nil)
	if err == nil {
		t.Fatal("Run: want an error for output that is not a report")
	}
}

// Only the declared levels are sent. A level nobody asked for is absent from
// the payload rather than present and empty, so an agent cannot read a blank
// field as a fact about the world.
func TestOnlyDeclaredContextLevelsAreServed(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "assignment.json")
	spec := declared(stub(t, "cat >"+captured+"\ncat <<'REPORT'\n"+
		`{"result":{"path":"a.txt"},"verdict":"ok"}`+"\nREPORT"))
	r, _ := runner(t, spec)
	if _, _, err := r.Run(t.Context(), "reader", task(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("reading what the agent was handed: %v", err)
	}
	var payload struct {
		Context map[string]any `json:"context"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("the assignment is not json: %v", err)
	}
	if _, ok := payload.Context["repository"]; !ok {
		t.Fatalf("context = %v, want the declared repository level", payload.Context)
	}
	for _, absent := range []string{"workspace", "global", "history"} {
		if _, present := payload.Context[absent]; present {
			t.Errorf("context carries %q, which this type never declared", absent)
		}
	}
}

// The assignment goes in on stdin as one JSON object, never on argv. A task
// naming a real file list overruns ARG_MAX, and the failure would arrive as
// an exec error naming the binary rather than the size of the ask.
func TestTheAssignmentArrivesOnStdin(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "assignment.json")
	spec := declared(stub(t, "cat >"+captured+"\ncat <<'REPORT'\n"+
		`{"result":{"path":"a.txt"},"verdict":"ok"}`+"\nREPORT"))
	r, _ := runner(t, spec)
	want := task()
	want.Files = []string{"one.go", "two.go"}
	if _, _, err := r.Run(t.Context(), "reader", want, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("reading what the agent was handed: %v", err)
	}
	var payload struct {
		Task struct {
			Files []string `json:"files"`
		} `json:"task"`
		Limits struct {
			MaxTokens int `json:"max_tokens"`
		} `json:"limits"`
		Effects []string `json:"effects"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("the assignment is not json: %v", err)
	}
	if len(payload.Task.Files) != 2 {
		t.Fatalf("files = %v, want both", payload.Task.Files)
	}
	if payload.Limits.MaxTokens != 100 {
		t.Fatalf("max_tokens = %d, want the declared ceiling", payload.Limits.MaxTokens)
	}
	if len(payload.Effects) != 1 || payload.Effects[0] != "read" {
		t.Fatalf("effects = %v, want the declared ceiling", payload.Effects)
	}
}

// The row is opened before the spawn. A row written afterwards would miss
// exactly the runs worth tracing: the ones that die in their first second.
func TestTheRowIsOpenedBeforeTheSpawn(t *testing.T) {
	witness := filepath.Join(t.TempDir(), "rows-at-spawn")
	dbDir := t.TempDir()
	store, err := trace.Open(t.Context(), filepath.Join(dbDir, "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	defer func() { _ = store.Close() }()

	// The stub asks the store, mid-run, how many rows exist. If Begin ran
	// after the spawn there would be none.
	spec := declared(stub(t, "cat >/dev/null\n"+
		"sqlite3 -readonly "+filepath.Join(dbDir, "traces.db")+
		" 'select count(*) from agent_trace where ended_at is null' >"+witness+" 2>&1\n"+
		"cat <<'REPORT'\n"+`{"result":{"path":"a.txt"},"verdict":"ok"}`+"\nREPORT"))
	r, err := agent.New(agent.Options{
		Types: []config.AgentType{spec}, Store: store, Self: "/nonexistent/atenea",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := r.Run(t.Context(), "reader", task(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	seen, err := os.ReadFile(witness)
	if err != nil {
		t.Skipf("the stub could not read the store (no sqlite3 here): %v", err)
	}
	if strings.TrimSpace(string(seen)) != "1" {
		t.Fatalf("the agent saw %q open rows while it ran, want exactly 1",
			strings.TrimSpace(string(seen)))
	}
}

// $atenea is the one placeholder, and it resolves to this binary. Anything
// else in command is used literally.
func TestSelfPlaceholderResolves(t *testing.T) {
	spec := declared(agent.SelfPlaceholder, "-c", "x")
	store, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	defer func() { _ = store.Close() }()

	echo := stub(t, "cat >/dev/null\necho \"$0\" >&2\nexit 9")
	r, err := agent.New(agent.Options{
		Types: []config.AgentType{spec}, Store: store, Self: echo,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, _, err := r.Run(t.Context(), "reader", task(), nil)
	if err == nil {
		t.Fatal("Run: the stub exits 9; want the death")
	}
	if !strings.Contains(report.Reason.Text, echo) {
		t.Fatalf("reason %q does not show that $atenea resolved to %s",
			report.Reason.Text, echo)
	}
}

// An unknown type is a refusal that names what is declared. Nothing falls
// back to another agent: an agent is asked for by name.
func TestUnknownTypeIsRefused(t *testing.T) {
	r, _ := runner(t, declared("/bin/true"))
	_, _, err := r.Run(t.Context(), "writer", task(), nil)
	if err == nil {
		t.Fatal("Run: want a refusal for an undeclared type")
	}
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "reader") {
		t.Fatalf("error %q does not name what is declared", err)
	}
}

// A child may hold its parent's effects or fewer, never more, and the depth
// cap is enforced where the card is made rather than where it is used.
func TestChildNarrowsAndDepthIsCapped(t *testing.T) {
	r, _ := runner(t, declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`)))
	_, parent, err := r.Run(t.Context(), "reader", task(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parent.Depth != 1 {
		t.Fatalf("root depth = %d, want 1", parent.Depth)
	}
	if _, err := parent.Child("x", "reader", contract.AgentSpecialized, task(),
		[]contract.Effect{contract.EffectWrite}, parent.Limits); err == nil {
		t.Fatal("Child: a child asking for write above a read-only parent must be refused")
	}
}

func TestDeclaredListsEveryType(t *testing.T) {
	r, _ := runner(t, declared("/bin/true"))
	if got := r.Declared(); len(got) != 1 || got[0] != "reader" {
		t.Fatalf("Declared() = %v, want [reader]", got)
	}
}
