package kivgraph

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func freshStatus(t *testing.T, repo contract.Repository, generation int, state string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(readyStatus("test", repo.Path)), &doc); err != nil {
		t.Fatal(err)
	}
	result := doc["results"].(map[string]any)
	result["snapshot_id"] = generation
	result["content_freshness"] = map[string]any{"generation": generation, "state": state}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestFreshnessWithoutAuthorizationNeverIndexes(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "stale"), false)
	runner := newTestRunner(t, sess)
	runner.requireFresh = true
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		t.Fatal("unauthorized rebuild")
		return IndexReport{}, nil
	}
	_, err := runner.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
	if contract.KindOf(err) != contract.FailureUnavailable || len(fake.callsTo(toolIntent)) != 0 {
		t.Fatalf("stale query dispatched: %v", err)
	}
}

func TestFailedAutomaticRebuildDoesNotLoop(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "stale"), false)
	runner := newTestRunner(t, sess)
	runner.requireFresh = true
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	var calls int
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		calls++
		return IndexReport{}, errors.New("fixture rebuild failed")
	}
	for i := 0; i < 2; i++ {
		_, err := runner.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
		if err == nil {
			t.Fatal("failed rebuild accepted")
		}
	}
	if calls != 1 {
		t.Fatalf("rebuilds=%d", calls)
	}
}

func TestConcurrentFreshnessChecksShareRebuild(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	stale := freshStatus(t, repo, 1, "stale")
	fresh := freshStatus(t, repo, 2, "fresh")
	var generation atomic.Int32
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		if generation.Load() == 2 {
			return fresh, false
		}
		return stale, false
	}
	runner := newTestRunner(t, sess)
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	var calls atomic.Int32
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		calls.Add(1)
		generation.Store(2)
		return IndexReport{Generation: "000002"}, nil
	}
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			status, err := runner.fetchStatus(t.Context(), sess)
			if err != nil {
				t.Error(err)
				return
			}
			_, _, err = runner.ensureFresh(t.Context(), sess, status, request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
			if err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("rebuilds=%d", calls.Load())
	}
}

func TestUnavailableInventoryNeverRebuilds(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "unavailable"), false)
	runner := newTestRunner(t, sess)
	runner.requireFresh = true
	runner.autoReindexRegistered = true
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		t.Fatal("missing root must block")
		return IndexReport{}, nil
	}
	_, err := runner.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
	if contract.KindOf(err) != contract.FailureUnavailable || len(fake.callsTo(toolIntent)) != 0 {
		t.Fatalf("unavailable inventory accepted: %v", err)
	}
}

func TestExplicitFreshVerificationReopensAutomaticMaintenance(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "fresh"), false)
	runner := newTestRunner(t, sess)
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	if err := os.WriteFile(filepath.Join(runner.maintenanceDirectory, "last-attempt"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls int
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		calls++
		fake.on(toolStatus, freshStatus(t, repo, 2, "fresh"), false)
		return IndexReport{Generation: "2"}, nil
	}
	status, _ := runner.fetchStatus(t.Context(), sess)
	req := contract.RunRequest{Repository: repo, Capability: contract.Capability{ID: CapabilityEnsureFresh}}
	if _, _, err := runner.ensureFresh(t.Context(), sess, status, req); err != nil {
		t.Fatal(err)
	}
	fake.on(toolStatus, freshStatus(t, repo, 1, "stale"), false)
	status, _ = runner.fetchStatus(t.Context(), sess)
	req.Capability.ID = CapabilityIntent
	if _, _, err := runner.ensureFresh(t.Context(), sess, status, req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("rebuilds = %d", calls)
	}
}

func TestSharedRebuildWaitHonorsTimeout(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "stale"), false)
	runner := newTestRunner(t, sess)
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		t.Fatal("duplicate rebuild")
		return IndexReport{}, nil
	}
	release, err := pidlock.Claim(filepath.Join(runner.maintenanceDirectory, "index.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	status, err := runner.fetchStatus(t.Context(), sess)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, _, err = runner.ensureFresh(ctx, sess, status, request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
	if contract.KindOf(err) != contract.FailureTimeout {
		t.Fatalf("timeout = %v", err)
	}
}
