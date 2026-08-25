//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package benchmark

import (
	"os"
	"runtime"
	"syscall"
)

// ProcessUsage contains resource usage collected after a child process exits.
type ProcessUsage struct {
	RSSBytes  int64
	CPUTimeMS float64
}

// Usage converts the operating system process state into portable metrics.
func Usage(state *os.ProcessState) ProcessUsage {
	if state == nil {
		return ProcessUsage{}
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return ProcessUsage{}
	}
	cpu := float64(usage.Utime.Sec)*1000 + float64(usage.Utime.Usec)/1000
	cpu += float64(usage.Stime.Sec)*1000 + float64(usage.Stime.Usec)/1000
	return ProcessUsage{RSSBytes: rssBytes(usage.Maxrss, runtime.GOOS), CPUTimeMS: cpu}
}

// rssBytes puts ru_maxrss into bytes for the operating system that reported it.
//
// Darwin is the exception, not Linux. getrusage returns ru_maxrss in bytes on
// macOS and in kilobytes everywhere else this file is built for, so the
// conversion has to name darwin and multiply for the rest. It used to name
// linux instead, which left every BSD in the build tag above -- dragonfly,
// freebsd, netbsd, openbsd -- reporting a peak memory figure 1024 times
// smaller than the truth, and a benchmark budget in megabytes could not be
// exceeded there however much the child allocated.
//
// The operating system is a parameter rather than read here, so the rule can
// be checked for every target this file builds for from whichever one is
// running the tests.
func rssBytes(maxrss int64, goos string) int64 {
	if goos == "darwin" {
		return maxrss
	}
	return maxrss * 1024
}
