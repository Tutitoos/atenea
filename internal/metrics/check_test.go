package metrics

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestAnOpenedBaseAnswers(t *testing.T) {
	s := store(t, Options{})
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("a base that was just written to does not answer: %v", err)
	}
}

// The base was healthy when the store opened it. A check that remembers that
// answer instead of asking again is a check that passes on the one morning it
// was written for.
func TestABaseScribbledOverDoesNotAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.duckdb")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.WriteFile(path, []byte("\x00\x00\x00\x00half a write"), 0o600); err != nil {
		t.Fatalf("scribbling over the base: %v", err)
	}

	err = s.Check(context.Background())
	if err == nil {
		t.Fatal("a file that is no longer a database reported that it answers")
	}
	if kind := contract.KindOf(err); kind != contract.FailureUnavailable {
		t.Errorf("kind = %s, want unavailable: it is the bin that sets the base aside", kind)
	}
}

// Opening proves the file is there and nothing else. A base whose table is gone
// opens without a word and fails on the first thing anybody asks it.
func TestABaseMissingItsMeasurementTableDoesNotAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.duckdb")
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE unrelated (x INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Deliberately not through Open, which would migrate the table back into
	// existence and hide exactly the case under test.
	s := &Store{path: path, lockWait: DefaultLockWait}

	if err := s.Check(context.Background()); err == nil {
		t.Fatal("a base with no measurement table reported that it answers")
	}
}

func TestSetAsideFreesThePathAndKeepsTheWreckage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.duckdb")
	const wreckage = "whatever the ugly close left in here"
	if err := os.WriteFile(path, []byte(wreckage), 0o600); err != nil {
		t.Fatalf("writing the base: %v", err)
	}
	// Local noon in Madrid, so the stamp has to be the UTC one or the wreckage
	// of two machines cannot be lined up afterwards.
	madrid := time.FixedZone("CEST", 2*60*60)

	aside, err := SetAside(path, time.Date(2026, 8, 2, 14, 0, 0, 0, madrid))
	if err != nil {
		t.Fatalf("SetAside: %v", err)
	}
	if aside != path+".corrupt-20260802T120000Z" {
		t.Errorf("aside = %s, want the base's path with the UTC moment on it", aside)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the path is not free for a fresh base: %v", err)
	}
	kept, err := os.ReadFile(aside)
	if err != nil {
		t.Fatalf("the wreckage was not kept: %v", err)
	}
	if string(kept) != wreckage {
		t.Errorf("wreckage = %q, want it byte for byte", kept)
	}
}

// The whole point of stepping aside: Atenea starts. It starts without its
// history, which is a cold start and not a fault, and the history it lost is
// what the backups exist to put back.
func TestAFreshBaseOpensWhereTheBrokenOneWas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.duckdb")
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("writing the base: %v", err)
	}
	if _, err := Open(path, Options{}); err == nil {
		t.Fatal("a store opened on bytes that are not a database")
	}

	aside, err := SetAside(path, time.Now())
	if err != nil {
		t.Fatalf("SetAside: %v", err)
	}
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("nothing could be opened where the broken base was: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Check(context.Background()); err != nil {
		t.Errorf("the replacement does not answer either: %v", err)
	}
	if _, err := os.Stat(aside); err != nil {
		t.Errorf("the wreckage was thrown away instead of kept: %v", err)
	}
}

// A stale log left beside a fresh database is how the replacement gets
// corrupted too.
func TestSetAsideTakesTheWriteAheadLogWithIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.duckdb")
	sidecars := [...]string{".wal", "-wal"}
	if err := os.WriteFile(path, []byte("base"), 0o600); err != nil {
		t.Fatalf("writing the base: %v", err)
	}
	for _, suffix := range sidecars {
		if err := os.WriteFile(path+suffix, []byte("log"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", suffix, err)
		}
	}

	aside, err := SetAside(path, time.Now())
	if err != nil {
		t.Fatalf("SetAside: %v", err)
	}
	for _, suffix := range sidecars {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Errorf("%s stayed behind for the fresh base to trip over: %v", suffix, err)
		}
		if _, err := os.Stat(aside + suffix); err != nil {
			t.Errorf("the %s log did not travel with its base: %v", suffix, err)
		}
	}
}

// holdEnv turns this test binary into the second Atenea, and heldMark is how
// it says it has the lock. DuckDB holds its lock per process, so two handles
// inside this one do not conflict and the contention has to be real.
const (
	holdEnv  = "ATENEA_TEST_HOLD_BASE"
	heldMark = ".held"
)

// A base another Atenea is in the middle of writing is not a damaged base. The
// answer to a base that does not answer is to move it out of the way, so
// binning contention as damage would pull a healthy file out from under a live
// process -- and manufacture exactly the corruption this check exists to catch.
func TestABaseHeldByAnotherAteneaIsNotReportedAsDamaged(t *testing.T) {
	if base := os.Getenv(holdEnv); base != "" {
		holdTheBase(base)
		return
	}
	path := filepath.Join(t.TempDir(), "metrics.duckdb")
	// Open migrates and lets go again, so the lock is free for the holder.
	s, err := Open(path, Options{LockWait: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	holder := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=60s")
	holder.Env = append(os.Environ(), holdEnv+"="+path)
	if err := holder.Start(); err != nil {
		t.Fatalf("starting the other Atenea: %v", err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill() })
	waitForLock(t, path+heldMark)

	err = s.Check(context.Background())
	if err == nil {
		t.Fatal("a base locked by another process answered anyway")
	}
	if kind := contract.KindOf(err); kind != contract.FailureTimeout {
		t.Errorf("kind = %s, want timeout: unavailable would set a healthy base aside", kind)
	}
}

// holdTheBase is the other Atenea: take the lock, say so, and keep holding it
// until the parent has asked its question and killed us.
func holdTheBase(base string) {
	db, err := sql.Open("duckdb", base)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		os.Exit(4)
	}
	if err := os.WriteFile(base+heldMark, nil, 0o600); err != nil {
		os.Exit(5)
	}
	time.Sleep(30 * time.Second)
}

// waitForLock polls rather than sleeping a guessed interval: the holder pays
// for a DuckDB open, and a fixed wait would be either flaky or slow.
func waitForLock(t *testing.T, mark string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(mark); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the other Atenea never took the lock")
}
