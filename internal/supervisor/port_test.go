package supervisor

import (
	"net"
	"strconv"
	"testing"
)

// A fixed port is returned as-is, with no listen attempt at all -- proven
// here with a host that could never be listened on, so the test would fail
// the moment choosePort tried.
func TestChoosePortFixedNeverProbesTheHost(t *testing.T) {
	got, err := choosePort("not-a-real-host.invalid", 9999)
	if err != nil {
		t.Fatalf("choosePort with a fixed port returned an error: %v", err)
	}
	if got != 9999 {
		t.Fatalf("choosePort = %d, want the fixed 9999 unchanged", got)
	}
}

// Port zero asks the OS for one, and the answer has to actually be usable:
// this is the port an adapter will be built against for the rest of the
// Supervisor's life.
func TestChoosePortZeroAsksTheOSForAUsableOne(t *testing.T) {
	got, err := choosePort("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("choosePort(0) returned an error: %v", err)
	}
	if got == 0 {
		t.Fatal("choosePort(0) returned 0, want a real port")
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(got)))
	if err != nil {
		t.Fatalf("the port choosePort returned could not be listened on: %v", err)
	}
	_ = l.Close()
}

// withPort substitutes the placeholder and leaves everything else in an arg
// list untouched -- including a port a user typed directly, which needs no
// substitution because it never carries the placeholder token at all.
func TestWithPortSubstitutesOnlyThePlaceholder(t *testing.T) {
	in := []string{"serve", "--port", portPlaceholder, "--fixed-port", "9000"}
	out := withPort(in, 4123)
	want := []string{"serve", "--port", "4123", "--fixed-port", "9000"}
	if len(out) != len(want) {
		t.Fatalf("withPort returned %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("withPort()[%d] = %q, want %q", i, out[i], want[i])
		}
	}
	// The input must survive untouched: it is a Spec's own field, read
	// again on every restart.
	if in[2] != portPlaceholder {
		t.Fatalf("withPort mutated its input: in[2] = %q", in[2])
	}
}

func TestWithPortNilForEmptyArgs(t *testing.T) {
	if got := withPort(nil, 4123); got != nil {
		t.Fatalf("withPort(nil, ...) = %v, want nil", got)
	}
}
