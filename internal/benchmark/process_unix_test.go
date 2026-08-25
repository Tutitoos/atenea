//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package benchmark

import "testing"

// getrusage reports ru_maxrss in bytes on macOS and in kilobytes on every
// other target this file is built for. The conversion used to key on linux, so
// the four BSDs in the build tag reported a peak 1024 times too small: a
// benchmark budget written in megabytes was unreachable there no matter how
// much memory the child actually took.
//
// Every target is checked from whichever one is running the tests, because the
// one that was wrong is the one nobody develops on.
func TestPeakMemoryIsInBytesOnEveryTargetThisFileBuildsFor(t *testing.T) {
	const peak = 4096
	for _, target := range []struct {
		goos string
		want int64
	}{
		{"darwin", peak},
		{"dragonfly", peak * 1024},
		{"freebsd", peak * 1024},
		{"linux", peak * 1024},
		{"netbsd", peak * 1024},
		{"openbsd", peak * 1024},
	} {
		if got := rssBytes(peak, target.goos); got != target.want {
			t.Errorf("%s: ru_maxrss %d became %d bytes, want %d",
				target.goos, peak, got, target.want)
		}
	}
}
