package recoverypilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type memoryStore struct{ attempts []Attempt }

func (s *memoryStore) PersistAttempt(_ context.Context, attempt Attempt) error {
	s.attempts = append(s.attempts, attempt)
	return nil
}

type scriptedRunner struct {
	results []Result
	calls   int
}

type cancelReturningOKRunner struct {
	started chan struct{}
	calls   int
}

func (r *cancelReturningOKRunner) Run(ctx context.Context, route Route) (Result, error) {
	r.calls++
	close(r.started)
	<-ctx.Done()
	return Result{Status: "ok", Route: route, CostUSD: money(0), Invoked: true, InvokedKnown: true}, nil
}

func (r *scriptedRunner) Run(ctx context.Context, route Route) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Route: route, Failure: contract.FailureCanceled, Canceled: true}, err
	}
	result := r.results[r.calls]
	r.calls++
	if result.Route.ID == "" {
		result.Route = route
	}
	return result, nil
}

func testRoute() Route {
	return Route{ID: "route", Backend: "backend", Provider: "provider", RequestedModel: "model", ObservedModel: "model"}
}

func testRequest() Request {
	return Request{RunID: "run", WorkflowID: "workflow", Route: testRoute(), Effects: []contract.Effect{contract.EffectRead}, Policy: Policy{MaxRetries: 1, BudgetUSD: 10, MaxDuration: time.Minute, ReadOnly: true}, Evidence: EvidenceFixture}
}

func money(value float64) *float64 { return &value }

func TestExecutePersistsTransientAttemptBeforeSameRouteRetry(t *testing.T) {
	store := &memoryStore{}
	runner := &scriptedRunner{results: []Result{
		{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(0.20), Invoked: true, InvokedKnown: true},
		{Status: "ok", CostUSD: money(0.20), Invoked: true, InvokedKnown: true},
	}}
	report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || report.RetryUsed != 1 || len(store.attempts) != 2 {
		t.Fatalf("report = %#v attempts = %#v", report, store.attempts)
	}
	if !store.attempts[0].PersistedBefore || !store.attempts[0].Route.Same(store.attempts[1].Route) {
		t.Fatalf("first attempt was not durable before the same route retry: %#v", store.attempts)
	}
}

func TestExecuteDoesNotFallbackOrRetryNonTransientAndUnknownCost(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		status string
	}{
		{name: "invalid", result: Result{Failure: contract.FailureInvalidInput, CostUSD: money(0.1)}, status: "failed"},
		{name: "permission", result: Result{Failure: contract.FailurePermissionDenied, CostUSD: money(0.1)}, status: "failed"},
		{name: "unknown cost", result: Result{Failure: contract.FailureUnavailable}, status: "blocked"},
		{name: "write result", result: Result{Failure: contract.FailureUnavailable, CostUSD: money(0.1), Effects: []contract.Effect{contract.EffectWrite}}, status: "blocked"},
		{name: "external", result: Result{Failure: contract.FailureUnavailable, CostUSD: money(0.1), External: true}, status: "blocked"},
		{name: "unclassified failure", result: Result{Status: "failed", CostUSD: money(0.1)}, status: "failed"},
		{name: "invalid cost", result: Result{Failure: contract.FailureUnavailable, CostUSD: money(-0.1)}, status: "blocked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{}
			runner := &scriptedRunner{results: []Result{test.result, {Status: "ok", CostUSD: money(0.1)}}}
			report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != test.status || runner.calls != 1 || len(store.attempts) != 1 {
				t.Fatalf("status=%s calls=%d attempts=%d reason=%s", report.Status, runner.calls, len(store.attempts), report.Reason)
			}
		})
	}
}

func TestExecuteDoesNotCertifyIncompleteResultWithoutFailure(t *testing.T) {
	runner := &scriptedRunner{results: []Result{{Status: "incomplete", CostUSD: money(0), Invoked: true, InvokedKnown: true}, {Status: "ok", CostUSD: money(0), Invoked: true, InvokedKnown: true}}}
	report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: &memoryStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "incomplete" || runner.calls != 1 || !strings.Contains(report.Reason, "incomplete") {
		t.Fatalf("incomplete result was certified or retried: %#v calls=%d", report, runner.calls)
	}
}

func TestExecuteOnlyExplicitOKStatusCanCertifySuccess(t *testing.T) {
	statuses := []string{"", "incomplete", "partial", "canceled", "unknown", "running", "complete", "success"}
	for _, status := range statuses {
		t.Run(statusOrEmpty(status), func(t *testing.T) {
			runner := &scriptedRunner{results: []Result{{Status: status, CostUSD: money(0), Invoked: true, InvokedKnown: true}}}
			report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: &memoryStore{}})
			if err != nil {
				t.Fatal(err)
			}
			if report.Status == "ok" || runner.calls != 1 {
				t.Fatalf("non-success status was certified: input=%q report=%#v calls=%d", status, report, runner.calls)
			}
		})
	}
}

func TestExecuteRejectsInvalidCostsBeforePreflightRetry(t *testing.T) {
	costs := []struct {
		name  string
		value float64
	}{
		{name: "negative", value: -1},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "nan", value: math.NaN()},
	}
	for _, test := range costs {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{results: []Result{{Status: "ok", CostUSD: &test.value, InvokedKnown: true, Invoked: false}, {Status: "ok", CostUSD: money(0), InvokedKnown: true, Invoked: true}}}
			store := &memoryStore{}
			report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != "blocked" || runner.calls != 1 || len(store.attempts) != 1 || store.attempts[0].Failure != contract.FailureInvalidInput.String() {
				t.Fatalf("invalid cost entered preflight retry: cost=%v report=%#v attempts=%#v calls=%d", test.value, report, store.attempts, runner.calls)
			}
		})
	}
}

func TestExecuteInvalidCostWithFileStoreIsReopenableAndJSONSafe(t *testing.T) {
	costs := []struct {
		name  string
		value float64
	}{
		{name: "negative", value: -1},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "nan", value: math.NaN()},
	}
	for _, test := range costs {
		t.Run(test.name, func(t *testing.T) {
			store := &FileStore{Path: filepath.Join(t.TempDir(), "attempts.jsonl")}
			runner := &scriptedRunner{results: []Result{{Status: "ok", CostUSD: &test.value, InvokedKnown: true, Invoked: false}}}
			report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: store})
			if err != nil || report.Status != "blocked" || runner.calls != 1 {
				t.Fatalf("invalid cost was accepted: report=%#v err=%v calls=%d", report, err, runner.calls)
			}
			attempts, err := LoadAttempts(store.Path)
			if err != nil || len(attempts) != 1 || attempts[0].Status != "blocked" || attempts[0].Failure != contract.FailureInvalidInput.String() || attempts[0].CostUSD != nil || attempts[0].Reason == "" {
				t.Fatalf("durable invalid-cost receipt = %#v err=%v", attempts, err)
			}
			data, err := report.JSON()
			if err != nil || !json.Valid(data) {
				t.Fatalf("report JSON = %s err=%v", data, err)
			}
			var encoded bytes.Buffer
			if err := json.NewEncoder(&encoded).Encode(report); err != nil || !json.Valid(bytes.TrimSpace(encoded.Bytes())) {
				t.Fatalf("report encoder = %s err=%v", encoded.Bytes(), err)
			}
		})
	}
}

func TestLoadAttemptsRejectsOversizedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAttempts(path); err == nil || !strings.Contains(err.Error(), "history is incomplete") {
		t.Fatalf("LoadAttempts oversized history = %v", err)
	}
}

func TestLoadAttemptsRejectsTruncatedJSONLHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.jsonl")
	if err := os.WriteFile(path, []byte("{}\n{\"status\":\"partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAttempts(path); err == nil || !strings.Contains(err.Error(), "history is incomplete") {
		t.Fatalf("LoadAttempts truncated history = %v", err)
	}
}

func TestFileStoreRejectsGrowthBeyondReadableHistoryLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), attemptsByteLimit), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	if err := store.PersistAttempt(t.Context(), Attempt{}); err == nil || !strings.Contains(err.Error(), "would exceed") {
		t.Fatalf("PersistAttempt oversized growth = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != attemptsByteLimit {
		t.Fatalf("history size = %d; rejected append changed the file", info.Size())
	}
}

func TestFileStoreSerializesTheSharedHistoryLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.jsonl")
	encoded, err := json.Marshal(Attempt{})
	if err != nil {
		t.Fatal(err)
	}
	recordSize := len(encoded) + 1
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), attemptsByteLimit-recordSize), 0o600); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- (&FileStore{Path: path}).PersistAttempt(t.Context(), Attempt{}) }()
	}
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful appends = %d, want exactly one", successes)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > attemptsByteLimit {
		t.Fatalf("history info = %+v, err=%v", info, err)
	}
}

func statusOrEmpty(status string) string {
	if status == "" {
		return "empty"
	}
	return status
}

func TestExecuteCancellationDuringRunnerCannotCertifySuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	runner := &cancelReturningOKRunner{started: make(chan struct{})}
	result := make(chan struct {
		report Report
		err    error
	}, 1)
	go func() {
		report, err := Execute(ctx, testRequest(), Options{Runner: runner, Store: &memoryStore{}})
		result <- struct {
			report Report
			err    error
		}{report, err}
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	completed := <-result
	if completed.err != nil || completed.report.Status != "canceled" || runner.calls != 1 || len(completed.report.Attempts) != 1 || completed.report.Attempts[0].Status != "canceled" {
		t.Fatalf("canceled runner result was certified: report=%#v err=%v calls=%d", completed.report, completed.err, runner.calls)
	}
}

func TestExecuteDoesNotRetryKnownCostWhenInvocationStateIsUnknown(t *testing.T) {
	runner := &scriptedRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(0.1), Invoked: true, InvokedKnown: false}, {Status: "ok", CostUSD: money(0), Invoked: true, InvokedKnown: true}}}
	report, err := Execute(t.Context(), testRequest(), Options{Runner: runner, Store: &memoryStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || runner.calls != 1 || !strings.Contains(report.Reason, "invocation state unknown") {
		t.Fatalf("unknown invocation was retried: %#v calls=%d", report, runner.calls)
	}
}

func TestExecuteEnforcesMeasuredDurationBeforeRetry(t *testing.T) {
	store := &memoryStore{}
	runner := &scriptedRunner{results: []Result{
		{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(0.1), Duration: time.Second},
		{Status: "ok", CostUSD: money(0.1)},
	}}
	request := testRequest()
	request.Policy.MaxDuration = time.Second
	report, err := Execute(t.Context(), request, Options{Runner: runner, Store: store, Now: func() time.Time { return time.Unix(0, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || runner.calls != 1 || report.DurationMS != 1000 {
		t.Fatalf("duration allowed a retry: status=%s calls=%d duration=%d reason=%s", report.Status, runner.calls, report.DurationMS, report.Reason)
	}
}

func TestExecuteDoesNotRetryWhenObservedModelChanges(t *testing.T) {
	store := &memoryStore{}
	request := testRequest()
	request.Route.ObservedModel = ""
	runner := &scriptedRunner{results: []Result{
		{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(0.1), Invoked: true, InvokedKnown: true, Route: Route{ID: "route", Backend: "backend", Provider: "provider", RequestedModel: "model", ObservedModel: "model-a"}},
		{Status: "ok", CostUSD: money(0.1), Invoked: true, InvokedKnown: true, Route: Route{ID: "route", Backend: "backend", Provider: "provider", RequestedModel: "model", ObservedModel: "model-b"}},
	}}
	report, err := Execute(t.Context(), request, Options{Runner: runner, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || runner.calls != 2 || len(store.attempts) != 2 {
		t.Fatalf("observed model changed without blocking fallback: %#v calls=%d", report, runner.calls)
	}
}

func TestExecuteCancellationStopsBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := &memoryStore{}
	runner := &scriptedRunner{results: []Result{{Status: "ok", CostUSD: money(0.1)}}}
	report, err := Execute(ctx, testRequest(), Options{Runner: runner, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "canceled" || runner.calls != 0 || len(store.attempts) != 0 {
		t.Fatalf("cancellation dispatched work: %#v calls=%d attempts=%d", report, runner.calls, len(store.attempts))
	}
}

func TestExecuteStopsBeforeRetryWhenBudgetIsExhausted(t *testing.T) {
	store := &memoryStore{}
	runner := &scriptedRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(1.0)}, {Status: "ok", CostUSD: money(0.1)}}}
	request := testRequest()
	request.Policy.BudgetUSD = 1
	report, err := Execute(t.Context(), request, Options{Runner: runner, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || runner.calls != 1 || len(store.attempts) != 1 {
		t.Fatalf("budget allowed a retry: status=%s calls=%d attempts=%d reason=%s", report.Status, runner.calls, len(store.attempts), report.Reason)
	}
	if report.SpentUSD != 1 || report.RetryUsed != 0 {
		t.Fatalf("budget accounting = %#v", report)
	}
}

func TestRebuildCoordinatorRequiresFullAuthorizedGenerationAndPositiveIndex(t *testing.T) {
	coordinator := &RebuildCoordinator{}
	if _, err := coordinator.Start(RebuildRequest{Root: t.TempDir(), Generation: "g1", Mode: "incremental", ExplicitAuthorization: true}); err == nil {
		t.Fatal("incremental rebuild was admitted")
	}
	root := t.TempDir()
	if _, err := coordinator.Start(RebuildRequest{Root: root, Generation: "g1", Mode: "full"}); err == nil {
		t.Fatal("unauthorized rebuild was admitted")
	}
	lease, err := coordinator.Start(RebuildRequest{Root: root, Generation: "g1", Mode: "full", ExplicitAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	coalesced, err := coordinator.Start(RebuildRequest{Root: root, Generation: "g1", Mode: "full", StandingAuthorization: true})
	if err != nil || !coalesced.coalesced {
		t.Fatalf("same generation was not coalesced: lease=%#v err=%v", coalesced, err)
	}
	if _, err := coordinator.Start(RebuildRequest{Root: root, Generation: "g2", Mode: "full", ExplicitAuthorization: true}); err == nil {
		t.Fatal("concurrent generation was admitted")
	}
	if err := lease.Finish(0); err == nil {
		t.Fatal("zero index count was accepted")
	}
	lease.Abort()
	lease, err = coordinator.Start(RebuildRequest{Root: root, Generation: "g2", Mode: "full", StandingAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Finish(1); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(RebuildRequest{Root: root, Generation: "g2", Mode: "full", ExplicitAuthorization: true}); err == nil {
		t.Fatal("completed generation was restarted")
	}
}

func TestExecuteRejectsNonReadOnlyRequest(t *testing.T) {
	request := testRequest()
	request.Effects = []contract.Effect{contract.EffectWrite}
	_, err := Execute(t.Context(), request, Options{Runner: &scriptedRunner{}, Store: &memoryStore{}})
	if err == nil || err.Error() != "recovery pilot accepts read-only work only" {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteDistinguishesPreflightFromKnownZeroInvocation(t *testing.T) {
	request := testRequest()
	preflight := &scriptedRunner{results: []Result{
		{Status: "failed", Failure: contract.FailureUnavailable, InvokedKnown: true, Invoked: false},
		{Status: "ok", CostUSD: money(0), InvokedKnown: true, Invoked: true},
	}}
	store := &memoryStore{}
	report, err := Execute(t.Context(), request, Options{Runner: preflight, Store: store})
	if err != nil || report.Status != "ok" || preflight.calls != 2 || len(store.attempts) != 2 || store.attempts[0].Invoked || !store.attempts[0].InvokedKnown {
		t.Fatalf("preflight = %#v calls=%d err=%v", report, preflight.calls, err)
	}
	knownZero := &scriptedRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(0), InvokedKnown: true, Invoked: true}, {Status: "ok", CostUSD: money(0), InvokedKnown: true, Invoked: true}}}
	report, err = Execute(t.Context(), request, Options{Runner: knownZero, Store: &memoryStore{}})
	if err != nil || report.Status != "ok" || knownZero.calls != 2 {
		t.Fatalf("known zero = %#v calls=%d err=%v", report, knownZero.calls, err)
	}
	unknown := &scriptedRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, InvokedKnown: true, Invoked: true}, {Status: "ok", CostUSD: money(0), InvokedKnown: true, Invoked: true}}}
	report, err = Execute(t.Context(), request, Options{Runner: unknown, Store: &memoryStore{}})
	if err != nil || report.Status != "blocked" || unknown.calls != 1 || report.Reason != "cost unknown; recovery stopped before another attempt" {
		t.Fatalf("unknown invoked cost = %#v calls=%d err=%v", report, unknown.calls, err)
	}
}

func TestExecuteStopsAtAttemptSpendingCeiling(t *testing.T) {
	request := testRequest()
	request.Policy.AttemptBudgetUSD = 0.10
	runner := &scriptedRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, CostUSD: money(0.10), InvokedKnown: true, Invoked: true}, {Status: "ok", CostUSD: money(0.10)}}}
	report, err := Execute(t.Context(), request, Options{Runner: runner, Store: &memoryStore{}})
	if err != nil || report.Status != "blocked" || runner.calls != 1 || report.NextOrStopped == "" {
		t.Fatalf("ceiling = %#v calls=%d err=%v", report, runner.calls, err)
	}
}

func TestRunFixtureRejectsNonEmptyRootWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "protected")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunFixture(t.Context(), FixtureOptions{Root: root, Sentinel: sentinel}); err == nil {
		t.Fatal("non-empty fixture root was accepted")
	}
	if got, err := os.ReadFile(protected); err != nil || string(got) != "keep" {
		t.Fatalf("protected file changed: %q err=%v", got, err)
	}
}

func TestRunFixtureKivgraphScenarioUsesProductionIndexBoundary(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S11")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if active {
		var body struct {
			Scenario string   `json:"scenario"`
			Requires []string `json:"requires"`
		}
		if err := json.Unmarshal(fixture.Bytes, &body); err != nil {
			t.Fatal(err)
		}
		if body.Scenario != "provider-failure-injection" || len(body.Requires) < 2 {
			t.Fatalf("unexpected sealed recovery contract: %+v", body)
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		t.Run(fmt.Sprintf("iteration-%d", attempt), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "fixture-root")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := RunFixture(t.Context(), FixtureOptions{Root: root, Sentinel: sentinel})
			if err != nil {
				t.Fatal(err)
			}
			for _, scenario := range report.Scenarios {
				if scenario.ID == "I" {
					if scenario.Status != "pass" || scenario.Evidence != EvidenceFixture {
						t.Fatalf("Kivgraph scenario = %#v", scenario)
					}
					return
				}
			}
			t.Fatal("Kivgraph scenario I was not reported")
		})
	}
}

func TestRunFixtureFailsWhenSentinelChangesDuringKivgraph(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fixture-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := RunFixture(t.Context(), FixtureOptions{
		Root:     root,
		Sentinel: sentinel,
		duringKivgraph: func() {
			if writeErr := os.WriteFile(sentinel, []byte("tampered during Kivgraph"), 0o600); writeErr != nil {
				t.Errorf("mutate sentinel: %v", writeErr)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "fail" {
		t.Fatalf("sentinel mutation was certified: %#v", report)
	}
	for _, scenario := range report.Scenarios {
		if scenario.ID == "E" {
			if scenario.Status != "fail" {
				t.Fatalf("sentinel scenario = %#v", scenario)
			}
			return
		}
	}
	t.Fatal("sentinel scenario E was not reported")
}
