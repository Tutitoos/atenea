package procstat

import (
	"os/exec"
	"runtime"
	"testing"
)

// The figure has to be a plausible number of bytes for a real process. This is
// the test that catches the units being wrong by a factor of a thousand, which
// is the whole hazard this package exists to contain.
func TestAFinishedProcessIsWeighedInBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no getrusage on this platform")
	}
	binary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no binary to weigh: %v", err)
	}
	cmd := exec.Command(binary, "version")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	peak := PeakRSS(cmd.ProcessState)
	if peak == 0 {
		t.Skip("this platform does not report a peak")
	}
	// A megabyte is below anything a real binary can start in; a hundred
	// gigabytes is above anything a `go version` could reach. Either bound
	// being crossed means the units are wrong, not that the process was
	// unusual.
	const floor, ceiling = 1 << 20, 100 << 30
	if peak < floor || peak > ceiling {
		t.Fatalf("peak = %d bytes, which is not a plausible size for a process", peak)
	}
}

// A process that never ran cannot be weighed, and asking must not panic.
func TestNothingToWeighIsZero(t *testing.T) {
	if got := PeakRSS(nil); got != 0 {
		t.Fatalf("PeakRSS(nil) = %d, want 0", got)
	}
}
