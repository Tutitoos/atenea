//go:build darwin || linux

package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type globalSlot struct {
	file *os.File
	once *sync.Once
}

func (s globalSlot) Release() {
	if s.file == nil {
		return
	}
	unlock := func() {
		_ = syscall.Flock(int(s.file.Fd()), syscall.LOCK_UN)
		_ = s.file.Close()
	}
	if s.once == nil {
		unlock()
		return
	}
	s.once.Do(unlock)
}

// acquireGlobalSlot reserves one OS lock in the profile/pool lane. flock is
// released by the kernel when the owning process crashes, so a stale PID can
// never permanently consume capacity. capacity=0 retains the historical unlimited
// behavior and does not create a lock file.
func acquireGlobalSlot(ctx context.Context, profile, pool string, capacity int) (globalSlot, error) {
	if capacity <= 0 {
		return globalSlot{}, nil
	}
	for {
		slot, acquired, err := tryAcquireGlobalSlot(profile, pool, capacity)
		if err != nil {
			return globalSlot{}, err
		}
		if acquired {
			return slot, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return globalSlot{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func tryAcquireGlobalSlot(profile, pool string, capacity int) (globalSlot, bool, error) {
	if capacity <= 0 {
		return globalSlot{}, true, nil
	}
	key := profile + "\x00" + pool
	sum := sha256.Sum256([]byte(key))
	dir, err := ensureAteneaLockDir("workflow-slots")
	if err != nil {
		return globalSlot{}, false, fmt.Errorf("workflow global slots: %w", err)
	}
	for index := 0; index < capacity; index++ {
		name := filepath.Join(dir, hex.EncodeToString(sum[:])[:32]+fmt.Sprintf("-%d.lock", index))
		file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return globalSlot{}, false, fmt.Errorf("workflow global slots: %w", err)
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return globalSlot{file: file, once: &sync.Once{}}, true, nil
		}
		_ = file.Close()
	}
	return globalSlot{}, false, nil
}

func profileSlotIdentity(profileName, profileDigest string) string {
	if profileDigest != "" {
		return profileDigest
	}
	if profileName != "" {
		return profileName
	}
	return "workflow-v1"
}
