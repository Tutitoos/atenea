//go:build !darwin && !linux

package mcpactivation

func acquireFileLock(string) (func(), error) { return func() {}, nil }
