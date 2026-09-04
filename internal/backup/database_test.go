package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/dbaccess"
)

// TestDatabaseSnapshotsAreSelfContained checks the regression scenario: database snapshots are self contained.
func TestDatabaseSnapshotsAreSelfContained(t *testing.T) {
	for _, kind := range []string{"sqlite", "duckdb"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(source, "state.db")
			db, err := sql.Open(kind, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if kind == "sqlite" {
				if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec(`CREATE TABLE audit (id INTEGER PRIMARY KEY, value INTEGER); INSERT INTO audit VALUES(1,10)`); err != nil {
				t.Fatal(err)
			}
			if kind == "duckdb" {
				if _, err = db.Exec(`CHECKPOINT`); err != nil {
					t.Fatal(err)
				}
			}
			if got := databaseKind(path); got != kind {
				raw, _ := os.ReadFile(path)
				t.Fatalf("detected %q, header %x", got, raw[:16])
			}
			store, err := New(Options{Source: source, Dir: filepath.Join(root, "copies"), Keep: 3})
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 2; index++ {
				if _, err = db.Exec(`UPDATE audit SET value=value+1`); err != nil {
					t.Fatal(err)
				}
				var sourceValue int
				if err := db.QueryRow(`SELECT value FROM audit WHERE id=1`).Scan(&sourceValue); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s iteration %d source=%d", kind, index, sourceValue)
				if kind == "duckdb" {
					if err = db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				shot, err := store.Snapshot(t.Context(), time.Now().Add(time.Duration(index)*time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				probe, err := sql.Open(kind, filepath.Join(shot.Path, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				var snapshotValue int
				err = probe.QueryRow(`SELECT value FROM audit WHERE id=1`).Scan(&snapshotValue)
				_ = probe.Close()
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("snapshot=%d", snapshotValue)
				restored := filepath.Join(root, "restore"+string(rune('a'+index)))
				if err = store.Restore(t.Context(), shot.Name, restored); err != nil {
					t.Fatal(err)
				}
				copied, err := sql.Open(kind, filepath.Join(restored, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				var value int
				err = copied.QueryRow(`SELECT value FROM audit WHERE id=1`).Scan(&value)
				_ = copied.Close()
				if kind == "duckdb" {
					db, err = sql.Open(kind, path)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err != nil || value != 11+index {
					t.Fatalf("snapshot value %d: %v", value, err)
				}
			}
		})
	}
}

// TestSQLiteSnapshotDuringTransactionalWrites checks the regression scenario: sqlite snapshot during transactional writes.
func TestSQLiteSnapshotDuringTransactionalWrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE paired (id INTEGER PRIMARY KEY, value INTEGER); INSERT INTO paired VALUES(1,0),(2,0)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			if _, err := db.Exec(`UPDATE paired SET value=value+1`); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 3; i++ {
		target := filepath.Join(root, "snapshot"+string(rune('a'+i)))
		if err = snapshotDatabase(t.Context(), "sqlite", source, target); err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		copyDB, err := sql.Open("sqlite", target)
		if err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		var different int
		err = copyDB.QueryRow(`SELECT COUNT(DISTINCT value) FROM paired`).Scan(&different)
		_ = copyDB.Close()
		if err != nil || different != 1 {
			cancel()
			<-done
			t.Fatal("transaction split", different, err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestLiveDuckDBWALIsRefused checks the regression scenario: live duck dbwalis refused.
func TestLiveDuckDBWALIsRefused(t *testing.T) {
	source := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("duckdb", source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`CREATE TABLE audit (id INTEGER); CHECKPOINT; INSERT INTO audit VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err = snapshotDatabase(t.Context(), "duckdb", source, filepath.Join(t.TempDir(), "copy.db")); err == nil {
		t.Fatal("live WAL snapshot accepted")
	}
}

// TestSQLiteRollbackJournalIsNotRestored excludes the live rollback journal from snapshots.
func TestSQLiteRollbackJournalIsNotRestored(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA journal_mode=DELETE; CREATE TABLE items (value INTEGER); INSERT INTO items VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE items SET value=2`); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path + "-journal"); err != nil {
		t.Fatal("fixture lacks journal", err)
	}
	s, err := New(Options{Source: source, Dir: filepath.Join(root, "copies"), Keep: 1})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := s.Snapshot(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(shot.Path, "state.db-journal")); !os.IsNotExist(err) {
		t.Fatal("journal included", err)
	}
	restored, err := sql.Open("sqlite", filepath.Join(shot.Path, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var value int
	if err = restored.QueryRow(`SELECT value FROM items`).Scan(&value); err != nil || value != 1 {
		t.Fatalf("snapshot: %d %v", value, err)
	}
}

// TestSnapshotWaitsForDuckDBLeaseAndDoesNotPublishOnCancellation preserves older backups.
func TestSnapshotWaitsForDuckDBLeaseAndDoesNotPublishOnCancellation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "state.db")
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE items(value INTEGER);INSERT INTO items VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Source: source, Dir: filepath.Join(root, "copies"), Keep: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Snapshot(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	release, err := dbaccess.Acquire(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = s.Snapshot(ctx, time.Now().Add(time.Minute))
	_ = release()
	if err == nil {
		t.Fatal("snapshot ignored active writer")
	}
	shots, err := s.List()
	if err != nil || len(shots) != 1 || shots[0].Name != first.Name {
		t.Fatalf("partial published or old copy rotated: %+v %v", shots, err)
	}
	userFile := "notes" + dbaccess.Suffix
	if err = os.WriteFile(filepath.Join(source, userFile), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := s.Snapshot(t.Context(), time.Now().Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(second.Path, "state.db"+dbaccess.Suffix)); !os.IsNotExist(err) {
		t.Fatal("lock copied", err)
	}
	if body, readErr := os.ReadFile(filepath.Join(second.Path, userFile)); readErr != nil || string(body) != "keep" {
		t.Fatalf("ordinary suffix file missing: %q %v", body, readErr)
	}
}
