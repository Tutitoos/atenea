package supervisor

import "testing"

// A crash reason is only as good as the tail it kept. These two behaviors --
// bounded to the limit, trimmed of the blank lines a subprocess leaves
// around its one useful line -- are the entire contract ring promises
// annotate.

func TestRingKeepsOnlyTheTailPastItsLimit(t *testing.T) {
	r := newRing(8)
	_, _ = r.Write([]byte("0123"))
	_, _ = r.Write([]byte("456789"))
	if got, want := r.String(), "23456789"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestRingWriteNeverFailsEvenOverLimit(t *testing.T) {
	r := newRing(4)
	n, err := r.Write([]byte("far more than the limit allows"))
	if err != nil {
		t.Fatalf("Write returned an error: %v", err)
	}
	if n != len("far more than the limit allows") {
		t.Fatalf("Write reported %d bytes, want the full length", n)
	}
	if got, want := r.String(), "lows"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestRingTrimsSurroundingBlankLines(t *testing.T) {
	r := newRing(1024)
	_, _ = r.Write([]byte("\n\n  panic: boom\n\n"))
	if got, want := r.String(), "panic: boom"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestRingEmptyStringWhenNothingWritten(t *testing.T) {
	r := newRing(64)
	if got := r.String(); got != "" {
		t.Fatalf("String() on an empty ring = %q, want empty", got)
	}
}
