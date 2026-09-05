package toolstats

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
INSERT OR IGNORE INTO meta VALUES ('started', ?);
CREATE TABLE IF NOT EXISTS events (
 id TEXT PRIMARY KEY, parent TEXT NOT NULL, level TEXT NOT NULL, tool TEXT NOT NULL,
 provider TEXT NOT NULL, repository TEXT NOT NULL, at INTEGER NOT NULL,
 ended INTEGER, duration INTEGER NOT NULL DEFAULT 0, outcome TEXT NOT NULL DEFAULT '',
 code TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS events_at ON events(at);
CREATE TABLE IF NOT EXISTS owners (id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS event_owners (event TEXT PRIMARY KEY, owner TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS event_owners_owner ON event_owners(owner);
CREATE TABLE IF NOT EXISTS catalogs (provider TEXT NOT NULL, tool TEXT NOT NULL, PRIMARY KEY(provider,tool));
CREATE TABLE IF NOT EXISTS discovered (provider TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS rollups (
 bucket INTEGER NOT NULL, level TEXT NOT NULL, tool TEXT NOT NULL, provider TEXT NOT NULL,
 repository TEXT NOT NULL, calls INTEGER NOT NULL, ok INTEGER NOT NULL, refused INTEGER NOT NULL,
 fail INTEGER NOT NULL, cancel INTEGER NOT NULL, dsum INTEGER NOT NULL, samples INTEGER NOT NULL,
 dmax INTEGER NOT NULL, last INTEGER NOT NULL,
 PRIMARY KEY(bucket,level,tool,provider,repository));
CREATE TABLE IF NOT EXISTS rollup_bounds (
 bucket INTEGER NOT NULL, level TEXT NOT NULL, tool TEXT NOT NULL, provider TEXT NOT NULL,
 repository TEXT NOT NULL, max_ended INTEGER NOT NULL,
 PRIMARY KEY(bucket,level,tool,provider,repository));
INSERT OR IGNORE INTO rollup_bounds
 SELECT bucket,level,tool,provider,repository,CAST(unixepoch('subsec')*1000000 AS INTEGER) FROM rollups;`

// Store is lazy: constructing or reading it never creates a database or starts a provider.
type Store struct {
	Path        string
	mu          sync.Mutex
	db          *sql.DB
	dropped     atomic.Int64
	compacted   time.Time
	owner       string
	ownerLock   *os.File
	maintenance atomic.Value
	writeError  atomic.Value
}

// New constructs a lazy activity store at path.
func New(path string) *Store { return &Store{Path: path} }

// Path returns the observability database path adjacent to routing metrics.
func Path(metricsPath string) string { return metricsPath + ".stats.sqlite" }

// dsn configures bounded SQLite caches, lock waiting, and immediate write transactions.
func dsn(path, mode string) string {
	u := url.URL{Scheme: "file", Path: path}
	extra := "&_pragma=temp_store(FILE)&_pragma=cache_size(-4096)"
	if mode == "rwc" {
		extra += "&_txlock=immediate"
	}
	return u.String() + "?mode=" + mode + "&_pragma=busy_timeout(2000)" + extra
}

// writer opens protected storage lazily and registers an exclusively locked writer.
func (s *Store) writer() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	if err := privateStorage(s.Path); err != nil {
		return nil, err
	}
	owner := uuid.NewString()
	lock, err := lockOwner(s.Path + "." + owner + ".lock")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = lock.Close()
			_ = os.Remove(s.Path + "." + owner + ".lock")
		}
	}()
	db, err := sql.Open("sqlite", dsn(s.Path, "rwc"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err == nil {
		var tx *sql.Tx
		tx, err = db.Begin()
		if err == nil {
			if _, err = tx.Exec(schema+metadataSchema, time.Now().UnixMicro()); err == nil {
				_, err = tx.Exec(`INSERT INTO owners VALUES(?)`, owner)
				if err == nil {
					err = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			} else {
				_ = tx.Rollback()
			}
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.owner, s.ownerLock = owner, lock
	success = true
	s.db = db
	return db, nil
}

// Close releases the database connection if it was opened.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if s.ownerLock != nil {
		_ = s.ownerLock.Close()
		s.ownerLock = nil
	}
	return err
}

// Call tracks one event and ensures it finishes at most once.
type Call struct {
	store *Store
	Event Event
	start time.Time
	once  sync.Once
}

// Begin records an active event and propagates its request context.
func (s *Store) Begin(ctx context.Context, e Event) (context.Context, *Call) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Metadata == (Metadata{}) {
		e.Metadata, _ = ctx.Value(metadataKey{}).(Metadata)
	}
	e.Metadata = e.Metadata.clean()
	e.ID = uuid.NewString()
	e.Parent = RequestID(ctx)
	if e.Level == "request" {
		ctx = WithRequest(ctx, e.ID)
	}
	c := &Call{store: s, Event: e, start: time.Now()}
	if s == nil {
		return ctx, c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.writer()
	if err != nil {
		message := Clean(contract.RedactRaw(err.Error()), 240)
		if previous, _ := s.writeError.Load().(string); previous != message {
			fmt.Fprintf(os.Stderr, "atenea stats recording unavailable: %s\n", message)
		}
		s.writeError.Store(message)
	} else {
		s.writeError.Store("")
	}
	if err == nil && time.Since(s.compacted) > time.Hour {
		s.compacted = time.Now()
		if maintenanceErr := s.compact(db, s.compacted); maintenanceErr != nil {
			reason := Clean(contract.RedactRaw(maintenanceErr.Error()), 240)
			s.maintenance.Store(reason)
			_, _ = db.Exec(`INSERT INTO meta VALUES('maintenance_failed',1),('maintenance_reason',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, reason)
		} else {
			s.maintenance.Store("")
			_, _ = db.Exec(`INSERT INTO meta VALUES('maintenance_failed',0) ON CONFLICT(key) DO UPDATE SET value=0`)
		}
	}
	if err == nil {
		var tx *sql.Tx
		tx, err = db.Begin()
		if err == nil {
			_, err = tx.Exec(`INSERT INTO events(id,parent,level,tool,provider,repository,at) VALUES(?,?,?,?,?,?,?)`, e.ID, e.Parent, e.Level, e.Tool, e.Provider, e.Repository, e.At.UnixMicro())
			if err == nil {
				_, err = tx.Exec(`INSERT INTO event_owners VALUES(?,?)`, e.ID, s.owner)
				if err == nil {
					err = writeMetadata(tx, e)
				}
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
	}
	if err != nil {
		s.dropped.Add(1)
	}
	return ctx, c
}

// Finish records the terminal outcome once, with sanitized diagnostics.
func (c *Call) Finish(outcome, code, reason string) {
	if c == nil || c.store == nil {
		return
	}
	c.once.Do(func() {
		s := c.store
		s.mu.Lock()
		defer s.mu.Unlock()
		db, err := s.writer()
		if err == nil {
			var tx *sql.Tx
			tx, err = db.Begin()
			if err == nil {
				var result sql.Result
				result, err = tx.Exec(`UPDATE events SET tool=?,provider=?,repository=?,ended=?,duration=?,outcome=?,code=?,reason=? WHERE id=?`, c.Event.Tool, c.Event.Provider, c.Event.Repository, time.Now().UnixMicro(), time.Since(c.start).Microseconds(), outcome, Clean(code, 80), Clean(contract.RedactRaw(reason), 240), c.Event.ID)
				if err == nil {
					n, _ := result.RowsAffected()
					if n == 0 {
						err = fmt.Errorf("missing stats event")
					}
				}
				if err == nil {
					err = writeMetadata(tx, c.Event)
				}
				if err == nil {
					err = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			}
		}
		if err != nil {
			s.dropped.Add(1)
		}
		// Persist a monotonic loss counter as soon as storage is writable again.
		if db != nil {
			if n := s.dropped.Swap(0); n > 0 {
				if _, e := db.Exec(`INSERT INTO meta VALUES('dropped',?) ON CONFLICT(key) DO UPDATE SET value=value+excluded.value`, n); e != nil {
					s.dropped.Add(n)
				}
			}
		}
	})
}

// End finishes the call using the structured error classification.
func (c *Call) End(err error) { outcome, code, reason := Outcome(err); c.Finish(outcome, code, reason) }

// Remember persists a provider catalog discovered during normal operation.
func (s *Store) Remember(provider string, names []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.writer()
	if err != nil {
		s.dropped.Add(1)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		s.dropped.Add(1)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM catalogs WHERE provider=?`, provider); err != nil {
		s.dropped.Add(1)
		return
	}
	for _, name := range names {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO catalogs VALUES(?,?)`, provider, name); err != nil {
			s.dropped.Add(1)
			return
		}
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO discovered VALUES(?)`, provider); err != nil {
		s.dropped.Add(1)
		return
	}
	if err = tx.Commit(); err != nil {
		s.dropped.Add(1)
	}
}

// Fold whole UTC days only; deleting detail and adding aggregates are one transaction.
func (s *Store) compact(db *sql.DB, now time.Time) error {
	cut := now.UTC().AddDate(0, 0, -7).Truncate(24 * time.Hour).UnixMicro()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = s.recoverOwners(tx, now, cut); err != nil {
		return err
	}
	if err := foldContext(tx, cut); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO rollup_bounds SELECT (at/86400000000)*86400000000,level,tool,provider,repository,max(ended)
 FROM events WHERE at<? AND ended IS NOT NULL GROUP BY 1,2,3,4,5
 ON CONFLICT(bucket,level,tool,provider,repository) DO UPDATE SET max_ended=max(max_ended,excluded.max_ended)`, cut)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO rollups SELECT (at/86400000000)*86400000000,level,tool,provider,repository,
 count(*),sum(outcome='ok'),sum(outcome='refused'),sum(outcome='fail'),sum(outcome='cancel'),
 sum(CASE WHEN outcome!='cancel' AND duration>=0 THEN duration ELSE 0 END),sum(outcome!='cancel' AND duration>=0),
 max(CASE WHEN outcome!='cancel' AND duration>=0 THEN duration ELSE 0 END),max(at)
 FROM events WHERE at<? AND ended IS NOT NULL GROUP BY 1,2,3,4,5
 ON CONFLICT(bucket,level,tool,provider,repository) DO UPDATE SET
 calls=calls+excluded.calls,ok=ok+excluded.ok,refused=refused+excluded.refused,
 fail=fail+excluded.fail,cancel=cancel+excluded.cancel,dsum=dsum+excluded.dsum,
 samples=samples+excluded.samples,dmax=max(dmax,excluded.dmax),last=max(last,excluded.last)`, cut)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM events WHERE at<? AND ended IS NOT NULL`, cut); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM event_owners WHERE event NOT IN (SELECT id FROM events)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM event_context WHERE event NOT IN (SELECT id FROM events)`); err != nil {
		return err
	}
	return tx.Commit()
}
