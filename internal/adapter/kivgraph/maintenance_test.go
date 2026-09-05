package kivgraph

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestBackgroundRebuildCoalescesAndExplicitWaitJoins(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	var fresh atomic.Bool
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		if fresh.Load() {
			return freshStatus(t, repo, 2, "fresh"), false
		}
		return freshStatus(t, repo, 1, "stale"), false
	}
	r := newTestRunner(t, sess)
	r.requireFresh = true
	r.autoReindexRegistered = true
	r.maintenanceDirectory = t.TempDir()
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r.index = func(ctx context.Context, _, _ string) (IndexReport, error) {
		calls.Add(1)
		close(started)
		select {
		case <-ctx.Done():
			return IndexReport{}, ctx.Err()
		case <-finish:
		}
		fresh.Store(true)
		return IndexReport{Generation: "2"}, nil
	}
	if err := r.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	defer r.CloseMaintenance()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
			if contract.CodeOf(err) != "maintenance_pending" {
				t.Errorf("query=%v", err)
			}
		}()
	}
	wg.Wait()
	<-started
	if calls.Load() != 1 {
		t.Fatalf("rebuilds=%d", calls.Load())
	}
	job, err := r.Maintenance()
	if err != nil || job.ID == "" || job.State != "running" {
		t.Fatalf("job=%+v %v", job, err)
	}
	close(finish)
	_, _, err = r.managedFresh(t.Context(), sess, &statusResult{SnapshotID: 1}, contract.RunRequest{Repository: repo, Capability: contract.Capability{ID: CapabilityEnsureFresh}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = r.Maintenance()
	if err != nil || job.State != "succeeded" || job.Generation != 2 {
		t.Fatalf("job=%+v %v", job, err)
	}
	if calls.Load() != 1 {
		t.Fatal("explicit wait started another rebuild")
	}
}

func TestServiceShutdownInterruptsOwnedRebuild(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "stale"), false)
	r := newTestRunner(t, sess)
	r.requireFresh = true
	r.autoReindexRegistered = true
	r.maintenanceDirectory = t.TempDir()
	started := make(chan struct{})
	r.index = func(ctx context.Context, _, _ string) (IndexReport, error) {
		close(started)
		<-ctx.Done()
		return IndexReport{}, ctx.Err()
	}
	if err := r.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
	if contract.CodeOf(err) != "maintenance_pending" {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	r.CloseMaintenance()
	job, err := r.Maintenance()
	if err != nil || job.State != "interrupted" {
		t.Fatalf("job=%+v %v", job, err)
	}
	if err := r.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	defer r.CloseMaintenance()
	_, err = r.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
	if contract.CodeOf(err) != "rebuild_blocked" {
		t.Fatal(err)
	}
}

func TestIndexPipeFailureDoesNotDiagnoseMCPAvailability(t *testing.T) {
	err := indexFailure(errors.New("reading Dart symbols: write |1: broken pipe"), t.Context())
	if err.Code != "index_worker_failed" || contract.AffectsHealth(err) {
		t.Fatal(err)
	}
}

func TestMaintenanceFailedGenerationRequiresNewInputsAndRetainsHistory(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	var digest atomic.Value
	digest.Store("input-a")
	fake.handlers[toolStatus] = func(map[string]any) (string, bool) {
		return strings.Replace(freshStatus(t, repo, 1, "stale"), `"state":"stale"`, `"state":"stale","input_digest":"`+digest.Load().(string)+`"`, 1), false
	}
	r := newTestRunner(t, sess)
	r.requireFresh = true
	r.autoReindexRegistered = true
	r.maintenanceDirectory = t.TempDir()
	var calls, verified atomic.Int32
	r.OnMaintenanceVerified = func() { verified.Add(1) }
	r.index = func(context.Context, string, string) (IndexReport, error) {
		calls.Add(1)
		return IndexReport{}, errors.New("Dart worker terminated")
	}
	if err := r.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	defer r.CloseMaintenance()
	query := func() error {
		_, err := r.Run(t.Context(), request(t, repo, CapabilityIntent, map[string]any{"intent": "test"}))
		return err
	}
	if err := query(); contract.CodeOf(err) != "maintenance_pending" {
		t.Fatal(err)
	}
	r.jobs.wg.Wait()
	first, err := r.Maintenance()
	if err != nil {
		t.Fatal(err)
	}
	if err := query(); contract.CodeOf(err) != "rebuild_blocked" {
		t.Fatal(err)
	}
	digest.Store("input-b")
	if err := query(); contract.CodeOf(err) != "maintenance_pending" {
		t.Fatal(err)
	}
	r.jobs.wg.Wait()
	second, err := r.Maintenance()
	if err != nil {
		t.Fatal(err)
	}
	historical, err := r.MaintenanceID(first.ID)
	if err != nil || historical.ID == second.ID || historical.State != "failed" || historical.Phase != "indexing" || calls.Load() != 2 || verified.Load() != 0 {
		t.Fatalf("history=%+v second=%+v calls=%d verified=%d err=%v", historical, second, calls.Load(), verified.Load(), err)
	}
}

func TestCanceledMaintenanceIsDistinctFromServiceInterruption(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, freshStatus(t, repo, 1, "stale"), false)
	r := newTestRunner(t, sess)
	r.requireFresh = true
	r.autoReindexRegistered = true
	r.maintenanceDirectory = t.TempDir()
	r.index = func(context.Context, string, string) (IndexReport, error) { return IndexReport{}, context.Canceled }
	if err := r.EnableBackground(); err != nil {
		t.Fatal(err)
	}
	defer r.CloseMaintenance()
	_, _, err := r.managedFresh(t.Context(), sess, &statusResult{SnapshotID: 1}, contract.RunRequest{Repository: repo, Capability: contract.Capability{ID: CapabilityEnsureFresh}})
	if contract.KindOf(err) != contract.FailureCanceled || contract.AffectsHealth(err) {
		t.Fatalf("canceled worker affected transport health: %v", err)
	}
	job, err := r.Maintenance()
	if err != nil || job.State != "canceled" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	_, _, err = r.managedFresh(t.Context(), sess, &statusResult{SnapshotID: 1}, contract.RunRequest{Repository: repo, Capability: contract.Capability{ID: CapabilityIntent}})
	if contract.CodeOf(err) != "rebuild_blocked" {
		t.Fatalf("canceled generation restarted automatically: %v", err)
	}
}
