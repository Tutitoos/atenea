//go:build darwin || linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// statsTerminalWidth returns the terminal width, or zero when unavailable.
func statsTerminalWidth(f *os.File) int {
	w, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(w.Col)
}
