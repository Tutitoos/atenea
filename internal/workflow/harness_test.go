package workflow_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The tests in this package use fake agents: shell stubs that answer a fixed
// report. Everything between the spawn and the record is the real path -- real
// processes, real assignments on stdin, real trace rows -- because the parts
// this package can get wrong are all on that side. A model would only make the
// answers slower and less predictable.

// stub writes an agent binary whose body is `body` and returns its path. The
// assignment is drained first: an agent that leaves stdin unread can wedge the
// writer.
func stub(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\ncat >/dev/null\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return path
}

// waitFor blocks until a stub says it reached a point, or fails the test.
//
// It replaces `time.Sleep(300 * time.Millisecond)` and the assumption
// underneath it: that a fixed wall-clock delay is long enough for another
// process to get somewhere. On a loaded machine it is not, and the test then
// exercises a state nobody meant to test -- canceling a run whose agent had
// not started yet, which is a different scenario that happens to share a name
// with this one.
//
// The deadline is generous on purpose. It is not a measurement of how long the
// step should take: it is the point past which something is wrong rather than
// merely slow, and a test that fails at ten seconds on a busy laptop teaches
// people to rerun it rather than read it.
func waitFor(t *testing.T, dir, marker string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if ran(t, dir, marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: it never appeared", marker)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// answers is a stub that reports ok and nothing else.
func answers(t *testing.T, dir, name string) string {
	t.Helper()
	return stub(t, dir, name, `echo '{"result":{"ok":true},"verdict":"ok"}'`)
}

// charged is a stub that answers ok with a charge attached: input and output
// tokens, a dollar figure, and who priced it.
func charged(t *testing.T, dir, name string, inputTokens, outputTokens int, usd float64, pricedBy string) string {
	t.Helper()
	body := fmt.Sprintf(`echo '{"result":{"ok":true},"verdict":"ok",`+
		`"spent":{"input_tokens":%d,"output_tokens":%d,"usd":%v,"priced_by":%q}}'`,
		inputTokens, outputTokens, usd, pricedBy)
	return stub(t, dir, name, body)
}

// declared builds an agent type over a stub.
func declared(name, command string, pool config.Pool, effects ...contract.Effect) config.AgentType {
	if len(effects) == 0 {
		effects = []contract.Effect{contract.EffectRead}
	}
	return config.AgentType{
		Spec: contract.AgentTypeSpec{
			Name: name,
			Kind: contract.AgentSpecialized,
			Result: []contract.Field{
				{Name: "ok", Type: contract.TypeBool, Required: true, Summary: "it ran"},
			},
		},
		Summary: "a fake agent for the workflow tests",
		Command: command,
		Context: []contract.ContextLevel{contract.ContextRepository},
		Effects: effects,
		Limits:  contract.Limits{MaxDuration: 20 * time.Second, MaxTokens: 100},
		Pool:    pool,
	}
}

// harness builds an engine over a real runner, a real trace store and a real
// workflow store, all in one temp database.
type harness struct {
	engine *workflow.Engine
	state  *workflow.Store
	traces *trace.Store
	dir    string
}

func newHarness(t *testing.T, lanes config.Workflow, types ...config.AgentType) *harness {
	t.Helper()
	return newHarnessWith(t, workflow.Options{Lanes: lanes}, "", types...)
}

// newHarnessOver builds a second engine over a database that already exists.
// Nothing in the first engine's memory reaches it, which is the point: what
// it can do, it does from the record.
func newHarnessOver(t *testing.T, dir string, lanes config.Workflow,
	types ...config.AgentType) *harness {
	t.Helper()
	return newHarnessWith(t, workflow.Options{Lanes: lanes}, dir, types...)
}

// newHarnessWith is newHarness with the engine's own seams open: the pid it
// runs as, and how it decides whether another pid is alive. An empty dir gets
// a fresh one.
func newHarnessWith(t *testing.T, opts workflow.Options, dir string,
	types ...config.AgentType) *harness {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	path := filepath.Join(dir, "traces.db")

	traces, err := trace.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	t.Cleanup(func() { _ = traces.Close() })

	state, err := workflow.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening the workflow store: %v", err)
	}
	t.Cleanup(func() { _ = state.Close() })

	runner, err := agent.New(agent.Options{
		Types: types,
		Store: traces,
		Self:  "/nonexistent/atenea",
	})
	if err != nil {
		t.Fatalf("building the runner: %v", err)
	}
	opts.Runner = runner
	opts.Store = state
	opts.Types = types
	engine, err := workflow.New(opts)
	if err != nil {
		t.Fatalf("building the engine: %v", err)
	}
	return &harness{engine: engine, state: state, traces: traces, dir: dir}
}

// step is a graph node with the boilerplate filled in.
func step(id, typeName string, needs []string, effects ...contract.Effect) workflow.Step {
	if len(effects) == 0 {
		effects = []contract.Effect{contract.EffectRead}
	}
	return workflow.Step{
		ID:         id,
		TypeName:   typeName,
		Task:       contract.Task{Objective: "run " + id, Criterion: "it answers"},
		Needs:      needs,
		Permission: contract.Permission{Effects: effects},
	}
}

// withFiles puts a file list on a step, which is what the write rule reads.
func withFiles(s workflow.Step, files ...string) workflow.Step {
	s.Task.Files = files
	return s
}

// reviewing points a step at the answer it audits, with the default bar.
func reviewing(s workflow.Step, subject string) workflow.Step {
	s.Subject = subject
	return s
}

// reviewingOnly is the same edge with the stricter bar: successes only.
func reviewingOnly(s workflow.Step, subject string, on workflow.Requirement) workflow.Step {
	s.Subject = subject
	s.On = on
	return s
}

func graphOf(steps ...workflow.Step) workflow.Graph {
	return workflow.Graph{Task: "a test commission", Steps: steps}
}

// statuses reads the record back as a map, which is what most assertions want.
func statuses(t *testing.T, run workflow.Run) map[string]string {
	t.Helper()
	out := make(map[string]string, len(run.Steps))
	for _, s := range run.Steps {
		out[s.Step.ID] = run.Label(s)
	}
	return out
}

func stepOf(t *testing.T, run workflow.Run, id string) workflow.StepRow {
	t.Helper()
	for _, s := range run.Steps {
		if s.Step.ID == id {
			return s
		}
	}
	t.Fatalf("no step %s in the record", id)
	return workflow.StepRow{}
}

// counter is a stub that records how many copies of itself were alive at
// once. Each run creates a file, counts the files, sleeps, and removes it, so
// the highest count any run saw is the real overlap.
func counter(t *testing.T, dir, name, lane string, hold time.Duration) string {
	t.Helper()
	live := filepath.Join(dir, "live-"+lane)
	all := filepath.Join(dir, "live-all")
	if err := os.MkdirAll(live, 0o750); err != nil {
		t.Fatalf("making the liveness dir: %v", err)
	}
	if err := os.MkdirAll(all, 0o750); err != nil {
		t.Fatalf("making the shared liveness dir: %v", err)
	}
	body := "d=" + live + "\n" +
		"a=" + all + "\n" +
		"touch \"$d/$$\"\n" +
		"touch \"$a/$$\"\n" +
		"ls \"$d\" | wc -l >> " + filepath.Join(dir, "counts-"+lane) + "\n" +
		"ls \"$a\" | wc -l >> " + filepath.Join(dir, "counts-all") + "\n" +
		"sleep " + strconv.FormatFloat(hold.Seconds(), 'f', 2, 64) + "\n" +
		"rm -f \"$d/$$\"\n" +
		"rm -f \"$a/$$\"\n" +
		`echo '{"result":{"ok":true},"verdict":"ok"}'`
	return stub(t, dir, name, body)
}

// peak is the highest number of agents a lane ever had running at once.
func peak(t *testing.T, dir, lane string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "counts-"+lane))
	if err != nil {
		t.Fatalf("reading the counts: %v", err)
	}
	high := 0
	for _, line := range strings.Fields(string(raw)) {
		n, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("count %q is not a number: %v", line, err)
		}
		if n > high {
			high = n
		}
	}
	return high
}

// ran reports whether an agent ever started, which is how a test proves a
// queued step was never spawned.
func ran(t *testing.T, dir, marker string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, marker))
	return err == nil
}

func rowsOf(t *testing.T, h *harness) []trace.Row {
	t.Helper()
	rows, err := h.traces.List(context.Background(), trace.Filter{})
	if err != nil {
		t.Fatalf("listing traces: %v", err)
	}
	return rows
}
