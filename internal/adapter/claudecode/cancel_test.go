package claudecode

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// slowStub stands in for a client that spawns helpers of its own -- a language
// server, an MCP server, a hook -- and does not come back quickly. The
// grandchild is the whole point: it inherits the pipe, so it is what used to
// keep Wait blocked long after the process Atenea started was dead.
//
// It answers --version at once, so what these tests time is the canceled turn
// and not the adapter's version probe waiting out its own ceiling.
func slowStub(t *testing.T, hold time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo '9.9.9 (Claude Code)'; exit 0;; esac\n" +
		"sleep " + seconds(hold) + " &\nwait\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return path
}

// seconds spells a duration the way sleep(1) wants it.
func seconds(d time.Duration) string {
	return strings.TrimSuffix(d.Truncate(time.Second/10).String(), "s")
}

func slowRunner(t *testing.T, hold time.Duration) *Runner {
	t.Helper()
	runner, err := New(Options{
		Binary:          slowStub(t, hold),
		Implementations: []string{"claude.search"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

// The bug as reported: ctrl-c after two seconds, and the trace said the
// provider took five minutes. Both halves are wrong. The bin is wrong because
// nothing timed out, and the sentence is wrong because it names a limit that
// was never reached -- it is the adapter reading its own configured ceiling
// off a context error it did not look at.
func TestCancelingIsNotATimeout(t *testing.T) {
	runner := slowRunner(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := runner.Run(ctx, request(t, map[string]any{"query": "TODO"}))

	if got := contract.KindOf(err); got != contract.FailureCanceled {
		t.Errorf("kind = %v, want canceled: nobody's provider did anything wrong", got)
	}
	if strings.Contains(err.Error(), "longer than") {
		t.Errorf("error = %q: a cancellation was reported as a deadline", err)
	}
}

// The other half of the same call, and the reason the first one was hard to
// notice: canceling did not actually stop anything. Go kills the process it
// started and none of its children, and Wait does not return while a survivor
// still holds the copy of stdout it inherited. Measured before the fix: a
// ctrl-c at two seconds returned after twenty-seven, which is how long the
// grandchild had left.
func TestCancelingReturnsWithoutWaitingForTheChild(t *testing.T) {
	// Well above the grace the fix allows, so a pass cannot be the child
	// simply finishing.
	runner := slowRunner(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, err := runner.Run(ctx, request(t, map[string]any{"query": "TODO"}))
	waited := time.Since(started)

	if err == nil {
		t.Fatal("a canceled call came back without an error")
	}
	// The budget is the grace the fix allows plus room for a loaded machine or
	// the race detector, and still a fraction of the child's own life. Before
	// the fix this call returned in thirty seconds, not a few seconds.
	if limit := procgroup.Grace + 5*time.Second; waited > limit {
		t.Errorf("canceling took %v, want under %v: the call waited for the child it had killed",
			waited.Truncate(time.Millisecond), limit)
	}
}

// A deadline the adapter itself set is a different fact and keeps its bin: the
// provider was given a limit and did not answer inside it. That one IS about
// the provider, and health is right to hear about it.
func TestAnExpiredDeadlineIsStillATimeout(t *testing.T) {
	runner, err := New(Options{
		Binary:          slowStub(t, 30*time.Second),
		Implementations: []string{"claude.search"},
		Timeout:         200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, runErr := runner.Run(context.Background(), request(t, map[string]any{"query": "TODO"}))

	if got := contract.KindOf(runErr); got != contract.FailureTimeout {
		t.Errorf("kind = %v, want timeout: the provider ran out of the time it was given", got)
	}
	if !strings.Contains(runErr.Error(), "longer than") {
		t.Errorf("error = %q, want the limit named", runErr)
	}
}

// A canceled context that arrives before the spawn must not be dressed up as
// a provider fault either. There is no provider in this story at all.
func TestAlreadyCanceledNeverBlamesTheProvider(t *testing.T) {
	runner := slowRunner(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Run(ctx, request(t, map[string]any{"query": "TODO"}))

	if got := contract.KindOf(err); got != contract.FailureCanceled {
		t.Errorf("kind = %v, want canceled", got)
	}
	var failure *contract.Failure
	if errors.As(err, &failure) && strings.Contains(failure.Message, "took") {
		t.Errorf("message = %q: nothing took any time", failure.Message)
	}
}

// The group kill is not the whole answer, and this is the case that proves the
// second half earns its place. A helper that daemonises -- setsid, which is
// what an MCP server or a language-server proxy does to outlive the shell that
// started it -- leaves the process group before the group is killed, and goes
// on holding the copy of stdout it inherited. Nothing Atenea signals can
// reach it. Only closing the pipes from this side ends the wait.
func TestAHelperThatLeavesTheGroupStillDoesNotHoldTheCall(t *testing.T) {
	if _, err := osexec.LookPath("setsid"); err != nil {
		t.Skipf("setsid is not on this machine: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo '9.9.9 (Claude Code)'; exit 0;; esac\n" +
		"setsid sleep 30 &\nsleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	runner, err := New(Options{Binary: path, Implementations: []string{"claude.search"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	if _, err := runner.Run(ctx, request(t, map[string]any{"query": "TODO"})); err == nil {
		t.Fatal("a canceled call came back without an error")
	}
	waited := time.Since(started)

	// Room for the grace to expire and not much more. Without it this waits
	// out the escapee, which is thirty seconds here and unbounded in life.
	if limit := procgroup.Grace + 5*time.Second; waited > limit {
		t.Errorf("canceling took %v, want under %v: an escaped helper held the call open",
			waited.Truncate(time.Millisecond), limit)
	}
}
