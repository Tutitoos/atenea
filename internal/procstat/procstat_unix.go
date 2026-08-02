//go:build unix

package procstat

import (
	"os"
	"runtime"
	"syscall"
)

// peakRSS reads getrusage's high-water mark off the finished process.
//
// The units are the trap: POSIX never fixed them, so Linux and the BSDs report
// kilobytes while Darwin reports bytes. Getting this wrong is a silent
// thousandfold error in a number nobody would think to double-check, which is
// exactly why it lives in one function with its name on it.
func peakRSS(state *os.ProcessState) int64 {
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return 0
	}
	// Maxrss is int64 on 64-bit platforms and int32 on 32-bit ones, so the
	// conversion is a no-op on the machine the linter is looking at and load
	// bearing everywhere else.
	peak := int64(usage.Maxrss) //nolint:unconvert // width differs by platform
	if peak <= 0 {
		return 0
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		return peak
	}
	return peak * 1024
}
