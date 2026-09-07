package workflow

import (
	"context"
	"testing"
	"time"
)

func TestGlobalWorkflowSlotBlocksAcrossOwnersAndReleasesOnCancel(t *testing.T) {
	first, err := acquireGlobalSlot(context.Background(), "sha256:test-profile", "agent", 1)
	if err != nil {
		t.Fatal(err)
	}
	deferred := false
	defer func() {
		if !deferred {
			first.Release()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireGlobalSlot(ctx, "sha256:test-profile", "agent", 1); err == nil {
		t.Fatal("second owner acquired the occupied global lane")
	}
	first.Release()
	deferred = true
	second, err := acquireGlobalSlot(context.Background(), "sha256:test-profile", "agent", 1)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestUnlimitedWorkflowLaneDoesNotCreateGlobalSlot(t *testing.T) {
	slot, err := acquireGlobalSlot(context.Background(), "profile", "review", 0)
	if err != nil {
		t.Fatal(err)
	}
	slot.Release()
}
