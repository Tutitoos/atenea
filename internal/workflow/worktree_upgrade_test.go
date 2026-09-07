//go:build darwin || linux

package workflow

import "testing"

func TestSharedWorktreeLeaseUpgradeFailsFastAndThenSucceeds(t *testing.T) {
	root := t.TempDir()
	first, err := acquireWorktreeLease("first", root, false)
	if err != nil {
		t.Fatalf("first shared lease: %v", err)
	}
	defer first.Release()
	second, err := acquireWorktreeLease("second", root, false)
	if err != nil {
		t.Fatalf("second shared lease: %v", err)
	}
	if err := first.Upgrade(); err == nil {
		t.Fatal("upgrade succeeded while another reader held the worktree")
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release second lease: %v", err)
	}
	if err := first.Upgrade(); err != nil {
		t.Fatalf("upgrade after reader release: %v", err)
	}
}
