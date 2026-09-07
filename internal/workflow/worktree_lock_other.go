//go:build !linux && !darwin

package workflow

import "fmt"

type worktreeLease struct{}

func (l *worktreeLease) Release() error { return nil }

func (l *worktreeLease) Upgrade() error { return nil }

func acquireWorktreeLease(_, _ string, _ bool) (*worktreeLease, error) {
	return nil, fmt.Errorf("worktree locking is unsupported on this platform")
}
