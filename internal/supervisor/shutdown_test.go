package supervisor

// What a shutdown has to be true of, and none of it was covered before: that
// Stop is a latch and not just a pass over the list, that it comes back at all
// when a child has left something behind, and that a caller waiting on a
// server somebody else stopped is told so instead of waiting out its own
// deadline. Every one of these ends with a real process having been started
// and stopped, because every one of the defects they pin lived in the gap
// between what the state machine believed and what the operating system was
// actually doing.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Stop is what Core.Shutdown calls, and the promise it makes is that nothing
// this supervisor owns is running afterwards. It was not a latch: Stop closed
// its own channel, which only the idle reaper ever read, and any later
// EnsureReady found StateStopped, took it for a cold server and spawned a
// child with nobody left to stop it. procgroup.Isolate gives that child its
// own process group, so it outlives the Atenea that started it.
func TestNothingCanBeStartedThroughASupervisorThatHasStopped(t *testing.T) {
	s, err := New(fakeSpec("after-stop", OnDemand, nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Stop()

	if _, err := s.EnsureReady(context.Background(), "after-stop"); err == nil {
		t.Fatal("EnsureReady started a server after Stop had already returned")
	}
	// A child spawned in spite of the refusal would take a moment to appear,
	// so the absence is checked over time rather than once.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st := s.Status()[0]; st.PID != 0 || st.State == StateReady || st.State == StateStarting {
			t.Fatalf("a child was started after Stop: state %v, pid %d", st.State, st.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WarmUp's goroutines are the one caller that can still be on its way into
// ensureReady when the shutdown begins, and nothing used to wait for them:
// Stop walked the list, found every server stopped and returned, while a
// warm-up goroutine that had not been scheduled yet went on to start one.
//
// The second half calls WarmUp after Stop has already returned, because a
// goroutine that has not run yet and one that starts now are the same thing
// from the supervisor's side, and only this ordering pins the outcome instead
// of leaving it to the scheduler.
func TestWarmUpCannotLeaveAChildRunningBehindStop(t *testing.T) {
	spec := fakeSpec("warm-race", Persistent, map[string]string{"FAKE_DELAY_MS": "200"})
	s, err := New(spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.WarmUp(context.Background())
	s.Stop()
	s.WarmUp(context.Background())

	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st := s.Status()[0]; st.PID != 0 || st.State == StateReady || st.State == StateStarting {
			t.Fatalf("a warm-up goroutine started a child after Stop returned: state %v, pid %d", st.State, st.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A child that has escaped is the case that took the service down: cmd.Stdout
// and cmd.Stderr are a *ring, not an *os.File, so os/exec copies them through
// a pipe and cmd.Wait does not return until the write end is closed. A
// grandchild that called setsid inherited that write end and left the group
// Terminate and Kill can reach, so Wait blocked for as long as it lived, and
// Stop -- and Core.Shutdown behind it -- blocked with it.
//
// The stop still has to be a clean one. Coming back within the bound but
// giving up on the child would satisfy a timing assertion on its own, which is
// why the state afterwards is checked too: Stopped means the activation ended
// and was reaped, Down means the shutdown walked away from a process it could
// not account for.
func TestStopReturnsCleanlyWhenAnEscapedGrandchildHoldsTheOutputPipe(t *testing.T) {
	spec := fakeSpec("leaky", OnDemand, map[string]string{"FAKE_LEAK_GRANDCHILD": "1"})
	p := newTestProcess(t, spec)
	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	stopped := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		p.stop()
		stopped <- time.Since(start)
	}()
	var took time.Duration
	select {
	case took = <-stopped:
	case <-time.After(8 * time.Second):
		t.Fatal("stop never returned: it is waiting on a pipe a survivor of the process group still holds")
	}
	if st := p.status(); st.State != StateStopped {
		t.Fatalf("state after a %s stop = %v, want %v: the child was given up on at the bound rather than reaped",
			took, st.State, StateStopped)
	}
}

// The other half of the same promise, from below: even when nothing ever
// answers, stop comes back. A child wedged in an uninterruptible wait -- a
// read against a hung mount is the usual one -- does not die on SIGKILL
// either, so there is a real shape behind this state that no signal can
// resolve. Modeled by hand rather than provoked, because the only ways to
// wedge a process for real need a mount or a driver this suite cannot have:
// an activation left in Starting with no goroutine behind it is what the
// state machine sees in both cases.
func TestStopGivesUpOnAnActivationThatWillNeverEnd(t *testing.T) {
	built, err := fakeSpec("wedged", OnDemand, nil).withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	p := newProcess(built)
	p.mu.Lock()
	p.state = StateStarting
	p.stopCh = make(chan struct{})
	p.mu.Unlock()

	returned := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		p.stop()
		returned <- time.Since(start)
	}()
	select {
	case took := <-returned:
		if bound := p.stopBound(); took > bound+time.Second {
			t.Fatalf("stop took %s on an activation that never ends, past its %s bound", took, bound)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop never returned: it is polling a state that nothing is left to change")
	}
	if st := p.status(); st.State != StateDown {
		t.Fatalf("state after giving up = %v, want %v: a child nothing could account for is not stopped", st.State, StateDown)
	}
}

// A caller waiting for a server that is stopped under it -- by the idle
// reaper, or by a shutdown -- used to keep waiting. ensureReady only handled
// Ready and Down inside its loop, and nothing moves a process out of Stopped
// on its own, so the caller spun on the tick until its own context expired and
// then reported a deadline that said nothing about what had happened.
func TestEnsureReadyGivesUpWhenTheServerIsStoppedUnderIt(t *testing.T) {
	spec := fakeSpec("stopped-under", OnDemand, map[string]string{"FAKE_DELAY_MS": "4000"})
	spec.ReadyTimeout = 10 * time.Second
	p := newTestProcess(t, spec)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	landed := make(chan error, 1)
	go func() {
		_, err := p.ensureReady(ctx)
		landed <- err
	}()
	waitFor(t, waitCeiling, func() bool { return p.status().State == StateStarting })
	p.requestStop()

	select {
	case err := <-landed:
		if err == nil {
			t.Fatal("ensureReady reported a server ready that had just been stopped")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ensureReady waited out its own context instead of noticing the stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ensureReady kept waiting on a process that had been stopped under it")
	}
}

// The same promise from the other end: a shutdown that lands while a child is
// still coming up has to return, and a spawn attempt has ready_timeout to run
// out -- ten seconds here -- before it would end on its own.
func TestStopReturnsWhileAChildIsStillStartingUp(t *testing.T) {
	spec := fakeSpec("slow-start", OnDemand, map[string]string{"FAKE_DELAY_MS": "4000"})
	spec.ReadyTimeout = 10 * time.Second
	s, err := New(spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _, _ = s.EnsureReady(context.Background(), "slow-start") }()
	waitFor(t, waitCeiling, func() bool { return s.Status()[0].State == StateStarting })

	returned := make(chan struct{})
	go func() {
		s.Stop()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned on a child that was still starting up")
	}
	if st := s.Status()[0]; st.PID != 0 {
		t.Fatalf("a child was left running after Stop: pid %d", st.PID)
	}
}

// The idle reaper had no test at all: the only coverage of the idle path was
// stopIfIdle called by hand, which skips both things that can go wrong here --
// whether the reaper runs at all, and whether it respects work in flight.
// Acquire is what a caller holds across a call it is making against the
// server, and reaping one mid-call would kill the very process being used.
func TestTheIdleReaperStopsAnUnusedServerButNotOneInUse(t *testing.T) {
	previous := idleCheckEvery
	idleCheckEvery = 20 * time.Millisecond
	t.Cleanup(func() { idleCheckEvery = previous })

	// A whole second of idle timeout, not the 120ms fakeSpec uses elsewhere,
	// because a process counts as idle from before it existed: lastUsed is set
	// when the process is constructed and a spawn takes a couple of hundred
	// milliseconds, so a shorter window has the reaper stopping the server
	// between it answering ready and the caller ever getting the answer. That
	// cannot happen with the real numbers -- a five-minute window against a
	// spawn measured in milliseconds -- and reproducing it here would be
	// testing the test.
	spec := fakeSpec("idle", OnDemand, nil)
	spec.IdleTimeout = time.Second
	s, err := New(spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	if _, err := s.EnsureReady(ctx, "idle"); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	s.Acquire("idle")
	time.Sleep(1500 * time.Millisecond)
	if st := s.Status()[0]; st.State != StateReady {
		t.Fatalf("state while a call was in flight = %v, want %v: the reaper stopped a server in use", st.State, StateReady)
	}

	s.Release("idle")
	waitFor(t, waitCeiling, func() bool { return s.Status()[0].State == StateStopped })
	if st := s.Status()[0]; st.PID != 0 {
		t.Fatalf("pid after the reaper stopped the server = %d, want zero", st.PID)
	}
	// The endpoint survives the stop, which is what makes an on_demand server
	// restartable at all: the adapter that was built with this URL is still
	// holding it, and a later EnsureReady has to bring the server back up
	// behind the same one.
	if endpoint, err := s.Endpoint("idle"); err != nil || endpoint == "" {
		t.Fatalf("Endpoint after an idle stop = %q, %v", endpoint, err)
	}
}

// A stdio child has no address to poll, so readiness is the MCP handshake
// itself -- and a handshake cannot be repeated. Driving it from the readiness
// tick bounded every attempt to 150ms, so a child that took longer than that
// to answer had its answer thrown away and was sent another initialize, under
// a new id, on the next tick: a protocol error to a server that checks, and a
// server that never came up at all as far as the supervisor was concerned.
func TestASlowStdioChildIsSentOneInitializeAndComesUp(t *testing.T) {
	log := filepath.Join(t.TempDir(), "initializes")
	spec := fakeStdioSpec("slow-handshake", OnDemand, map[string]string{
		"FAKE_INITIALIZE_DELAY_MS": "600",
		"FAKE_INITIALIZE_LOG":      log,
	})
	spec.ReadyTimeout = 4 * time.Second
	p := newTestProcess(t, spec)

	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("a child that answers the handshake in 600ms was not accepted: %v", err)
	}
	if sent := countInitializes(t, log); sent != 1 {
		t.Fatalf("the child was sent %d initialize requests, want exactly 1", sent)
	}
}

func countInitializes(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the handshake log: %v", err)
	}
	return len(strings.Fields(string(raw)))
}
