//go:build !unix

package procstat

import "os"

// peakRSS has nothing to read on a platform without getrusage. Saying so is the
// honest answer; the alternative is a zero that reads like a measurement.
func peakRSS(*os.ProcessState) int64 { return 0 }
