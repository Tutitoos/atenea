package floor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func measurement(repo, model string) floor.Measurement {
	return floor.Measurement{
		Repository:       repo,
		Model:            model,
		USD:              0.35,
		CacheWriteTokens: 25340,
		InputTokens:      120,
		OutputTokens:     40,
		CLIVersion:       "claude-code/1.2.3",
		MeasuredAt:       time.Date(2026, 8, 14, 19, 40, 0, 0, time.UTC),
	}
}

// A Put has to survive being read back by a different Store over the same
// path: the workflow engine that checks a floor and the `atenea floor
// measure` that wrote it are two different process runs, never one Store
// instance held open across both.
func TestPutAndGetRoundTripThroughARealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "floors.json")
	writer, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := measurement("atenea", "claude-opus-5")
	if err := writer.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reader, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, ok, err := reader.Get("atenea", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want the row Put just wrote")
	}
	if got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
}

// A re-measurement of the same pair is a replacement, not an addition -- a
// row half from one CLI version and half from another would not describe
// anything that was ever actually measured.
func TestPutReplacesTheSamePairAndLeavesOthersAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	other := measurement("atenea", "claude-sonnet-5")
	if err := store.Put(other); err != nil {
		t.Fatalf("Put other: %v", err)
	}
	original := measurement("atenea", "claude-opus-5")
	original.USD = 0.35
	if err := store.Put(original); err != nil {
		t.Fatalf("Put original: %v", err)
	}

	replacement := measurement("atenea", "claude-opus-5")
	replacement.USD = 0.41
	replacement.CLIVersion = "claude-code/1.3.0"
	replacement.MeasuredAt = original.MeasuredAt.Add(24 * time.Hour)
	if err := store.Put(replacement); err != nil {
		t.Fatalf("Put replacement: %v", err)
	}

	got, ok, err := store.Get("atenea", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want the replaced row")
	}
	if got != replacement {
		t.Errorf("Get = %+v, want the replacement %+v", got, replacement)
	}

	untouched, ok, err := store.Get("atenea", "claude-sonnet-5")
	if err != nil {
		t.Fatalf("Get other: %v", err)
	}
	if !ok {
		t.Fatal("Get other: ok = false, want the untouched row still there")
	}
	if untouched != other {
		t.Errorf("Get other = %+v, want it unchanged at %+v", untouched, other)
	}

	rows, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2: %+v", len(rows), rows)
	}
}

// A pair nobody has measured -- including every pair on a machine whose
// cache file was never written -- is the ordinary state a caller checks for
// before deciding the refusal is off, not a fault.
func TestGetOnAnUnknownPairIsAPlainNoNotAnError(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, ok, err := store.Get("atenea", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get on a file that was never written: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true on a file that was never written")
	}

	if err := store.Put(measurement("atenea", "claude-opus-5")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, ok, err = store.Get("other-repo", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get on an unmeasured repository: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true for a repository nothing measured")
	}
}

// List has to come back in the same order every time, independent of the
// order rows were Put in: the status screen prints it directly, and a
// listing that reordered itself from one run to the next would read as
// changing data rather than the same three rows read twice.
func TestListIsStableRegardlessOfPutOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, m := range []floor.Measurement{
		measurement("zeta", "claude-opus-5"),
		measurement("atenea", "claude-sonnet-5"),
		measurement("atenea", "claude-opus-5"),
	} {
		if err := store.Put(m); err != nil {
			t.Fatalf("Put %s/%s: %v", m.Repository, m.Model, err)
		}
	}

	first, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	second, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("List returned %d then %d rows, want 3 both times", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("row %d differed between two List calls: %+v vs %+v", i, first[i], second[i])
		}
	}
	want := []string{"atenea/claude-opus-5", "atenea/claude-sonnet-5", "zeta/claude-opus-5"}
	for i, m := range first {
		if got := m.Repository + "/" + m.Model; got != want[i] {
			t.Errorf("row %d = %q, want %q", i, got, want[i])
		}
	}
}

// A file that is not the JSON this package writes is refused by name, never
// silently treated as empty: silently starting over would erase every row a
// person or another tool put there without saying so.
func TestACorruptFileIsRefusedNamingThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, _, err := store.Get("atenea", "claude-opus-5"); err == nil {
		t.Fatal("Get on a corrupt file: want an error")
	} else if !containsPath(err, path) {
		t.Errorf("Get error %q does not name the corrupt file %q", err, path)
	}

	putErr := store.Put(measurement("atenea", "claude-opus-5"))
	if putErr == nil {
		t.Fatal("Put on a corrupt file: want an error, not a silent reset")
	}
	if !containsPath(putErr, path) {
		t.Errorf("Put error %q does not name the corrupt file %q", putErr, path)
	}
	if contract.KindOf(putErr) == contract.FailureUnspecified {
		t.Errorf("corrupt-file error was not sorted into a contract.Failure bin: %v", putErr)
	}

	// The refusal must not have touched the file it refused.
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read fixture back: %v", readErr)
	}
	if string(body) != "not json" {
		t.Errorf("corrupt file was rewritten to %q", body)
	}
}

func containsPath(err error, path string) bool {
	return err != nil && strings.Contains(err.Error(), path)
}

// Put on a fresh target directory -- one that does not exist until write
// creates it -- must leave nothing behind but the final file: no partial
// *.tmp is an artifact of the temp-then-rename path being exercised for the
// first time in that directory, and a leftover would mean the rename step
// was skipped rather than completed.
func TestWriteIsAtomicNoPartialFileInAFreshDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "atenea")
	path := filepath.Join(dir, "floors.json")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("fixture directory already exists: %v", err)
	}

	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Put(measurement("atenea", "claude-opus-5")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "floors.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want exactly [floors.json]", names)
	}
}
