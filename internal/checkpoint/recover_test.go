package checkpoint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"

	"github.com/Tutitoos/atenea/internal/checkpoint"
)

func storeAt(t *testing.T, dir string) *checkpoint.Store {
	t.Helper()
	s, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", filepath.Base(path), err)
	}
}

// The defer that removes the temporary file covers an error return and not a
// SIGKILL, so this is what the disk looks like after an ugly close.
func TestAnInterruptedDumpIsSweptAndTheReceiptBesideItSurvives(t *testing.T) {
	dir := t.TempDir()
	store := storeAt(t, dir)
	good := run("20260802T120000-abc123")
	if err := store.Save(good); err != nil {
		t.Fatalf("Save: %v", err)
	}
	interrupted := good.ID + ".2416055379.tmp"
	writeFile(t, filepath.Join(dir, interrupted), `{"id":"20260802T120`)

	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Swept != 1 || rec.Torn != 0 {
		t.Fatalf("recovery = %+v, want one dump swept and nothing torn", rec)
	}
	if len(rec.Files) != 1 || rec.Files[0] != interrupted {
		t.Errorf("Files = %v, want the swept dump named for the incident report", rec.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, interrupted)); !os.IsNotExist(err) {
		t.Errorf("the interrupted dump is still on disk: %v", err)
	}
	if _, err := store.Load(good.ID); err != nil {
		t.Fatalf("the receipt beside it did not survive the sweep: %v", err)
	}
}

// Deleting the torn receipt would destroy the only evidence of what the ugly
// close cost, so it is moved out of the loader's way instead of removed.
func TestATornReceiptIsSetAsideAndKeptAsEvidence(t *testing.T) {
	dir := t.TempDir()
	store := storeAt(t, dir)
	const id = "20260802T120000-abc123"
	const garbage = `{"id":"20260802T120000-abc123","task":"find every`
	writeFile(t, filepath.Join(dir, id+".json"), garbage)

	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Torn != 1 || rec.Swept != 0 {
		t.Fatalf("recovery = %+v, want one receipt torn and nothing swept", rec)
	}
	if len(rec.Files) != 1 || rec.Files[0] != id+".json.torn" {
		t.Errorf("Files = %v, want where the evidence went", rec.Files)
	}
	kept, err := os.ReadFile(filepath.Join(dir, id+".json.torn"))
	if err != nil {
		t.Fatalf("the evidence of the ugly close is gone: %v", err)
	}
	if string(kept) != garbage {
		t.Errorf("the evidence was rewritten on the way: %q", kept)
	}
	if _, err := store.Load(id); err == nil {
		t.Error("the torn receipt still loads as a run")
	}
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("List = %v, want the torn receipt out of the runs", ids)
	}
}

// Resuming a torn run is a dead end, and the reason it is a dead end is a file
// sitting in the same directory. Measured before this held: `atenea resume`
// answered "no such file or directory" for the .json, which is true and sends
// the reader to the one path that has nothing on it.
func TestResumingATornRunNamesTheEvidenceItLeftBehind(t *testing.T) {
	dir := t.TempDir()
	store := storeAt(t, dir)
	const id = "20260802T120000-abc123"
	writeFile(t, filepath.Join(dir, id+".json"), `{"id":"20260802T120000-abc123","task":"find every`)
	if _, err := store.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	_, err := store.Load(id)
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", contract.KindOf(err))
	}
	aside := filepath.Join(dir, id+".json.torn")
	if !strings.Contains(err.Error(), aside) {
		t.Errorf("error = %q, want the path of the file that is actually there (%s)", err, aside)
	}

	// A run that never existed is a different fact and must not borrow this
	// sentence: there is no evidence to point at.
	_, err = store.Load("20260101T000000-000000")
	if strings.Contains(err.Error(), "torn") {
		t.Errorf("error = %q, want a plain missing-run answer for a run nobody wrote", err)
	}
}

// An ordinary write failure must not borrow the disk sentence. The ENOSPC side
// of the same rule is in disk_test.go, in-package: a full filesystem cannot be
// produced honestly from a unit test without mounting one.
func TestAPlainWriteFailureDoesNotBlameTheDisk(t *testing.T) {
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	store := storeAt(t, filepath.Join(readOnly, "runs"))
	err := store.Save(checkpoint.Run{ID: "20260802T120000-abc124", Kind: "ask"})
	if err == nil {
		t.Skip("this user can write into a directory with mode 0500")
	}
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", contract.KindOf(err))
	}
	if strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("error = %q, want a plain write failure for a directory nobody may write", err)
	}
}

// A cut that lands after the braces close leaves a file that parses and
// describes no run at all. It is as unusable as one that does not parse.
func TestAReceiptThatNamesNoRunIsTorn(t *testing.T) {
	dir := t.TempDir()
	store := storeAt(t, dir)
	writeFile(t, filepath.Join(dir, "20260802T120000-abc123.json"), "{}\n")

	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Torn != 1 {
		t.Fatalf("recovery = %+v, want a receipt that names no run set aside", rec)
	}
}

// Recovery runs before every commission, including the thousands that follow a
// clean stop. It must not cost the good receipts anything.
func TestAGoodReceiptComesThroughRecoveryUntouched(t *testing.T) {
	dir := t.TempDir()
	store := storeAt(t, dir)
	original := run("20260802T120000-abc123")
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, original.ID+".json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Swept != 0 || rec.Torn != 0 || len(rec.Files) != 0 {
		t.Fatalf("recovery = %+v, want a clean directory left alone", rec)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the receipt is gone: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("the receipt was replaced by another file of the same name")
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the receipt was rewritten")
	}
	read, err := store.Load(original.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if read.Task != original.Task || len(read.Steps) != len(original.Steps) {
		t.Errorf("run = %+v, want the one that was dumped", read)
	}
}

// The pass runs at every start, not only the one after the crash.
func TestASecondRecoveryFindsNothingLeft(t *testing.T) {
	dir := t.TempDir()
	store := storeAt(t, dir)
	writeFile(t, filepath.Join(dir, "20260802T120000-abc123.4155.tmp"), `{"id":`)
	writeFile(t, filepath.Join(dir, "20260802T110000-def456.json"), "not json at all")

	first, err := store.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if first.Swept != 1 || first.Torn != 1 {
		t.Fatalf("first recovery = %+v, want one swept and one torn", first)
	}
	second, err := store.Recover()
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	if second.Swept != 0 || second.Torn != 0 || len(second.Files) != 0 {
		t.Fatalf("second recovery = %+v, want a directory already put right", second)
	}
}

func TestADisabledStoreRecoversNothing(t *testing.T) {
	store := storeAt(t, "")
	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("checkpointing off reported a failure: %v", err)
	}
	if rec.Swept != 0 || rec.Torn != 0 || len(rec.Files) != 0 {
		t.Errorf("recovery = %+v, want nothing from a store that writes nothing", rec)
	}
}

// An Atenea that has never been given work has nothing that could be half
// written, and recovery must not be what finally creates the folder.
func TestAMissingDirectoryIsNothingToRecover(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	store := storeAt(t, dir)

	rec, err := store.Recover()
	if err != nil {
		t.Fatalf("a directory that was never written reported a failure: %v", err)
	}
	if rec.Swept != 0 || rec.Torn != 0 || len(rec.Files) != 0 {
		t.Errorf("recovery = %+v, want nothing", rec)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("recovery created the directory it had nothing to recover from")
	}
}
