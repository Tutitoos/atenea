//go:build !darwin && !linux

package main

import "os"

// statsTerminalWidth returns the terminal width, or zero when unavailable.
func statsTerminalWidth(*os.File) int { return 0 }
