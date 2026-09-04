package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
