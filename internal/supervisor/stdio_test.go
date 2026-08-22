package supervisor

// The tests here cover what changed when a Spec gained a Transport: the
// zero value must keep meaning exactly what it always meant, a stdio spec
// that also sets an http-only field must be refused rather than silently
// half-honored, and a real stdio child -- spawned, initialized, and reached
// through Supervisor.Session -- must behave like the http path always has,
// including burning its restart budget the same way when it never comes
// up.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The zero value of Spec.Transport must keep meaning exactly what it meant
// before Transport existed: an http spec, with Host defaulted the same way
// it always was.
func TestSpecWithDefaultsZeroValueTransportIsHTTP(t *testing.T) {
	built, err := Spec{ID: "x", Command: "true", Lifecycle: OnDemand}.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if built.Transport != TransportHTTP {
		t.Fatalf("Transport = %q, want the zero value to default to %q", built.Transport, TransportHTTP)
	}
	if built.Host == "" {
		t.Fatal("an http spec's Host was left empty by withDefaults")
	}
}

func TestSupervisorPublicLifecycleAndStatus(t *testing.T) {
	persistent := fakeSpec("persistent", Persistent, nil)
	onDemand := fakeSpec("on-demand", OnDemand, nil)
	sup, err := New(persistent, onDemand)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	endpoint, err := sup.Endpoint(persistent.ID)
	if err != nil || endpoint == "" {
		t.Fatalf("Endpoint = %q, %v", endpoint, err)
	}
	if _, err := sup.Endpoint("missing"); err == nil {
		t.Fatal("Endpoint accepted an unknown server")
	}
	if got := len(sup.Status()); got != 2 {
		t.Fatalf("initial status length = %d, want 2", got)
	}

	sup.WarmUp(ctx)
	if _, err := sup.EnsureReady(ctx, persistent.ID); err != nil {
		t.Fatalf("persistent EnsureReady: %v", err)
	}
	sup.Start(ctx)
	sup.Start(ctx)
	sup.Acquire(onDemand.ID)
	sup.Release(onDemand.ID)
	sup.Acquire("missing")
	sup.Release("missing")
	if got := sup.Status()[0].State; got != StateReady {
		t.Fatalf("persistent state = %v, want ready", got)
	}
	sup.Stop()
	sup.Stop()
	if got := sup.Status()[0].State; got != StateStopped {
		t.Fatalf("persistent final state = %v, want stopped", got)
	}
}

func TestStateStringCoversKnownAndUnknownValues(t *testing.T) {
	wants := map[State]string{
		StateStopped: "stopped", StateStarting: "starting", StateReady: "ready",
		StateRestarting: "restarting", StateDown: "down", State(255): "unknown",
	}
	for state, want := range wants {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}

// A stdio spec that also sets Host, Port or EndpointPath is almost always a
// config mistake -- a stdio server listens on nothing -- and withDefaults
// must say so rather than silently ignore fields that can never take
// effect.
func TestSpecWithDefaultsRejectsStdioWithHTTPOnlyFields(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
	}{
		{"Host", Spec{ID: "x", Command: "true", Lifecycle: OnDemand, Transport: TransportStdio, Host: "127.0.0.1"}},
		{"Port", Spec{ID: "x", Command: "true", Lifecycle: OnDemand, Transport: TransportStdio, Port: 4123}},
		{"EndpointPath", Spec{ID: "x", Command: "true", Lifecycle: OnDemand, Transport: TransportStdio, EndpointPath: "/mcp"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.spec.withDefaults(); err == nil {
				t.Fatalf("withDefaults accepted a stdio spec with %s set", tt.name)
			}
		})
	}
}

// A stdio child spawned by Supervisor becomes reachable through Session
// once EnsureReady returns: the handshake already happened as part of
// reaching ready, and a real tool call through the session Session hands
// back proves the pipe is not just open but actually answering.
func TestSupervisorStdioChildIsReachableViaSessionOnceReady(t *testing.T) {
	spec := fakeStdioSpec("stdio-ready", OnDemand, nil)
	sup, err := New(spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(sup.Stop)

	if _, err := sup.EnsureReady(context.Background(), spec.ID); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}

	sess, err := sup.Session(spec.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	got, err := sess.Call(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("Call through the returned session: %v", err)
	}
	if got != "pong" {
		t.Fatalf("Call result = %q, want %q", got, "pong")
	}
}

// Session refuses a caller before there is anything live to hand out --
// never started, and stopped again after having been -- landing on the same
// FailureUnavailable bin either way: both mean the identical thing to
// whoever is asking, which is that nothing is listening on the other end
// right now.
func TestSupervisorSessionFailsUnavailableWithoutALiveChild(t *testing.T) {
	tests := []struct {
		name  string
		start bool
	}{
		{name: "NeverStarted", start: false},
		{name: "StartedThenStopped", start: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := fakeStdioSpec("stdio-"+tt.name, OnDemand, nil)
			sup, err := New(spec)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(sup.Stop)

			if tt.start {
				if _, err := sup.EnsureReady(context.Background(), spec.ID); err != nil {
					t.Fatalf("EnsureReady: %v", err)
				}
				sup.Stop()
			}

			_, err = sup.Session(spec.ID)
			if err == nil {
				t.Fatal("Session succeeded with no live child")
			}
			if got := contract.KindOf(err); got != contract.FailureUnavailable {
				t.Fatalf("kind = %v, want %v", got, contract.FailureUnavailable)
			}
		})
	}
}

// A stdio child that exits the moment it is spawned -- before it can ever
// answer initialize -- is retried up to the budget and marked down once
// exhausted, exactly like an http server that crashes before answering its
// probe: the restart budget does not care which transport a spec used to
// never come up.
func TestProcessStdioChildThatExitsImmediatelyBurnsRestartBudgetToDown(t *testing.T) {
	spec := fakeStdioSpec("stdio-crashy", OnDemand, map[string]string{
		"FAKE_EXIT_AFTER_MS": "10",
		"FAKE_EXIT_CODE":     "9",
	})
	spec.ReadyTimeout = 500 * time.Millisecond
	spec.RestartLimit = 2
	spec.RestartDelay = 30 * time.Millisecond
	p := newTestProcess(t, spec)

	_, err := p.ensureReady(context.Background())
	if err == nil {
		t.Fatal("ensureReady succeeded against a child that exits immediately")
	}
	if !strings.Contains(err.Error(), "is down") {
		t.Fatalf("error %q does not say the server is down", err)
	}

	st := p.status()
	if st.State != StateDown {
		t.Fatalf("state = %v, want %v", st.State, StateDown)
	}
	if st.Restarts != 3 {
		t.Fatalf("Restarts = %d, want 3 (one initial attempt plus two retries)", st.Restarts)
	}
	if !strings.Contains(st.LastReason, "exited before answering ready") {
		t.Fatalf("LastReason = %q, missing the exit explanation", st.LastReason)
	}
}
