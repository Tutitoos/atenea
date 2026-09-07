package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Tutitoos/atenea/internal/resultcache"
	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type cacheFixtureRunner struct {
	calls            atomic.Int32
	partial          bool
	identityErr      bool
	generation       int
	snapshot         int
	resultGeneration int
	resultSnapshot   int
	resultTruncated  bool
	sourceTrimmed    int
	started          chan struct{}
	release          chan struct{}
	startOnce        sync.Once
	identityCalls    atomic.Int32
	identitySecond   chan struct{}
	identityOnce     sync.Once
	runErr           error
	runOutcome       contract.Outcome
}

func (r *cacheFixtureRunner) ID() string                { return "fixture" }
func (r *cacheFixtureRunner) Serves(string) bool        { return true }
func (r *cacheFixtureRunner) Implementations() []string { return []string{"fixture.context"} }
func (r *cacheFixtureRunner) Capabilities() []string    { return []string{"code.context"} }
func (r *cacheFixtureRunner) CacheIdentity(context.Context, contract.RunRequest) (contract.CacheIdentity, error) {
	if r.identitySecond != nil && r.identityCalls.Add(1) == 2 {
		r.identityOnce.Do(func() { close(r.identitySecond) })
	}
	if r.identityErr {
		return contract.CacheIdentity{}, contract.Fail(contract.FailureUnavailable, "identity unavailable")
	}
	generation, snapshot := r.generation, r.snapshot
	if generation == 0 {
		generation, snapshot = 3, 4
	}
	return contract.CacheIdentity{Generation: generation, Snapshot: snapshot, Freshness: "fresh", ToolVersion: "fixture-v1", Instance: "fixture-1"}, nil
}

func TestCachedRunnerUsesObservedGenerationBeforeHit(t *testing.T) {
	runner := &cacheFixtureRunner{}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: runner, cache: cache}
	req := cacheFixtureRequest(t.TempDir())
	first, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit {
		t.Fatal("first call unexpectedly hit")
	}
	runner.generation, runner.snapshot = 2, 9
	second, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHit || runner.calls.Load() != 2 {
		t.Fatalf("stale generation hit=%v calls=%d", second.CacheHit, runner.calls.Load())
	}
	if second.CacheValidation == nil || !second.CacheValidation.Called || second.CacheValidation.Duration < 0 {
		t.Fatalf("missing validation receipt: %+v", second.CacheValidation)
	}
}

func TestCachedRunnerKeepsIdentityFailureSeparateFromProviderOutcome(t *testing.T) {
	runner := &cacheFixtureRunner{identityErr: true}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	out, runErr := (cachedRunner{Runner: runner, cache: cache}).Run(context.Background(), cacheFixtureRequest(t.TempDir()))
	if runErr != nil {
		t.Fatal(runErr)
	}
	if out.CacheValidation == nil || out.CacheValidation.Error == "" || out.CacheValidation.Duration < 0 {
		t.Fatalf("identity error was not preserved: %+v", out.CacheValidation)
	}
}
func (r *cacheFixtureRunner) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	if r.started != nil {
		r.startOnce.Do(func() { close(r.started) })
		<-r.release
	}
	r.calls.Add(1)
	if r.runErr != nil {
		return r.runOutcome, r.runErr
	}
	generation, snapshot := r.resultGeneration, r.resultSnapshot
	if generation == 0 {
		generation, snapshot = 3, 4
	}
	result := map[string]any{"answer": "fixture"}
	if r.resultTruncated {
		result["truncated"] = true
	}
	if r.sourceTrimmed > 0 {
		result["source_trimmed"] = r.sourceTrimmed
	}
	out := contract.Outcome{Verdict: contract.VerdictOK, Result: result, Spent: contract.Sample{Duration: time.Second, Tokens: 7}, SpentUSD: 0.25, SpentUSDKnown: true, Evidence: []contract.QueryEvidence{{Completeness: "complete", ContentGeneration: generation, SnapshotID: snapshot, Freshness: "fresh"}}}
	if r.partial {
		out.Evidence[0].Truncated = true
	}
	return out, nil
}

func TestCachedRunnerFailedLeaderAndWaiterShareErrorWithoutDuplicateAccounting(t *testing.T) {
	physicalErr := errors.New("provider unavailable")
	runner := &cacheFixtureRunner{runErr: physicalErr, runOutcome: contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"answer": "useful"}, Spent: contract.Sample{Duration: time.Second, Tokens: 9}, SpentUSD: 0.4, SpentUSDKnown: true, Evidence: []contract.QueryEvidence{{Completeness: "complete", ContentGeneration: 3, SnapshotID: 4, Freshness: "fresh"}}}, started: make(chan struct{}), release: make(chan struct{}), identitySecond: make(chan struct{})}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: runner, cache: cache}
	req := cacheFixtureRequest(t.TempDir())
	type answer struct {
		out contract.Outcome
		err error
	}
	first := make(chan answer, 1)
	go func() { out, err := w.Run(context.Background(), req); first <- answer{out, err} }()
	<-runner.started
	second := make(chan answer, 1)
	go func() { out, err := w.Run(context.Background(), req); second <- answer{out, err} }()
	select {
	case <-runner.identitySecond:
	case <-time.After(time.Second):
		t.Fatal("waiter did not reach identity validation")
	}
	deadline := time.Now().Add(time.Second)
	for cache.InFlightWaiters() == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if cache.InFlightWaiters() == 0 {
		t.Fatal("waiter did not join the in-flight call")
	}
	close(runner.release)
	a, b := <-first, <-second
	if !errors.Is(a.err, physicalErr) || !errors.Is(b.err, physicalErr) {
		t.Fatalf("shared errors = %v / %v", a.err, b.err)
	}
	if a.out.Coalesced || a.out.CacheHit || !b.out.Coalesced || b.out.CacheHit {
		t.Fatalf("error flags = leader hit/coalesced %v/%v waiter %v/%v", a.out.CacheHit, a.out.Coalesced, b.out.CacheHit, b.out.Coalesced)
	}
	if a.out.Spent.Tokens != 9 || a.out.SpentUSD != 0.4 || !a.out.SpentUSDKnown || a.out.Result == nil {
		t.Fatalf("leader lost useful outcome: spent=%+v usd=%v known=%v result=%v", a.out.Spent, a.out.SpentUSD, a.out.SpentUSDKnown, a.out.Result)
	}
	if b.out.Spent.Tokens != 0 || b.out.SpentUSD != 0 || b.out.SpentUSDKnown {
		t.Fatalf("waiter duplicated provider spend: spent=%+v usd=%v known=%v", b.out.Spent, b.out.SpentUSD, b.out.SpentUSDKnown)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("physical provider calls = %d, want 1", runner.calls.Load())
	}
}

func TestCachedRunnerCoalescedPartialDoesNotDuplicateSpend(t *testing.T) {
	runner := &cacheFixtureRunner{partial: true, started: make(chan struct{}), release: make(chan struct{}), identitySecond: make(chan struct{})}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: runner, cache: cache}
	req := cacheFixtureRequest(t.TempDir())
	type answer struct {
		out contract.Outcome
		err error
	}
	first := make(chan answer, 1)
	go func() { out, err := w.Run(context.Background(), req); first <- answer{out, err} }()
	<-runner.started
	second := make(chan answer, 1)
	go func() { out, err := w.Run(context.Background(), req); second <- answer{out, err} }()
	select {
	case <-runner.identitySecond:
	case <-time.After(time.Second):
		t.Fatal("second consumer did not reach identity validation")
	}
	deadline := time.Now().Add(time.Second)
	for cache.InFlightWaiters() == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if cache.InFlightWaiters() == 0 {
		t.Fatal("waiter did not join the in-flight call")
	}
	close(runner.release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatalf("coalesced calls failed: %v / %v", a.err, b.err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("physical calls = %d, want 1", runner.calls.Load())
	}
	if a.out.CacheHit || b.out.CacheHit || a.out.Coalesced || !b.out.Coalesced {
		t.Fatalf("partial coalescing flags = hit %v/%v coalesced %v/%v", a.out.CacheHit, b.out.CacheHit, a.out.Coalesced, b.out.Coalesced)
	}
	if a.out.SpentUSD != 0.25 || a.out.Spent.Tokens != 7 {
		t.Fatalf("producer spend = %+v usd=%v", a.out.Spent, a.out.SpentUSD)
	}
	if b.out.SpentUSD != 0 || b.out.Spent.Tokens != 0 {
		t.Fatalf("waiter duplicated spend = %+v usd=%v", b.out.Spent, b.out.SpentUSD)
	}
}

func TestCachedRunnerCompleteLeaderWaiterThenStoredHit(t *testing.T) {
	runner := &cacheFixtureRunner{started: make(chan struct{}), release: make(chan struct{}), identitySecond: make(chan struct{})}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: runner, cache: cache}
	req := cacheFixtureRequest(t.TempDir())
	type answer struct {
		out contract.Outcome
		err error
	}
	first := make(chan answer, 1)
	go func() { out, err := w.Run(context.Background(), req); first <- answer{out, err} }()
	<-runner.started
	second := make(chan answer, 1)
	go func() { out, err := w.Run(context.Background(), req); second <- answer{out, err} }()
	select {
	case <-runner.identitySecond:
	case <-time.After(time.Second):
		t.Fatal("second consumer did not reach identity validation")
	}
	deadline := time.Now().Add(time.Second)
	for cache.InFlightWaiters() == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if cache.InFlightWaiters() == 0 {
		t.Fatal("waiter did not join the in-flight call")
	}
	close(runner.release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent calls failed: %v / %v", a.err, b.err)
	}
	if a.out.CacheHit || a.out.Coalesced {
		t.Fatalf("leader flags = hit %v coalesced %v", a.out.CacheHit, a.out.Coalesced)
	}
	if b.out.CacheHit || !b.out.Coalesced {
		t.Fatalf("waiter flags = hit %v coalesced %v", b.out.CacheHit, b.out.Coalesced)
	}
	third, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !third.CacheHit || third.Coalesced {
		t.Fatalf("stored flags = hit %v coalesced %v", third.CacheHit, third.Coalesced)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("physical provider calls = %d, want 1", runner.calls.Load())
	}
}

func TestCachedRunnerStatsCountsOnePhysicalAttemptForLeaderAndWaiter(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stats := toolstats.New(filepath.Join(state, "activity.sqlite"))
	runner := &cacheFixtureRunner{started: make(chan struct{}), release: make(chan struct{}), identitySecond: make(chan struct{})}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: statsRunner{Runner: runner, store: stats}, cache: cache}
	req := cacheFixtureRequest(root)
	first := make(chan error, 1)
	go func() { _, err := w.Run(context.Background(), req); first <- err }()
	<-runner.started
	second := make(chan error, 1)
	go func() { _, err := w.Run(context.Background(), req); second <- err }()
	select {
	case <-runner.identitySecond:
	case <-time.After(time.Second):
		t.Fatal("waiter did not reach identity validation")
	}
	time.Sleep(100 * time.Millisecond)
	close(runner.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("physical provider calls = %d, want 1", got)
	}
	snapshot, err := stats.Read(context.Background(), toolstats.Query{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var attempts int64
	for _, row := range snapshot.Rows {
		if row.Level == "attempt" && row.Name == req.Implementation.ID {
			attempts += row.Calls
		}
	}
	if attempts != 1 {
		var events, rollups int
		db, openErr := sql.Open("sqlite", "file:"+stats.Path+"?mode=ro")
		if openErr == nil {
			_ = db.QueryRow("SELECT count(*) FROM events").Scan(&events)
			_ = db.QueryRow("SELECT count(*) FROM rollups").Scan(&rollups)
			_ = db.Close()
		}
		t.Fatalf("attempt events = %d, want 1; rows=%+v sql_events=%d sql_rollups=%d", attempts, snapshot.Rows, events, rollups)
	}
}

func TestCacheableOutcomeRejectsStructuralPartialSignals(t *testing.T) {
	base := contract.Outcome{Verdict: contract.VerdictOK, Evidence: []contract.QueryEvidence{{Completeness: "complete", Freshness: "fresh", ContentGeneration: 1, SnapshotID: 1}}}
	for _, test := range []struct {
		name   string
		result map[string]any
	}{
		{name: "truncated", result: map[string]any{"truncated": true}},
		{name: "truncated_string", result: map[string]any{"truncated": "true"}},
		{name: "source_trimmed", result: map[string]any{"source_trimmed": 2}},
		{name: "nested_partial", result: map[string]any{"rows": []any{map[string]any{"partial": true}}}},
		{name: "numeric_incomplete", result: map[string]any{"incomplete": 1}},
		{name: "next_cursor", result: map[string]any{"next_cursor": "more"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := base
			out.Result = test.result
			if cacheableOutcome(out) {
				t.Fatal("structural partial result admitted to cache")
			}
		})
	}
}

func TestCachedRunnerDoesNotPublishResultFromRotatedGeneration(t *testing.T) {
	runner := &cacheFixtureRunner{generation: 1, snapshot: 1, resultGeneration: 2, resultSnapshot: 2}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: runner, cache: cache}
	req := cacheFixtureRequest(t.TempDir())
	if _, err := w.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if runner.calls.Load() != 2 || cache.Len() != 0 {
		t.Fatalf("rotated result was cached: calls=%d entries=%d", runner.calls.Load(), cache.Len())
	}
}

func cacheFixtureRequest(root string) contract.RunRequest {
	return contract.RunRequest{
		Capability:     contract.Capability{ID: "code.context", Version: contract.Version{Major: 1}, Effects: []contract.Effect{contract.EffectRead}, Inputs: []contract.Field{{Name: "task", Type: contract.TypeString}}},
		Implementation: contract.Implementation{ID: "fixture.context", Provider: "fixture", Capability: "code.context"},
		Repository:     contract.NewRepository("repo", root, []string{"go"}, contract.ScaleSmall, contract.VCSAbsent, nil),
		Payload:        map[string]any{"task": "find entry"}, Permission: contract.Permission{Task: "cache", Effects: []contract.Effect{contract.EffectRead}},
	}
}

func TestCachedRunnerInvalidatesOnSourceAndDoesNotCachePartial(t *testing.T) {
	runner := &cacheFixtureRunner{}
	cache, err := resultcache.New(resultcache.Config{MaxEntries: 8, MaxBytes: 1 << 20, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w := cachedRunner{Runner: runner, cache: cache}
	req := cacheFixtureRequest(t.TempDir())
	first, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || !second.CacheHit || runner.calls.Load() != 1 {
		t.Fatalf("hits=%v/%v calls=%d", first.CacheHit, second.CacheHit, runner.calls.Load())
	}
	if err := os.WriteFile(filepath.Join(req.Repository.Path, "changed.go"), []byte("package changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if third.CacheHit || runner.calls.Load() != 2 {
		t.Fatalf("source invalidation hit=%v calls=%d", third.CacheHit, runner.calls.Load())
	}
	runner.partial = true
	req.Payload["task"] = "partial"
	partial, err := w.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if partial.CacheHit || runner.calls.Load() != 3 {
		t.Fatalf("partial hit=%v calls=%d", partial.CacheHit, runner.calls.Load())
	}
}
