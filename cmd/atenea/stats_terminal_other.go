//go:build !darwin && !linux

package main

import "os"

func statsTerminalWidth(*os.File) int { return 0 }
