package workflow_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/activity"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestWorkflowActivityAgentHelper(t *testing.T) {
	barrier := os.Getenv("ATENEA_WORKFLOW_ACTIVITY_BARRIER")
	if barrier == "" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	file, err := os.OpenFile(barrier, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = file.WriteString("ready\n")
	_ = file.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(barrier)
		if strings.Count(string(raw), "ready") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(3)
		}
		time.Sleep(time.Millisecond)
	}
	if err := activity.PublishFromEnvironment("Bash"); err != nil {
		os.Exit(4)
	}
	_, _ = os.Stdout.WriteString(`{"result":{"ok":true},"verdict":"ok","thread_id":"thread-tool","turn_id":"turn-tool","usage_revision":3,"requested_model":"gpt-5.6-sol","observed_model":"gpt-5.6-sol","requested_reasoning_effort":"medium","observed_reasoning_effort":"medium"}`)
	os.Exit(0)
}

func TestActivityNoticeIsBoundedDeterministicMarkdown(t *testing.T) {
	n := workflow.NewActivityNotice("ATENEA", "reader", "busco", strings.Repeat("referencias ", 30),
		"identificar [components](maliciosos) sin exponer /Users/alice/token sk-proj-secret ghp_token")
	if !strings.HasPrefix(n.Markdown, "> **ATENEA · reader** — busco ") {
		t.Fatalf("markdown = %q", n.Markdown)
	}
	if strings.ContainsAny(n.Markdown, "`[]()") || strings.Contains(n.Markdown, "/Users/") ||
		strings.Contains(n.Markdown, "sk-proj") || strings.Contains(n.Markdown, "ghp_") {
		t.Fatalf("unsafe activity markdown = %q", n.Markdown)
	}
	if words := len(strings.Fields(n.Markdown)); words < 15 || words > 25 {
		t.Fatalf("activity uses %d words, want 15..25: %q", words, n.Markdown)
	}
}

func TestActivityNoticeRedactsSecretsBeforeNormalizingSeparators(t *testing.T) {
	n := workflow.NewActivityNotice("ATENEA", "reader", "busco", "token ghp_supersecretbody", "validar la privacidad")
	if strings.Contains(n.Markdown, "supersecretbody") || strings.Contains(n.Objective, "supersecretbody") {
		t.Fatalf("secret escaped activity redaction: %#v", n)
	}
}

func TestActivityCursorReconnectsWithoutDuplicates(t *testing.T) {
	h := newHarness(t, noCeiling(), declared("worker", answers(t, t.TempDir(), "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	first := workflow.NewActivityNotice("ATENEA", "worker", "busco", "el símbolo Router", "identificar sus usos")
	first.WorkflowID, first.InvocationID, first.At = run.ID, "call-1", time.Now()
	if inserted, err := h.state.RecordActivityOnce(t.Context(), first); err != nil || !inserted {
		t.Fatalf("first insert = %v err=%v", inserted, err)
	}
	if inserted, err := h.state.RecordActivityOnce(t.Context(), first); err != nil || inserted {
		t.Fatalf("replay insert = %v err=%v", inserted, err)
	}
	batch, cursor, err := h.state.Activities(t.Context(), run.ID, 0, 200)
	if err != nil || len(batch) != 1 || cursor == 0 {
		t.Fatalf("first batch = %#v cursor=%d err=%v", batch, cursor, err)
	}
	second := workflow.NewActivityNotice("ATENEA", "reviewer", "reviso", "el cambio aplicado", "comprobar el criterio")
	second.WorkflowID, second.InvocationID, second.At = run.ID, "call-2", time.Now()
	if err := h.state.RecordActivity(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	next, nextCursor, err := h.state.Activities(t.Context(), run.ID, cursor, 200)
	if err != nil || len(next) != 1 || next[0].InvocationID != "call-2" || nextCursor <= cursor {
		t.Fatalf("reconnect batch = %#v cursor=%d err=%v", next, nextCursor, err)
	}
}

func TestActivityPageDeclaresWhenMoreEntriesRemain(t *testing.T) {
	h := newHarness(t, noCeiling(), declared("worker", answers(t, t.TempDir(), "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"call-1", "call-2", "call-3"} {
		notice := workflow.NewActivityNotice("ATENEA", "worker", "ejecuto", id, "probar la página")
		notice.WorkflowID, notice.InvocationID, notice.At = run.ID, id, time.Now()
		if err := h.state.RecordActivity(t.Context(), notice); err != nil {
			t.Fatal(err)
		}
	}
	first, cursor, more, err := h.state.ActivitiesPage(t.Context(), run.ID, 0, 2)
	if err != nil || len(first) != 2 || !more {
		t.Fatalf("first page = %#v cursor=%d more=%v err=%v", first, cursor, more, err)
	}
	second, next, more, err := h.state.ActivitiesPage(t.Context(), run.ID, cursor, 2)
	if err != nil || len(second) != 1 || second[0].InvocationID != "call-3" || more || next <= cursor {
		t.Fatalf("second page = %#v cursor=%d more=%v err=%v", second, next, more, err)
	}
}

func TestPendingActivityIsAcknowledgedOnlyAfterSuccessfulPublication(t *testing.T) {
	h := newHarness(t, noCeiling(), declared("worker", answers(t, t.TempDir(), "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	notice := workflow.NewActivityNotice("PLAN", "Ampliación", "añado", "los pasos aprobados", "ampliar el workflow")
	notice.WorkflowID, notice.InvocationID, notice.At = run.ID, "plan-1-digest", time.Now()
	if err := h.state.RecordActivity(t.Context(), notice); err != nil {
		t.Fatal(err)
	}
	pending, err := h.state.PendingActivities(t.Context(), run.ID, 200)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending before ack = %#v err=%v", pending, err)
	}
	if err := h.state.MarkActivitiesDelivered(t.Context(), run.ID, []string{notice.InvocationID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = h.state.PendingActivities(t.Context(), run.ID, 200)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after ack = %#v err=%v", pending, err)
	}
}

func TestWorkflowPublishesDurableActivityBeforeAgentProcess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "activity-visible")
	command := stub(t, dir, "worker", "test -f '"+marker+"' || exit 9\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	opts := workflow.Options{Lanes: noCeiling(), Activity: func(batch []workflow.ActivityNotice) error {
		return os.WriteFile(marker, []byte(batch[0].Markdown), 0o600)
	}}
	h := newHarnessWith(t, opts, dir, declared("worker", command, config.PoolAgent))
	run, err := h.engine.Start(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Activity) != 1 || run.Activity[0].InvocationID == "" {
		t.Fatalf("activity = %#v", run.Activity)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != run.Activity[0].Markdown {
		t.Fatalf("published = %q err=%v, durable=%q", raw, err, run.Activity[0].Markdown)
	}
}

func TestActivityCorrelationIsCompletedOnTheOriginalInvocation(t *testing.T) {
	dir := t.TempDir()
	command := stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok","thread_id":"thread-1","turn_id":"turn-1","usage_revision":7,"requested_model":"gpt-5.6-sol","observed_model":"gpt-5.6-sol","requested_reasoning_effort":"medium","observed_reasoning_effort":"medium"}'`)
	h := newHarness(t, noCeiling(), declared("worker", command, config.PoolAgent))
	node := step("a", "worker", nil)
	node.PointID = "P05"
	node.PointTitle = "Avisos Markdown"
	node.Route = &contract.Route{Model: "gpt-5.6-sol", RequestedModel: "gpt-5.6-sol", ReasoningEffort: "medium", RequestedReasoningEffort: "medium"}
	run, err := h.engine.Start(t.Context(), graphOf(node))
	if err != nil {
		t.Fatal(err)
	}
	var correlated []workflow.ActivityNotice
	for _, notice := range run.Activity {
		if notice.AgentRunID != "" {
			correlated = append(correlated, notice)
		}
	}
	if len(correlated) != 1 {
		t.Fatalf("correlated activity count = %d, all=%#v", len(correlated), run.Activity)
	}
	n := correlated[0]
	if n.PointID != "P05" || n.AgentRunID == "" || n.InvocationID != n.AgentRunID || n.ThreadID != "thread-1" || n.TurnID != "turn-1" || n.UsageRevision != 7 || n.RequestedModel != "gpt-5.6-sol" || n.ObservedModel != "gpt-5.6-sol" || n.RequestedReasoningEffort != "medium" || n.ObservedReasoningEffort != "medium" {
		t.Fatalf("correlated activity = %#v", n)
	}
}

func TestParallelDispatchPublishesOneBatchWithOneLinePerCall(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "published")
	batches := 0
	opts := workflow.Options{Lanes: noCeiling(), Activity: func(batch []workflow.ActivityNotice) error {
		batches++
		if len(batch) != 2 {
			t.Fatalf("batch has %d notices, want 2", len(batch))
		}
		return os.WriteFile(marker, []byte(batch[0].Markdown+"\n"+batch[1].Markdown), 0o600)
	}}
	worker := stub(t, dir, "worker", "test -f '"+marker+"' || exit 9\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	h := newHarnessWith(t, opts, dir, declared("worker", worker, config.PoolAgent))
	run, err := h.engine.Start(t.Context(), graphOf(step("a", "worker", nil), step("b", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	if batches != 1 || len(run.Activity) != 2 {
		t.Fatalf("batches=%d activity=%d", batches, len(run.Activity))
	}
}

func TestParallelAgentsPublishInternalToolsWithoutLoss(t *testing.T) {
	dir := t.TempDir()
	barrier := filepath.Join(dir, "barrier")
	worker := declared("worker", os.Args[0], config.PoolAgent)
	worker.Args = []string{"-test.run=^TestWorkflowActivityAgentHelper$"}
	worker.Env = []string{"ATENEA_WORKFLOW_ACTIVITY_BARRIER=" + barrier}
	var mu sync.Mutex
	internalBatchSizes := []int{}
	opts := workflow.Options{Lanes: noCeiling(), Activity: func(batch []workflow.ActivityNotice) error {
		if len(batch) > 0 && batch[0].Tool == "Bash" {
			mu.Lock()
			internalBatchSizes = append(internalBatchSizes, len(batch))
			mu.Unlock()
		}
		return nil
	}}
	h := newHarnessWith(t, opts, dir, worker)
	a, b := step("a", "worker", nil), step("b", "worker", nil)
	a.PointID, b.PointID = "P05", "P05"
	a.PointTitle, b.PointTitle = "Avisos Markdown", "Avisos Markdown"
	run, err := h.engine.Start(t.Context(), graphOf(a, b))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	totalInternalNotices := 0
	for _, size := range internalBatchSizes {
		totalInternalNotices += size
	}
	if totalInternalNotices != 2 {
		t.Fatalf("internal batches = %v, want two notices", internalBatchSizes)
	}
	toolNotices := 0
	for _, notice := range run.Activity {
		if notice.Tool != "Bash" {
			continue
		}
		toolNotices++
		if notice.PointID != "P05" || notice.AgentRunID == "" || notice.ThreadID != "thread-tool" || notice.TurnID != "turn-tool" || notice.UsageRevision != 3 || notice.RequestedModel != "gpt-5.6-sol" || notice.ObservedModel != "gpt-5.6-sol" {
			t.Fatalf("tool notice correlation = %#v", notice)
		}
	}
	if toolNotices != 2 {
		t.Fatalf("tool notices = %d, want 2", toolNotices)
	}
	telemetry, err := h.state.PointTelemetry(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	toolUses := 0
	for _, point := range telemetry {
		if point.PointID == "P05" {
			toolUses = point.ToolUses
		}
	}
	if toolUses != 2 {
		t.Fatalf("tool telemetry uses = %d, want 2", toolUses)
	}
}

func TestPublicationFailureStopsBeforeAgentProcess(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "agent-ran")
	worker := stub(t, dir, "worker", "touch '"+ran+"'\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	opts := workflow.Options{Lanes: noCeiling(), Activity: func([]workflow.ActivityNotice) error {
		return errors.New("closed chat")
	}}
	h := newHarnessWith(t, opts, dir, declared("worker", worker, config.PoolAgent))
	run, err := h.engine.Start(t.Context(), graphOf(step("a", "worker", nil)))
	if err == nil || !strings.Contains(err.Error(), "could not be published") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(ran); !os.IsNotExist(statErr) {
		t.Fatalf("agent ran before publication: %v", statErr)
	}
	if got := stepOf(t, run, "a").Status; got != workflow.StatusInterrupted {
		t.Fatalf("status = %s, want interrupted", got)
	}
}

func TestFailedPlanPublicationIsReplayedAfterDurableApplyBeforeDispatch(t *testing.T) {
	dir := t.TempDir()
	worker := declared("worker", answers(t, dir, "worker"), config.PoolAgent)
	first := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Activity: func(batch []workflow.ActivityNotice) error {
		if len(batch) != 1 || batch[0].Kind != "plan" {
			t.Fatalf("failed batch = %#v", batch)
		}
		return errors.New("chat closed")
	}}, dir, worker)
	run, _, err := first.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	approve(t, first, run.ID)
	gate, err := first.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("b", "worker", []string{"a"})}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.state.Answer(t.Context(), run.ID, gate.Ordinal, workflow.DecisionApproved, "tester", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.engine.Resume(t.Context(), run.ID, nil); err == nil {
		t.Fatal("resume applied a plan whose notice failed")
	}
	held, err := first.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(held.Steps) != 2 {
		t.Fatalf("plan was not saved before publication: %d steps", len(held.Steps))
	}

	var published []workflow.ActivityNotice
	second := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Activity: func(batch []workflow.ActivityNotice) error {
		published = append(published, batch...)
		return nil
	}}, dir, worker)
	finished, err := second.engine.Resume(t.Context(), run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(published) == 0 || published[0].Kind != "plan" || len(finished.Steps) != 2 {
		t.Fatalf("published=%#v steps=%d", published, len(finished.Steps))
	}
}
