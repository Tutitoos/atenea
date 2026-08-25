package supervisor

import (
	"context"
	"testing"
	"time"
)

// A child that accepts the TCP connection and never answers used to hang the
// probe forever.
//
// readyNow passed context.Background() on both HTTP branches, and the default
// client that withDefaults supplies had no Timeout either, so the call blocked
// inside waitForReady's `case <-ticker.C`. From there neither ready_timeout nor
// stopCh was ever evaluated again: run() was parked, waitStopped polled a state
// that could not change, Supervisor.Stop never returned, and Core.Shutdown hung
// behind it. A refused connection fails instantly, which is why every ordinary
// test passed.
func TestAChildThatAcceptsAndNeverAnswersDoesNotHangTheProbe(t *testing.T) {
	spec := fakeSpec("mute", OnDemand, map[string]string{"FAKE_ACCEPT_AND_HANG": "1"})
	spec.ReadyTimeout = 700 * time.Millisecond
	p := newTestProcess(t, spec)

	landed := make(chan error, 1)
	go func() {
		_, err := p.ensureReady(context.Background())
		landed <- err
	}()

	select {
	case err := <-landed:
		if err == nil {
			t.Fatal("a server that never answered was reported ready")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ensureReady never returned: the probe is blocked on a child that will not answer")
	}
}

// And the stop path still works on one, which is the half that actually took
// the service down: a probe parked forever meant Stop() could never observe
// the state it was waiting for.
func TestAMuteChildCanStillBeStopped(t *testing.T) {
	spec := fakeSpec("mute-stop", OnDemand, map[string]string{"FAKE_ACCEPT_AND_HANG": "1"})
	spec.ReadyTimeout = 400 * time.Millisecond
	p := newTestProcess(t, spec)

	_, _ = p.ensureReady(context.Background())

	stopped := make(chan struct{})
	go func() {
		p.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop never returned on a child whose probe was in flight")
	}
}
