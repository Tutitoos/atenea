//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package benchmark

import "os"

type ProcessUsage struct {
	RSSBytes  int64
	CPUTimeMS float64
}

func Usage(*os.ProcessState) ProcessUsage {
	return ProcessUsage{}
}
