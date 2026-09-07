package kivgraph

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestProbeManagedFreshUsesAuthorizedFullProductionLane(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	var mu sync.Mutex
	generation, fresh := 1, false
	var indexCalls atomic.Int32
	var modesMu sync.Mutex
	var modes []string
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		state := "stale"
		if fresh {
			state = "fresh"
		}
		return freshStatusWithGeneration(t, repo, generation, state), false
	}
	runner := newTestRunner(t, sess)
	runner.maintenanceDirectory = t.TempDir()
	runner.index = func(ctx context.Context, _, mode string) (IndexReport, error) {
		if err := ctx.Err(); err != nil {
			return IndexReport{}, err
		}
		indexCalls.Add(1)
		modesMu.Lock()
		modes = append(modes, mode)
		modesMu.Unlock()
		if mode != "full" {
			return IndexReport{}, errors.New("diagnostic admitted non-full indexing")
		}
		mu.Lock()
		generation, fresh = 2, true
		mu.Unlock()
		return IndexReport{Generation: "2", Nodes: 7, Edges: 11}, nil
	}

	unauthorized := contract.Permission{Task: "fixture", Effects: []contract.Effect{contract.EffectRead}}
	if _, err := runner.ProbeManagedFresh(t.Context(), repo, unauthorized); contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("unauthorized probe error = %v, want permission denied", err)
	}
	if got := indexCalls.Load(); got != 0 {
		t.Fatalf("unauthorized probe started %d index calls", got)
	}

	authorized := contract.Permission{Task: "fixture", Effects: []contract.Effect{contract.EffectRead, contract.EffectWrite, contract.EffectProcess}}
	report, err := runner.ProbeManagedFresh(t.Context(), repo, authorized)
	if err != nil {
		t.Fatalf("authorized probe: %v", err)
	}
	if report.Status != "fresh" || report.Generation != 2 || !report.Rebuilt || indexCalls.Load() != 1 {
		t.Fatalf("report=%+v indexCalls=%d", report, indexCalls.Load())
	}
	modesMu.Lock()
	defer modesMu.Unlock()
	if len(modes) != 1 || modes[0] != "full" {
		t.Fatalf("index modes=%v, want one full call", modes)
	}
}

func TestProbeManagedFreshStandingServiceCoalescesAndHonorsCancellation(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	var mu sync.Mutex
	generation, fresh := 1, false
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		state := "stale"
		if fresh {
			state = "fresh"
		}
		return freshStatusWithGeneration(t, repo, generation, state), false
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var indexCalls atomic.Int32
	runner := newTestRunner(t, sess)
	runner.requireFresh = true
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	runner.index = func(ctx context.Context, _, mode string) (IndexReport, error) {
		if mode != "full" {
			return IndexReport{}, errors.New("standing service attempted non-full indexing")
		}
		indexCalls.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return IndexReport{}, ctx.Err()
		case <-release:
		}
		mu.Lock()
		generation, fresh = 2, true
		mu.Unlock()
		return IndexReport{Generation: "2", Nodes: 7, Edges: 11}, nil
	}
	if err := runner.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	defer runner.CloseMaintenance()
	auto := contract.RunRequest{
		Capability:     contract.Capability{ID: CapabilityIntent, Version: contract.Version{Major: 1}, Summary: "fixture intent", Effects: []contract.Effect{contract.EffectRead}, Inputs: []contract.Field{{Name: "intent", Type: contract.TypeString, Required: true}}},
		Implementation: contract.Implementation{ID: ImplIntent, Provider: "kivgraph", Capability: CapabilityIntent},
		Repository:     repo, Payload: map[string]any{"intent": "fixture"}, Permission: contract.Permission{Task: "fixture", Effects: []contract.Effect{contract.EffectRead}},
	}
	first := make(chan error, 1)
	go func() { _, err := runner.Run(t.Context(), auto); first <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("standing maintenance did not start")
	}
	if got := indexCalls.Load(); got != 1 {
		t.Fatalf("coalesced calls=%d, want one", got)
	}
	if err := <-first; contract.CodeOf(err) != "maintenance_pending" {
		t.Fatalf("automatic request error=%v, want maintenance_pending", err)
	}

	result := make(chan struct {
		report ManagedFreshReport
		err    error
	}, 1)
	authorized := contract.Permission{Task: "fixture", Effects: []contract.Effect{contract.EffectRead, contract.EffectWrite, contract.EffectProcess}}
	go func() {
		report, err := runner.ProbeManagedFresh(t.Context(), repo, authorized)
		result <- struct {
			report ManagedFreshReport
			err    error
		}{report, err}
	}()
	close(release)
	out := <-result
	if out.err != nil || out.report.Status != "fresh" || out.report.Generation != 2 {
		t.Fatalf("joined report=%+v err=%v", out.report, out.err)
	}
	if got := indexCalls.Load(); got != 1 {
		t.Fatalf("joined maintenance duplicated index: %d", got)
	}

	// A separate explicit runner proves cancellation at the real index seam
	// does not publish a generation or silently dispatch a second full pass.
	cancelFake, cancelSess := newFakeKivgraph(t)
	cancelFake.on(toolStatus, freshStatusWithGeneration(t, repo, 1, "stale"), false)
	cancelStarted := make(chan struct{})
	var cancelCalls atomic.Int32
	cancelRunner := newTestRunner(t, cancelSess)
	cancelRunner.maintenanceDirectory = t.TempDir()
	cancelRunner.index = func(ctx context.Context, _, mode string) (IndexReport, error) {
		if mode != "full" {
			return IndexReport{}, errors.New("cancellation admitted non-full indexing")
		}
		cancelCalls.Add(1)
		close(cancelStarted)
		<-ctx.Done()
		return IndexReport{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	callDone := make(chan error, 1)
	go func() { _, err := cancelRunner.ProbeManagedFresh(ctx, repo, authorized); callDone <- err }()
	select {
	case <-cancelStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellation fixture did not reach full index seam")
	}
	cancel()
	if err := <-callDone; err == nil {
		t.Fatal("canceled managed freshness reported success")
	}
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("canceled managed freshness retried index: %d", got)
	}
}

func freshStatusWithGeneration(t *testing.T, repo contract.Repository, generation int, state string) string {
	t.Helper()
	return freshStatus(t, repo, generation, state)
}
