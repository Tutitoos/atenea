package main

import (
	"strings"
	"testing"
)

// A configuration without an index-capable provider reports that fact rather
// than silently claiming that every repository is ready.
func TestDetectReportsWhenNoAttachedProviderCanTell(t *testing.T) {
	out, err := cli(t, "--config", settingsFile(t), "detect")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(out, "no attached provider can report index readiness") {
		t.Fatalf("out = %q", out)
	}
}
