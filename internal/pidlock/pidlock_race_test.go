package pidlock

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// The lock is what keeps two `atenea run` processes off the same store, so the
// interesting moment is the one where several of them start at once against a
// lock left by a machine that was powered off: every one of them sees a dead
// pid and every one of them is entitled to clear it. Clearing the file and
// retrying let more than one of them create it with O_EXCL and walk away
// convinced it had the exclusion: this test caught that in three runs out of
// twenty against the old claim, which is why sixteen claimants and not two.
func TestExactlyOneOfManyClaimantsTakesOverAStaleLock(t *testing.T) {
	const claimants = 16
	path := filepath.Join(t.TempDir(), "atenea.lock")
	if err := os.WriteFile(path, []byte("999999999"), 0o600); err != nil {
		t.Fatal(err)
	}

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	var mu sync.Mutex
	taken := 0
	for range claimants {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			release, err := Claim(path)
			if err != nil {
				return
			}
			mu.Lock()
			taken++
			mu.Unlock()
			_ = release
		}()
	}
	start.Done()
	done.Wait()

	if taken != 1 {
		t.Fatalf("%d of %d concurrent claimants took the same stale lock; exactly one may", taken, claimants)
	}
	if got := Holder(path); got != os.Getpid() {
		t.Fatalf("the lock ended up naming %d, want this process %d", got, os.Getpid())
	}
}

// A lock file with no pid in it is wreckage from a claim that died between the
// O_EXCL create and the write, not a holder. Read as a holder -- which is what
// an unparsable file is deliberately read as -- it could never be cleared, and
// the service it guards could not be started again without deleting the file
// by hand.
func TestAnEmptyLockFileIsClearedRatherThanRefusedForever(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Claim(path)
	if err != nil {
		t.Fatalf("Claim over an empty lock file: %v", err)
	}
	defer release()
	if got := Holder(path); got != os.Getpid() {
		t.Fatalf("Holder = %d, want this process %d", got, os.Getpid())
	}
}

// The release closure outlives the claim it was handed for: a lock cleared as
// stale by somebody else already belongs to that somebody by the time this
// process gets around to shutting down, and removing the file then would take
// a live holder's claim away.
func TestReleaseLeavesALockThatHasSinceChangedHands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.lock")
	release, err := Claim(path)
	if err != nil {
		t.Fatal(err)
	}
	const successor = 424242
	if err := os.WriteFile(path, []byte(strconv.Itoa(successor)), 0o600); err != nil {
		t.Fatal(err)
	}
	release()
	if got := Holder(path); got != successor {
		t.Fatalf("Holder after a stale release = %d, want the successor %d still holding", got, successor)
	}
}

// Signal 0 answers EPERM for a process this user may not signal -- init, a
// root daemon, another user's shell. That is a process that exists, and
// reading it as gone would hand its lock to the next claimant.
func TestAProcessThisUserMayNotSignalCountsAsAlive(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: every pid is signalable, so EPERM cannot be provoked")
	}
	if !Alive(1) {
		t.Fatal("pid 1 was reported dead; it exists, this user just may not signal it")
	}

	path := filepath.Join(t.TempDir(), "init.lock")
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(path); err == nil {
		t.Fatal("a lock held by pid 1 was taken over as stale")
	}
}
