package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBackendMemoryPersistsProbeReadings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-health.json")
	first, err := newBackendMemory(path)
	if err != nil {
		t.Fatalf("newBackendMemory: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	first.record("serena", backendReading{State: BackendFailed, At: at, Reason: "connection refused"})

	second, err := newBackendMemory(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reading, ok := second.reading("serena")
	if !ok {
		t.Fatal("persisted reading was not restored")
	}
	if reading.State != BackendFailed || reading.Reason != "connection refused" || !reading.At.Equal(at) {
		t.Fatalf("reading after reopen = %+v, want persisted failure", reading)
	}
}
