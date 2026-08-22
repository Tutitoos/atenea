//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package benchmark

import (
	"os"
	"runtime"
	"syscall"
)

type ProcessUsage struct {
	RSSBytes  int64
	CPUTimeMS float64
}

func Usage(state *os.ProcessState) ProcessUsage {
	if state == nil {
		return ProcessUsage{}
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return ProcessUsage{}
	}
	rss := int64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	cpu := float64(usage.Utime.Sec)*1000 + float64(usage.Utime.Usec)/1000
	cpu += float64(usage.Stime.Sec)*1000 + float64(usage.Stime.Usec)/1000
	return ProcessUsage{RSSBytes: rss, CPUTimeMS: cpu}
}
