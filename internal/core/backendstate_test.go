package core

import (
	"os"
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

// A reading that says the same thing as the last one must not reach the disk.
//
// persistLocked marshals the whole map, writes a temporary file, fsyncs it and
// renames it, inside the write lock -- and record runs once per backend on
// every tools/list of every open chat, plus once per raw tools/call. Almost all
// of those readings are identical to the one before them, so almost all of that
// work was writing the same bytes back over themselves. The check is on State
// and Reason and not on At, because the timestamp differs every time by
// construction and comparing it would make every reading unequal.
func TestAnUnchangedReadingIsNotWrittenAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-health.json")
	memory, err := newBackendMemory(path)
	if err != nil {
		t.Fatalf("newBackendMemory: %v", err)
	}
	memory.record("serena", backendReading{State: BackendOK, At: time.Now()})
	stamp := func() time.Time {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("the first reading never reached the disk: %v", err)
		}
		return info.ModTime()
	}
	first := stamp()
	// Coarse filesystem timestamps would make an immediate rewrite look like
	// no rewrite at all, so the second reading is deliberately later than the
	// resolution of the field being compared.
	time.Sleep(20 * time.Millisecond)
	for range 20 {
		memory.record("serena", backendReading{State: BackendOK, At: time.Now()})
	}
	if again := stamp(); !again.Equal(first) {
		t.Errorf("twenty identical readings rewrote the file at %s, first written at %s", again, first)
	}

	// A reading that does say something new is still written, which is the
	// half of this that the file exists for.
	memory.record("serena", backendReading{State: BackendFailed, At: time.Now(), Reason: "connection refused"})
	if changed := stamp(); changed.Equal(first) {
		t.Fatal("a backend going from ok to failed was never written down")
	}
	reopened, err := newBackendMemory(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := reopened.reading("serena"); !ok || got.State != BackendFailed || got.Reason != "connection refused" {
		t.Errorf("reloaded reading = %+v, %v, want the failure and its reason", got, ok)
	}
}
