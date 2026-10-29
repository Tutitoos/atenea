package pidlock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestClaimReportsAndReleasesTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "atenea.lock")
	release, err := Claim(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := Holder(path); got != os.Getpid() {
		t.Fatalf("Holder = %d, want current pid %d", got, os.Getpid())
	}
	if !Alive(os.Getpid()) || Alive(0) || Alive(-1) {
		t.Fatal("Alive did not distinguish the current pid from non-positive pids")
	}
	release()
	if got := Holder(path); got != 0 {
		t.Fatalf("Holder after release = %d, want zero", got)
	}
}

func TestClaimRemovesAStaleLockButRefusesAnUnreadableOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.lock")
	if err := os.WriteFile(path, []byte("999999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Claim(path)
	if err != nil {
		t.Fatalf("Claim stale lock: %v", err)
	}
	release()
	if kernelLock {
		return // the file's contents never refuse a claim here; see the next test
	}

	if err := os.WriteFile(path, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Holder(path); got != 0 {
		t.Fatalf("Holder malformed lock = %d, want zero", got)
	}
	_, err = Claim(path)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("Claim malformed lock error = %v, want ErrHeld", err)
	}

	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Claim(path)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("Claim live lock error = %v, want ErrHeld", err)
	}
}

// A pid left in the file that now belongs to some unrelated live process (the
// normal state after a reboot) must not keep the service down; only the
// kernel's lock says who holds it, and a second claim while it is held fails.
func TestKernelLockDecidesNotTheRecycledPid(t *testing.T) {
	if !kernelLock {
		t.Skip("no advisory lock on this system")
	}
	path := filepath.Join(t.TempDir(), "recycled.lock")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Claim(path)
	if err != nil {
		t.Fatalf("Claim over a lock naming a live but unrelated pid: %v", err)
	}
	defer release()
	if _, err := Claim(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second Claim while held = %v, want ErrHeld", err)
	}
}

func TestClaimReturnsFilesystemErrors(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(filepath.Join(parent, "lock")); err == nil {
		t.Fatal("Claim through a file parent unexpectedly succeeded")
	}
}
