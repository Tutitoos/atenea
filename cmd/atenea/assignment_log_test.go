package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The recorder passes the bytes through: an agent must read exactly what the
// spawn sent it, recorded or not.
func TestTheAssignmentIsRecordedAndStillReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATENEA_ASSIGNMENT_LOG", dir)

	const card = `{"contract":"3.0.0","id":"a","type":"plan"}`
	got, err := io.ReadAll(recordAssignment("plan", strings.NewReader(card)))
	if err != nil {
		t.Fatalf("reading through the recorder: %v", err)
	}
	if string(got) != card {
		t.Errorf("the agent was handed %q, want %q", got, card)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*-plan-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want one recorded assignment, got %v (%v)", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if string(raw) != card {
		t.Errorf("recorded %q, want the bytes as sent %q", raw, card)
	}
}

// Unset is the normal case: nothing written, bytes untouched.
func TestNoAssignmentRecordWithoutADirectory(t *testing.T) {
	t.Setenv("ATENEA_ASSIGNMENT_LOG", "")
	const card = `{"id":"a"}`
	got, err := io.ReadAll(recordAssignment("plan", strings.NewReader(card)))
	if err != nil || string(got) != card {
		t.Fatalf("got %q (%v), want the card unchanged", got, err)
	}
}

// A stdin that failed halfway must reach the agent as a read failure, not as a
// short card it would report as malformed.
func TestAFailedReadStaysAFailure(t *testing.T) {
	t.Setenv("ATENEA_ASSIGNMENT_LOG", t.TempDir())
	broken := io.MultiReader(strings.NewReader(`{"id":`), &failingReader{})
	if _, err := io.ReadAll(recordAssignment("plan", broken)); err == nil {
		t.Error("a broken stdin was passed on as a clean short card")
	}
}

// The recorder has to be wired into the spawn's entry point, not merely
// exist: this drives cmdAgentRun itself. The agent is handed a card it will
// reject, because what is under test is that the bytes were recorded on the
// way in, whatever the agent then makes of them.
func TestTheSpawnEntryPointRecords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATENEA_ASSIGNMENT_LOG", dir)

	_ = cmdAgentRun("filereader", strings.NewReader(`{"id":"not-a-valid-card"}`), io.Discard)

	files, err := filepath.Glob(filepath.Join(dir, "*-filereader-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("cmdAgentRun recorded %v (%v), want one card", files, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
