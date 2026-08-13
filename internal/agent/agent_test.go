package agent_test

import (
	"context"
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

// runnerOver builds a Runner over an existing store rather than a fresh one,
// wired the same way cmd/atenea/agent.go and workflow.Serve wire it -- for a
// test that has to seed trace rows before the first real dispatch, which a
// store built fresh every time could never have.
func runnerOver(t *testing.T, store *trace.Store, declared config.AgentType) *agent.Runner {
	t.Helper()
	r, err := agent.New(agent.Options{
		Types:     []config.AgentType{declared},
		Store:     store,
		Self:      "/nonexistent/atenea",
		Workspace: agent.Workspace{RepositoryID: "current", RepositoryRoot: t.TempDir()},
		History: func(ctx context.Context, name string, limit int) ([]trace.Row, error) {
			return store.List(ctx, trace.Filter{TypeName: name, Limit: limit})
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func runner(t *testing.T, declared config.AgentType) (*agent.Runner, *trace.Store) {
	t.Helper()
	store, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return runnerOver(t, store, declared), store
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

// openStore opens a bare trace store a test can seed rows into directly,
// standing in for the earlier dispatches pastRuns reads back -- seeding
// through Begin/Complete is what the store itself would have written, and
// it does not need a second process spawned just to produce a closed row.
func openStore(t *testing.T) *trace.Store {
	t.Helper()
	s, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedClosedRun writes one closed "reader" row directly to store, the way
// an earlier `atenea agent reader` would have left it.
func seedClosedRun(t *testing.T, store *trace.Store, id string, at time.Time,
	verdict contract.Verdict, discovered []contract.Discovery) {
	t.Helper()
	if err := store.Begin(t.Context(), trace.Row{
		ID: id, TypeName: "reader", Kind: contract.AgentSpecialized,
		Objective: "an earlier run", Depth: 1, StartedAt: at,
	}); err != nil {
		t.Fatalf("seeding %s: Begin: %v", id, err)
	}
	if err := store.Complete(t.Context(), id, at.Add(time.Second),
		verdict, contract.Reason{}, discovered); err != nil {
		t.Fatalf("seeding %s: Complete: %v", id, err)
	}
}

// dispatchWithHistory runs one real dispatch of "reader", declaring the
// given context levels, against store, and returns the raw assignment JSON
// the agent process was handed on stdin -- what pastRuns actually served,
// not a second-hand description of it.
func dispatchWithHistory(t *testing.T, store *trace.Store, levels []contract.ContextLevel) []byte {
	t.Helper()
	captured := filepath.Join(t.TempDir(), "assignment.json")
	spec := declared(stub(t, "cat >"+captured+"\ncat <<'REPORT'\n"+
		`{"result":{"path":"a.txt"},"verdict":"ok"}`+"\nREPORT"))
	spec.Context = levels
	r := runnerOver(t, store, spec)
	if _, _, err := r.Run(t.Context(), "reader", task(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("reading what the agent was handed: %v", err)
	}
	return raw
}

// historyRuns parses the "runs" pastRuns served at the history level out of
// one captured assignment.
func historyRuns(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var payload struct {
		Context struct {
			History struct {
				Runs []map[string]any `json:"runs"`
			} `json:"history"`
		} `json:"context"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("the assignment is not json: %v", err)
	}
	return payload.Context.History.Runs
}

// findRun returns the served entry for one past run id. A real dispatch's
// own row is opened before it serves its own history (Begin runs before
// serve), so that row is always present too -- a test looks its target row
// up by id rather than assume a count or a position.
func findRun(runs []map[string]any, id string) map[string]any {
	for _, run := range runs {
		if run["id"] == id {
			return run
		}
	}
	return nil
}

// A discovery is not the work's result -- it is a fact the row's own verdict
// backs. An ok row's discovery is exactly what the next dispatch of the same
// type should be able to build on without paying to learn it again.
func TestADiscoveryFromAnOKRowReachesTheNextRunsHistory(t *testing.T) {
	store := openStore(t)
	seedClosedRun(t, store, "past-ok", time.Now(), contract.VerdictOK,
		[]contract.Discovery{
			{Level: contract.ContextRepository, Note: "the loader lives at internal/config/config.go"},
		})

	runs := historyRuns(t, dispatchWithHistory(t, store, []contract.ContextLevel{contract.ContextHistory}))
	run := findRun(runs, "past-ok")
	if run == nil {
		t.Fatalf("runs = %+v, want the seeded ok row present", runs)
	}
	found, _ := run["discovered"].([]any)
	if len(found) != 1 || found[0] != "repository: the loader lives at internal/config/config.go" {
		t.Fatalf("discovered = %v, want the ok row's one discovery", found)
	}
}

// The gate is the discovering row's own verdict: a row that answered badly
// is not a source the next run should build on without checking it again, so
// what it found stays off the wire even though it is genuinely on the row.
func TestADiscoveryFromAFailedRowDoesNotReachHistory(t *testing.T) {
	store := openStore(t)
	seedClosedRun(t, store, "past-failed", time.Now(), contract.VerdictFailed,
		[]contract.Discovery{
			{Level: contract.ContextRepository, Note: "a fact a failed run still noticed"},
		})

	raw := dispatchWithHistory(t, store, []contract.ContextLevel{contract.ContextHistory})
	run := findRun(historyRuns(t, raw), "past-failed")
	if run == nil {
		t.Fatalf("runs missing the seeded failed row")
	}
	if _, present := run["discovered"]; present {
		t.Fatalf("run = %v, a failed row must carry no discovered key", run)
	}
	if strings.Contains(string(raw), "a fact a failed run still noticed") {
		t.Fatalf("assignment = %s, a failed row's discovery leaked onto the wire", raw)
	}
}

// Two rows hitting the same fact are not two facts. The served history must
// not repeat one sentence once per row that happened to report it.
func TestTwoRowsReportingTheSameNoteServeItOnce(t *testing.T) {
	store := openStore(t)
	now := time.Now()
	seedClosedRun(t, store, "past-1", now, contract.VerdictOK,
		[]contract.Discovery{{Level: contract.ContextRepository, Note: "shared fact"}})
	seedClosedRun(t, store, "past-2", now.Add(time.Minute), contract.VerdictOK,
		[]contract.Discovery{{Level: contract.ContextRepository, Note: "shared fact"}})

	runs := historyRuns(t, dispatchWithHistory(t, store, []contract.ContextLevel{contract.ContextHistory}))
	count := 0
	for _, run := range runs {
		found, _ := run["discovered"].([]any)
		for _, d := range found {
			if d == "repository: shared fact" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("runs = %+v, want the shared discovery served exactly once, got %d", runs, count)
	}
}

// A level a type never declared is not sent at all -- discoveries included.
// Real history sitting in the trace store must not leak onto the wire just
// because it exists; it leaks only to a type that asked for it.
func TestAnAgentTypeThatDidNotDeclareHistoryIsServedNothing(t *testing.T) {
	store := openStore(t)
	seedClosedRun(t, store, "past-ok", time.Now(), contract.VerdictOK,
		[]contract.Discovery{{Level: contract.ContextRepository, Note: "should never cross the wire"}})

	raw := dispatchWithHistory(t, store, []contract.ContextLevel{contract.ContextRepository})
	var payload struct {
		Context map[string]any `json:"context"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("the assignment is not json: %v", err)
	}
	if _, present := payload.Context["history"]; present {
		t.Fatalf("context = %v, want no history key for a type that never declared it", payload.Context)
	}
	if strings.Contains(string(raw), "should never cross the wire") {
		t.Fatalf("assignment = %s, a withheld discovery leaked onto the wire", raw)
	}
}
