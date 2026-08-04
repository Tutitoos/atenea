package checkpoint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestLockRefusesASecondHolder(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	release, err := store.Lock("20260802T120000-abc123")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()

	if _, err := store.Lock("20260802T120000-abc123"); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("second Lock kind = %v, want unavailable: %v", contract.KindOf(err), err)
	}
}

func TestLockReleaseFreesIt(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	release, err := store.Lock("20260802T120000-abc123")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	release()

	second, err := store.Lock("20260802T120000-abc123")
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	second()
}

// A lock left behind by a process that never got to release it -- a crash,
// not a clean stop -- must not permanently block the run it was protecting.
func TestLockClearsAHolderThatNoLongerExists(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A process that has already exited leaves a pid nothing answers to.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("true: %v", err)
	}
	dead := cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(dir, "20260802T120000-abc123.lock"),
		[]byte(strconv.Itoa(dead)), 0o600); err != nil {
		t.Fatalf("seeding a stale lock: %v", err)
	}

	release, err := store.Lock("20260802T120000-abc123")
	if err != nil {
		t.Fatalf("Lock over a dead holder: %v", err)
	}
	release()
}

func TestLockDisabledStoreNeverBlocks(t *testing.T) {
	store, err := checkpoint.New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	release, err := store.Lock("20260802T120000-abc123")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	release()
}

func TestLockRefusesUnsafeIds(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := store.Lock("../escape"); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}
