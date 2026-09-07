//go:build darwin || linux

package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkflowLocksShareStableUserRootAcrossProcessEnvironments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	profile := "stable-lock-root-" + strconv.Itoa(os.Getpid())
	slot, err := acquireGlobalSlot(context.Background(), profile, "agent", 1)
	if err != nil {
		t.Fatalf("parent global slot: %v", err)
	}
	defer slot.Release()
	lease, err := acquireWorktreeLease("test", root, true)
	if err != nil {
		t.Fatalf("parent worktree lease: %v", err)
	}
	defer lease.Release()

	lockRoot := ateneaLockRoot()
	globalDir, err := ensureAteneaLockDir("workflow-slots")
	if err != nil {
		t.Fatal(err)
	}
	worktreeDir, err := ensureAteneaLockDir("worktree-locks")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(globalDir) != lockRoot || filepath.Dir(worktreeDir) != lockRoot {
		t.Fatalf("lock directories do not share %q: global %q, worktree %q", lockRoot, globalDir, worktreeDir)
	}
	if !strings.HasPrefix(lockRoot, "/tmp/atenea-") {
		t.Fatalf("lock root %q is not an absolute per-user root", lockRoot)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestWorkflowLockHelperProcess", "--")
	cmd.Env = lockHelperEnv(map[string]string{
		"ATENEA_LOCK_HELPER":   "1",
		"ATENEA_LOCK_PROFILE":  profile,
		"ATENEA_LOCK_WORKTREE": root,
		"TMPDIR":               filepath.Join(t.TempDir(), "tmp-from-child"),
		"XDG_CACHE_HOME":       filepath.Join(t.TempDir(), "xdg-from-child"),
		"HOME":                 filepath.Join(t.TempDir(), "home-from-child"),
		"USER":                 "different-user-name",
		"LOGNAME":              "different-login-name",
	})
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child could acquire a lock through changed environment: %v\n%s", err, output)
	}
}

func TestWorkflowLockHelperProcess(t *testing.T) {
	if os.Getenv("ATENEA_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	profile := os.Getenv("ATENEA_LOCK_PROFILE")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	slot, err := acquireGlobalSlot(ctx, profile, "agent", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			slot.Release()
		}
		t.Fatalf("global slot under changed environment = %v, want deadline while parent holds it", err)
	}
	if _, err := acquireWorktreeLease("child", os.Getenv("ATENEA_LOCK_WORKTREE"), true); err == nil {
		t.Fatal("worktree lease under changed environment acquired while parent holds it")
	}
}

func lockHelperEnv(overrides map[string]string) []string {
	remove := make(map[string]bool, len(overrides))
	for key := range overrides {
		remove[key] = true
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || remove[key] {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}
