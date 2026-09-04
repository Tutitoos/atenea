package toolstats

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMaintenanceFailurePreservesCalls exercises a persistent rollup failure.
func TestMaintenanceFailurePreservesCalls(t *testing.T) {
	s := testStore(t)
	_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "old", At: time.Now().AddDate(0, 0, -10)})
	c.End(nil)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_rollup BEFORE INSERT ON rollups BEGIN SELECT RAISE(FAIL,'maintenance fault'); END`); err != nil {
		t.Fatal(err)
	}
	s.compacted = time.Time{}
	for i := 0; i < 3; i++ {
		_, c = s.Begin(context.Background(), Event{Level: "request", Tool: "new"})
		c.End(nil)
	}
	out := snapshot(t, s, Query{})
	if r := total(t, out, "request"); r.Calls != 4 || r.OK != 4 || out.Coverage.Dropped != 0 {
		t.Fatalf("%+v %+v", r, out.Coverage)
	}
	if time.Since(s.compacted) > time.Minute || !strings.Contains(strings.Join(out.Coverage.Notes, " "), "maintenance failed") {
		t.Fatalf("missing backoff/diagnostic: %+v", out.Coverage)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_rollup`); err != nil {
		t.Fatal(err)
	}
	s.compacted = time.Time{}
	_, c = s.Begin(context.Background(), Event{Level: "request", Tool: "new"})
	c.End(nil)
	if r := total(t, snapshot(t, s, Query{}), "request"); r.Calls != 5 {
		t.Fatal(r)
	}
}

// TestStatsCrashWriter is a child process intentionally terminated without Close.
func TestStatsCrashWriter(t *testing.T) {
	path := os.Getenv("ATENEA_STATS_CRASH_TEST_PATH")
	if path == "" {
		return
	}
	s := New(path)
	s.Begin(context.Background(), Event{Level: "request", Tool: "crashed", At: time.Now().AddDate(0, 0, -12)})
	if s.db == nil {
		os.Exit(2)
	}
	fmt.Println("ready")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
	os.Exit(0)
}

// TestCrashRecoveryProtectsLiveWritersAndCompactsOnce uses a real OS writer lock.
func TestCrashRecoveryProtectsLiveWritersAndCompactsOnce(t *testing.T) {
	s := testStore(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestStatsCrashWriter$")
	cmd.Env = append(os.Environ(), "ATENEA_STATS_CRASH_TEST_PATH="+s.Path)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill() })
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child %q %v", line, err)
	}
	_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "current"})
	c.End(nil)
	if r := total(t, snapshot(t, s, Query{}), "request"); r.Active != 1 || r.Calls != 1 {
		t.Fatal(r)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	for i := 0; i < 2; i++ {
		if err = s.compact(s.db, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	out := snapshot(t, s, Query{})
	r := total(t, out, "request")
	if r.Active != 0 || r.Calls != 2 || r.Fail != 1 || r.OK != 1 || r.Samples != 1 || r.P95US != nil || !out.Coverage.Partial {
		t.Fatalf("%+v %+v", r, out.Coverage)
	}
	_ = s.Close()
	out = snapshot(t, New(s.Path), Query{})
	if total(t, out, "request").Calls != 2 {
		t.Fatal("restart changed totals")
	}
}

// TestPrivateStorage rejects shared paths without changing directory permissions.
func TestPrivateStorage(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "stats.sqlite")
	if err := privateStorage(path); err == nil {
		t.Fatal("accepted shared directory")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created database before validation")
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0755 {
		t.Fatal("changed shared directory")
	}
	s := testStore(t)
	_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "permissions"})
	c.End(nil)
	for _, path := range []string{s.Path, s.Path + "-wal", s.Path + "-shm"} {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("permissions %s %v", path, err)
		}
	}
	link := filepath.Join(filepath.Dir(s.Path), "link.sqlite")
	if err := os.Symlink(s.Path, link); err != nil {
		t.Fatal(err)
	}
	if err := privateStorage(link); err == nil {
		t.Fatal("accepted symlink")
	}
}

// TestExactSQLPercentiles verifies nearest-rank values without Go duration samples.
func TestExactSQLPercentiles(t *testing.T) {
	s := testStore(t)
	db, err := s.writer()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UnixMicro()
	_, err = db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
 INSERT INTO events(id,parent,level,tool,provider,repository,at,ended,duration,outcome)
 SELECT CAST(i AS TEXT),'','request',CASE WHEN i<=5000 THEN 'a' ELSE 'b' END,'p','app',?,?,i,'ok' FROM n`, at, at+1)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out := snapshot(t, s, Query{Repository: "app"})
	t.Logf("10,000 events exact query: %s", time.Since(start))
	r := total(t, out, "request")
	if r.Calls != 10000 || r.P95US == nil || *r.P95US != 9500 || *r.MeanUS != 5000 || *r.MaxUS != 10000 {
		t.Fatal(r)
	}
	for _, r := range out.Rows {
		expected := int64(4750)
		if r.Name == "b" {
			expected = 9750
		}
		if r.P95US == nil || *r.P95US != expected {
			t.Fatal(r)
		}
	}
	filtered := snapshot(t, s, Query{Tool: "b", Repository: "app"})
	if r := total(t, filtered, "request"); r.Calls != 5000 || *r.P95US != 9750 {
		t.Fatal(r)
	}
}

// TestSQLiteResultCodeRetries covers base and extended busy codes from the driver.
func TestSQLiteResultCodeRetries(t *testing.T) {
	s := testStore(t)
	db, err := s.writer()
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+s.Path+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(`INSERT INTO meta VALUES('busy_test',1)`)
	if err == nil || !transientRead(fmt.Errorf("wrapped: %w", err)) {
		t.Fatalf("busy not classified: %v", err)
	}
	_ = tx.Rollback()
	snapshotTx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = snapshotTx.QueryRow(`SELECT count(*) FROM meta`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta VALUES('snapshot_test',1)`); err != nil {
		t.Fatal(err)
	}
	_, err = snapshotTx.Exec(`INSERT INTO meta VALUES('snapshot_write',1)`)
	if err == nil || !transientRead(err) {
		t.Fatalf("extended busy not classified: %v", err)
	}
	_ = snapshotTx.Rollback()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cursor, err := conn.QueryContext(context.Background(), `SELECT * FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	if !cursor.Next() {
		t.Fatal("missing lock fixture")
	}
	_, err = conn.ExecContext(context.Background(), `DROP TABLE meta`)
	if err == nil || !transientRead(err) {
		t.Fatalf("locked not classified: %v", err)
	}
	_ = cursor.Close()
	if transientRead(context.Canceled) || transientRead(fmt.Errorf("corrupt database")) {
		t.Fatal("non-transient error retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Read(ctx, Query{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
