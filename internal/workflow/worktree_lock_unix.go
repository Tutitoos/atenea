//go:build linux || darwin

package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// worktreeLease is a kernel-owned lock. Closing the descriptor, including
// because the process crashed, releases it for the next workflow.
type worktreeLease struct {
	file *os.File
}

// Upgrade changes a shared lease into an exclusive lease without closing the
// descriptor. Keeping the same open file description closes the validation to
// mutation gap: a resume cannot release its read lock while another workflow
// acquires the checkout before the write expansion is applied.
func (l *worktreeLease) Upgrade() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("worktree is busy")
		}
		return fmt.Errorf("worktree lock: %w", err)
	}
	return nil
}

func (l *worktreeLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// acquireWorktreeLease canonicalizes the repository before naming the lock.
// EvalSymlinks makes two settings entries that reach the same checkout share
// one lock, while the digest keeps lock filenames bounded and opaque.
func acquireWorktreeLease(_ string, root string, exclusive bool) (*worktreeLease, error) {
	if root == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("worktree path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("worktree path: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, fmt.Errorf("worktree path: %w", err)
	}
	digest := sha256.Sum256([]byte(canonical))
	dir, err := ensureAteneaLockDir("worktree-locks")
	if err != nil {
		return nil, fmt.Errorf("worktree lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(digest[:])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("worktree lock: %w", err)
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("worktree is busy")
		}
		return nil, fmt.Errorf("worktree lock: %w", err)
	}
	return &worktreeLease{file: file}, nil
}
