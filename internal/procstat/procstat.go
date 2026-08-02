// Package procstat is the one place in Atenea that knows what an operating
// system calls things.
//
// Everything else deals in bytes and durations. Weighing a finished process is
// the single question whose answer is shaped differently on every platform --
// different field widths, different units, and on some no answer at all -- so
// the difference is boxed here instead of leaking into the adapters, which are
// meant to be dumb translators and nothing more.
//
// Not knowing is a valid answer. A platform that will not say returns zero, and
// zero travels all the way to the database as NULL rather than as a claim that
// something used no memory.
package procstat

import "os"

// PeakRSS reports the high-water mark of resident memory, in bytes, of a
// process that has already finished. It returns zero when the platform does not
// report one.
func PeakRSS(state *os.ProcessState) int64 {
	if state == nil {
		return 0
	}
	return peakRSS(state)
}
