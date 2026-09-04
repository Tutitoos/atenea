package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
	_ "modernc.org/sqlite"

	"github.com/Tutitoos/atenea/internal/dbaccess"
)

// databaseKind inspects a header, not an extension or arbitrary file contents.
func databaseKind(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var header [16]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return ""
	}
	if string(header[:]) == "SQLite format 3\x00" {
		return "sqlite"
	}
	if string(header[8:12]) == "DUCK" {
		return "duckdb"
	}
	return ""
}

// databaseSidecar excludes journals and coordination files from a standalone snapshot.
func databaseSidecar(path string) bool {
	if strings.HasSuffix(path, dbaccess.Suffix) {
		return true
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".wal"} {
		if strings.HasSuffix(path, suffix) && databaseKind(strings.TrimSuffix(path, suffix)) != "" {
			return true
		}
	}
	return false
}

// snapshotDatabase creates a self-contained engine-consistent copy. It never
// hardlinks mutable databases or copies their journal at a different instant.
func snapshotDatabase(ctx context.Context, kind, source, destination string) error {
	if kind == "duckdb" {
		release, err := dbaccess.Acquire(ctx, source, true)
		if err != nil {
			return err
		}
		defer func() { _ = release() }()
		// Separate DuckDB instances cannot safely snapshot a live in-process WAL.
		// The exclusive access lease prevents a new Atenea connection until the copy closes.
		if _, err := os.Stat(source + ".wal"); err == nil {
			return fmt.Errorf("backup: DuckDB writer is active; checkpoint and close before retrying")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	dsn := source
	if kind == "sqlite" {
		u := url.URL{Scheme: "file", Path: source}
		dsn = u.String() + "?mode=ro&_pragma=busy_timeout(2000)"
	}
	db, err := sql.Open(kind, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if kind == "sqlite" {
		if _, err = db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
			return err
		}
		copyDB, err := sql.Open(kind, destination)
		if err != nil {
			return err
		}
		defer func() { _ = copyDB.Close() }()
		var check string
		if err = copyDB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
			return err
		}
		if check != "ok" {
			return fmt.Errorf("backup integrity check failed")
		}
		return nil
	}
	var database string
	if err = db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return err
	}
	quoted := strings.ReplaceAll(destination, "'", "''")
	if _, err = db.ExecContext(ctx, `ATTACH '`+quoted+`' AS atenea_backup_snapshot`); err != nil {
		return err
	}
	defer func() { _, _ = db.ExecContext(context.WithoutCancel(ctx), `DETACH atenea_backup_snapshot`) }()
	_, err = db.ExecContext(ctx, `COPY FROM DATABASE "`+strings.ReplaceAll(database, `"`, `""`)+`" TO atenea_backup_snapshot`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `CHECKPOINT atenea_backup_snapshot`)
	return err
}
