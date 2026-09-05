package kivgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestStatusRetriesMixedGenerationsWithoutRebuilding(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			repo := testRepo(t)
			fake, sess := newFakeKivgraph(t)
			mixed := strings.Replace(freshStatus(t, repo, 77, "stale"), `"generation":77`, `"generation":76`, 1)
			calls := 0
			fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
				calls++
				if persistent || calls == 1 {
					return mixed, false
				}
				return freshStatus(t, repo, 77, "fresh"), false
			}
			runner := newTestRunner(t, sess)
			status, err := runner.fetchStatus(t.Context(), sess)
			if persistent {
				if err == nil || !strings.Contains(err.Error(), "generation changed") || calls != 3 {
					t.Fatalf("persistent race: calls=%d status=%+v err=%v", calls, status, err)
				}
			} else if err != nil || !contentFresh(status) || calls != 2 {
				t.Fatalf("publication race: calls=%d status=%+v err=%v", calls, status, err)
			}
		})
	}
}

func TestIndexInventoryFailureNamesTheCauseWithoutLeakingRawOutput(t *testing.T) {
	runner := &Runner{}
	err := runner.failureFor(errors.New("private-token: source inventory changed during indexing; no fresh generation published"), t.Context())
	if !strings.Contains(err.Error(), "source files changed during indexing") || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("unsafe or misleading indexing error: %v", err)
	}
}

func TestFreshnessRebuildSurvivesPublicationRace(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	rebuilt, probes, indexes := false, 0, 0
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		if !rebuilt {
			return freshStatus(t, repo, 76, "stale"), false
		}
		probes++
		if probes == 1 {
			return strings.Replace(freshStatus(t, repo, 77, "stale"), `"generation":77`, `"generation":76`, 1), false
		}
		return freshStatus(t, repo, 77, "fresh"), false
	}
	runner := newTestRunner(t, sess)
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		indexes++
		rebuilt = true
		return IndexReport{Generation: "000077"}, nil
	}
	observed := &evidenceSession{Session: sess}
	initial, err := runner.fetchStatus(t.Context(), observed)
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := runner.ensureFresh(t.Context(), observed, initial, request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
	if err != nil || !contentFresh(status) || indexes != 1 || probes != 2 {
		t.Fatalf("rebuild: indexes=%d probes=%d status=%+v err=%v", indexes, probes, status, err)
	}
	foundMismatch := false
	for _, e := range observed.evidence {
		if e.SnapshotID == 77 && e.ContentGeneration == 76 {
			foundMismatch = true
		}
	}
	if !foundMismatch {
		t.Fatal("receipt lost the mismatched attestation generation")
	}
	// A subsequent check must use the verified generation without indexing.
	if _, _, err := runner.ensureFresh(t.Context(), observed, status, request(t, repo, CapabilityIntent, map[string]any{"intent": "test"})); err != nil || indexes != 1 {
		t.Fatalf("subsequent check rebuilt: indexes=%d err=%v", indexes, err)
	}
}

func TestMixedGenerationRetryHonorsCancellation(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		cancel()
		return strings.Replace(freshStatus(t, repo, 77, "fresh"), `"generation":77`, `"generation":76`, 1), false
	}
	_, err := newTestRunner(t, sess).fetchStatus(ctx, sess)
	if err == nil || len(fake.callsTo(toolStatus)) != 1 {
		t.Fatalf("retry ignored cancellation: %v", err)
	}
}

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
	var calls atomic.Int32
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		calls.Add(1)
		return IndexReport{}, errors.New("fixture rebuild failed")
	}
	if err := runner.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.CloseMaintenance)
	for i := 0; i < 2; i++ {
		_, err := runner.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
		runner.jobs.wg.Wait()
		if err == nil {
			t.Fatal("failed rebuild accepted")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("rebuilds=%d", calls.Load())
	}
}

func TestFailedFreshnessVerificationIsRearmedByChangedInputDigest(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	statusWithDigest := func(generation int, digest string) string {
		return strings.Replace(freshStatus(t, repo, generation, "stale"),
			`"state":"stale"`, `"input_digest":"`+digest+`","state":"stale"`, 1)
	}
	responses := []string{
		statusWithDigest(1, "input-a"),
		statusWithDigest(2, "input-b"),
		statusWithDigest(2, "input-c"),
	}
	var probes, indexes int
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		response := responses[probes]
		probes++
		return response, false
	}
	runner := newTestRunner(t, sess)
	runner.autoReindexRegistered = true
	runner.maintenanceDirectory = t.TempDir()
	runner.index = func(context.Context, string, string) (IndexReport, error) {
		indexes++
		if indexes == 1 {
			return IndexReport{Generation: "2"}, nil
		}
		return IndexReport{}, errors.New("second fixture rebuild stopped")
	}
	initial := &statusResult{SnapshotID: 1, Status: "ready", ContentFreshness: &contentFreshness{Generation: 1, State: "stale", InputDigest: "input-a"}}
	if _, _, err := runner.ensureFresh(t.Context(), sess, initial, request(t, repo, CapabilityIntent, map[string]any{"intent": "test"})); contract.CodeOf(err) != "freshness_unverified" {
		t.Fatalf("first verification error = %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(runner.maintenanceDirectory, "last-attempt"))
	if err != nil || string(marker) != "2:input-b" {
		t.Fatalf("marker = %q, err = %v", marker, err)
	}
	changed := &statusResult{SnapshotID: 2, Status: "ready", ContentFreshness: &contentFreshness{Generation: 2, State: "stale", InputDigest: "input-c"}}
	if _, _, err = runner.ensureFresh(t.Context(), sess, changed, request(t, repo, CapabilityIntent, map[string]any{"intent": "test"})); contract.CodeOf(err) == "rebuild_blocked" {
		t.Fatalf("changed input digest did not rearm maintenance: %v", err)
	}
	if indexes != 2 {
		t.Fatalf("rebuilds = %d, want 2", indexes)
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
