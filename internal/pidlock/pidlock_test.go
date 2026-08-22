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

func TestClaimReturnsFilesystemErrors(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(filepath.Join(parent, "lock")); err == nil {
		t.Fatal("Claim through a file parent unexpectedly succeeded")
	}
}
