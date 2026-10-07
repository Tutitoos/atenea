package toolstats

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContextReadIsPrivateBoundedAndSeparateFromAttempts(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	for i, origin := range []string{"normal", "synthetic", "unknown"} {
		ctx := WithMetadata(context.Background(), Metadata{Client: "codex", Profile: "shared", ClientVersion: "1.2.3", Origin: origin})
		ctx, request := s.Begin(ctx, Event{Level: "request", Tool: "tool", Provider: "atenea", At: now.Add(-time.Minute)})
		for j := 0; j <= i; j++ {
			_, attempt := s.Begin(ctx, Event{Level: "attempt", Tool: "provider.tool", Provider: "provider", At: now.Add(-time.Minute)})
			attempt.End(nil)
		}
		request.End(nil)
	}
	ctx := WithMetadata(context.Background(), Metadata{Client: "name@example.com", Profile: "/private/file", ClientVersion: "secret/path", Origin: "normal"})
	_, private := s.Begin(ctx, Event{Level: "request", Tool: "private", At: now.Add(-time.Minute)})
	private.End(nil)
	out, err := New(s.Path).Context(context.Background(), Query{Since: now.Add(-time.Hour), Until: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Requests != 4 || out.Attempts != 6 || out.MissingContext != 0 || len(out.Rows) != 4 {
		t.Fatalf("context counts: %+v", out)
	}
	seen := map[string]bool{}
	for _, row := range out.Rows {
		seen[row.Origin] = true
	}
	for _, origin := range []string{"normal", "synthetic", "unknown"} {
		if !seen[origin] {
			t.Fatalf("missing %s: %+v", origin, out.Rows)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"example.com", "/private", "secret/path", "provider.tool", "receipt", "request_id"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("private data %q in %s", leak, raw)
		}
	}
	if got := total(t, snapshot(t, s, Query{}), "request").Calls; got != 4 {
		t.Fatalf("context read counted itself: %d", got)
	}
}

func TestContextOverflowMissingMetadataAndWindow(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	clients := []string{"codex", "chatgpt", "claude", "omp", "opencode"}
	profiles := []string{"shared", "chatgpt", "codex", "claude", "omp", "opencode"}
	for i := 0; i < 30; i++ {
		ctx := WithMetadata(context.Background(), Metadata{Client: clients[i/6], Profile: profiles[i%6], ClientVersion: "1.2.3", Origin: "normal"})
		_, call := s.Begin(ctx, Event{Level: "request", Tool: "tool", At: now.Add(-time.Minute)})
		call.End(nil)
	}
	_, missing := s.Begin(context.Background(), Event{Level: "request", Tool: "tool", At: now.Add(-time.Minute)})
	missing.End(nil)
	// A malformed or missing metadata row is kept under unknown, not normal.
	if _, err := s.db.Exec(`DELETE FROM event_context WHERE event=?`, missing.Event.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.Context(context.Background(), Query{Since: now.Add(-time.Hour), Until: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) > contextMaxRows || out.Requests != 31 || out.MissingContext != 1 || !out.Partial || out.Overflow.Requests == 0 {
		t.Fatalf("bounds/coverage: %+v", out)
	}
	if _, err := s.Context(context.Background(), Query{Since: now.Add(-169 * time.Hour), Until: now}); err == nil {
		t.Fatal("accepted 169 hours")
	}
}

func TestContextMissingDatabaseIsReadOnly(t *testing.T) {
	s := testStore(t)
	out, err := s.Context(context.Background(), Query{})
	if err != nil || out.HistoryAvailable || out.Requests != 0 {
		t.Fatalf("missing database: %+v %v", out, err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("read created database: %v", err)
	}
}

func TestContextLegacyWithoutOwnerOrContextTables(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "old.sqlite")
	db, err := sql.Open("sqlite", dsn(path, "rwc"))
	if err != nil {
		t.Fatal(err)
	}
	// Pre-owner and pre-context schema: those tables never existed here.
	_, err = db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value INTEGER NOT NULL);
CREATE TABLE events(id TEXT PRIMARY KEY,parent TEXT NOT NULL,level TEXT NOT NULL,tool TEXT NOT NULL,provider TEXT NOT NULL,repository TEXT NOT NULL,at INTEGER NOT NULL,ended INTEGER,duration INTEGER NOT NULL DEFAULT 0,outcome TEXT NOT NULL DEFAULT '',code TEXT NOT NULL DEFAULT '',reason TEXT NOT NULL DEFAULT '');
CREATE TABLE rollups(bucket INTEGER NOT NULL,level TEXT NOT NULL,tool TEXT NOT NULL,provider TEXT NOT NULL,repository TEXT NOT NULL,calls INTEGER NOT NULL,ok INTEGER NOT NULL,refused INTEGER NOT NULL,fail INTEGER NOT NULL,cancel INTEGER NOT NULL,dsum INTEGER NOT NULL,samples INTEGER NOT NULL,dmax INTEGER NOT NULL,last INTEGER NOT NULL,PRIMARY KEY(bucket,level,tool,provider,repository));`)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UnixMicro()
	if _, err = db.Exec(`INSERT INTO meta VALUES('started',?)`, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO events(id,parent,level,tool,provider,repository,at,ended,outcome) VALUES('one','','request','old','','',?,?,'ok')`, at, at+1); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := New(path).Context(context.Background(), Query{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !out.HistoryAvailable || !out.Partial || out.Requests != 1 || out.MissingContext != 1 || out.UnknownOriginRequests != 1 {
		t.Fatalf("legacy context: %+v", out)
	}
}

func TestContextAdjacentWindowsDoNotDoubleCountBoundaryDetail(t *testing.T) {
	s := testStore(t)
	boundary := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	ctx := WithMetadata(context.Background(), Metadata{Client: "codex", Origin: "normal"})
	_, before := s.Begin(ctx, Event{Level: "request", Tool: "before", At: boundary.Add(-time.Microsecond)})
	before.End(nil)
	if _, err := s.db.Exec(`UPDATE events SET ended=? WHERE id=?`, boundary.Add(-time.Microsecond).UnixMicro(), before.Event.ID); err != nil {
		t.Fatal(err)
	}
	_, edge := s.Begin(ctx, Event{Level: "request", Tool: "edge", At: boundary})
	_, attempt := s.Begin(context.Background(), Event{Level: "attempt", Tool: "edge-attempt", At: boundary})
	attempt.End(nil)
	edge.End(nil)
	if _, err := s.db.Exec(`DELETE FROM event_context WHERE event=?`, attempt.Event.ID); err != nil {
		t.Fatal(err)
	}
	first, err := s.Context(context.Background(), Query{Since: boundary.Add(-time.Hour), Until: boundary})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Context(context.Background(), Query{Since: boundary, Until: boundary.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if first.Requests != 1 || first.Attempts != 0 || first.MissingAttemptContext != 0 || second.Requests != 1 || second.Attempts != 1 || second.MissingAttemptContext != 1 || !second.Partial {
		t.Fatalf("adjacent windows: first=%+v second=%+v", first, second)
	}
}

func TestContextCompletionAtUntilIsExcludedAcrossCounters(t *testing.T) {
	s := testStore(t)
	boundary := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	ctx := WithMetadata(context.Background(), Metadata{Client: "codex", Origin: "normal"})
	_, before := s.Begin(ctx, Event{Level: "request", Tool: "before", At: boundary.Add(-2 * time.Microsecond)})
	before.End(nil)
	_, edge := s.Begin(ctx, Event{Level: "request", Tool: "edge", At: boundary.Add(-time.Microsecond)})
	_, attempt := s.Begin(ctx, Event{Level: "attempt", Tool: "edge-attempt", At: boundary.Add(-time.Microsecond)})
	attempt.End(nil)
	edge.End(nil)
	for _, id := range []string{edge.Event.ID, attempt.Event.ID} {
		if _, err := s.db.Exec(`UPDATE events SET ended=?,duration=-1,code='recording_interrupted',outcome='fail' WHERE id=?`, boundary.UnixMicro(), id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`DELETE FROM event_context WHERE event=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE events SET ended=? WHERE id=?`, boundary.Add(-time.Microsecond).UnixMicro(), before.Event.ID); err != nil {
		t.Fatal(err)
	}
	since := boundary.Add(-time.Hour)
	left, err := s.Context(context.Background(), Query{Since: since, Until: boundary})
	if err != nil {
		t.Fatal(err)
	}
	right, err := s.Context(context.Background(), Query{Since: since, Until: boundary.Add(time.Microsecond)})
	if err != nil {
		t.Fatal(err)
	}
	if left.Requests != 1 || left.Attempts != 0 || left.RecoveredRequests != 0 || left.RecoveredAttempts != 0 || left.MissingContext != 0 || left.MissingAttemptContext != 0 {
		t.Fatalf("exclusive completion: %+v", left)
	}
	if right.Requests != 2 || right.Attempts != 1 || right.RecoveredRequests != 1 || right.RecoveredAttempts != 1 || right.MissingContext != 1 || right.MissingAttemptContext != 1 || !right.Partial {
		t.Fatalf("later completion: %+v", right)
	}
}

func TestContextAttemptUncertaintyAndRecovery(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	ctx := WithMetadata(context.Background(), Metadata{Client: "codex", Profile: "shared", ClientVersion: "2.1", Origin: "normal"})
	_, request := s.Begin(ctx, Event{Level: "request", Tool: "tool", At: now.Add(-time.Minute)})
	_, attempt := s.Begin(context.Background(), Event{Level: "attempt", Tool: "impl", At: now.Add(-time.Minute)})
	attempt.End(nil)
	request.End(nil)
	if _, err := s.db.Exec(`DELETE FROM event_context WHERE event=?`, attempt.Event.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET code='recording_interrupted',duration=-1,outcome='fail' WHERE id=?`, attempt.Event.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.Context(context.Background(), Query{Since: now.Add(-time.Hour), Until: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Partial || out.Requests != 1 || out.Attempts != 1 || out.RecoveredRequests != 0 || out.RecoveredAttempts != 1 || out.MissingContext != 0 || out.MissingAttemptContext != 1 || out.UnknownOriginRequests != 0 || out.UnknownOriginAttempts != 1 {
		t.Fatalf("attempt uncertainty: %+v", out)
	}
}

func TestContextRollupBoundaryAndLegacyCoverage(t *testing.T) {
	s := testStore(t)
	_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "seed"})
	c.End(nil)
	bucket := time.Now().UTC().Add(-48 * time.Hour).Truncate(24 * time.Hour).UnixMicro()
	_, err := s.db.Exec(`INSERT INTO rollups(bucket,level,tool,provider,repository,calls,ok,refused,fail,cancel,dsum,samples,dmax,last) VALUES(?, 'request','old','p','',4,4,0,0,0,0,0,0,?)`, bucket, bucket+1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO context_rollups(bucket,level,tool,provider,repository,client,client_version,profile,provider_version,schema_hash,origin,code,calls,ok,refused,fail,cancel,dsum,samples,dmax,last,max_ended) VALUES(?,'request','old','p','','codex','2.1','shared','','','normal','',3,3,0,0,0,0,0,0,?,?)`, bucket, bucket+1, bucket+2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO rollups(bucket,level,tool,provider,repository,calls,ok,refused,fail,cancel,dsum,samples,dmax,last) VALUES(?,'attempt','old','p','',2,0,0,2,0,0,0,0,?)`, bucket, bucket+1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO context_rollups(bucket,level,tool,provider,repository,client,client_version,profile,provider_version,schema_hash,origin,code,calls,ok,refused,fail,cancel,dsum,samples,dmax,last,max_ended) VALUES(?,'attempt','old','p','','codex','2.1','shared','','','normal','recording_interrupted',2,0,0,2,0,0,0,0,?,?)`, bucket, bucket+1, bucket+2)
	if err != nil {
		t.Fatal(err)
	}
	// A bucket starting exactly at the exclusive window end must not inflate
	// omitted coverage for the preceding window.
	_, err = s.db.Exec(`INSERT INTO rollups(bucket,level,tool,provider,repository,calls,ok,refused,fail,cancel,dsum,samples,dmax,last) VALUES(?,'request','next','p','',5,5,0,0,0,0,0,0,?)`, bucket+86400000000, bucket+86400000001)
	if err != nil {
		t.Fatal(err)
	}
	end := bucket + 86400000000
	for _, entry := range []struct {
		level, tool string
		calls       int64
	}{{"request", "end-request", 5}, {"attempt", "end-attempt", 7}} {
		_, err = s.db.Exec(`INSERT INTO rollups(bucket,level,tool,provider,repository,calls,ok,refused,fail,cancel,dsum,samples,dmax,last) VALUES(?,?,?,?,?, ?,0,0,?,0,0,0,0,?)`, bucket, entry.level, entry.tool, "p", "", entry.calls, entry.calls, bucket+1)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(`INSERT INTO context_rollups(bucket,level,tool,provider,repository,client,client_version,profile,provider_version,schema_hash,origin,code,calls,ok,refused,fail,cancel,dsum,samples,dmax,last,max_ended) VALUES(?,?,?,?,?,'codex','2.1','shared','','','normal','recording_interrupted',?,0,0,?,0,0,0,0,?,?)`, bucket, entry.level, entry.tool, "p", "", entry.calls, entry.calls, bucket+1, end)
		if err != nil {
			t.Fatal(err)
		}
	}
	full, err := s.Context(context.Background(), Query{Since: time.UnixMicro(bucket), Until: time.UnixMicro(bucket + 86400000000)})
	if err != nil {
		t.Fatal(err)
	}
	if full.Requests != 3 || full.Attempts != 2 || full.RecoveredRequests != 0 || full.RecoveredAttempts != 2 || full.OmittedRollupRequests != 6 || full.OmittedRollupAttempts != 7 || !full.Partial {
		t.Fatalf("full rollup: %+v", full)
	}
	justAfter, err := s.Context(context.Background(), Query{Since: time.UnixMicro(bucket), Until: time.UnixMicro(end + 1)})
	if err != nil {
		t.Fatal(err)
	}
	if justAfter.Requests != 8 || justAfter.Attempts != 9 || justAfter.RecoveredRequests != 5 || justAfter.RecoveredAttempts != 9 || justAfter.OmittedRollupRequests != 6 || justAfter.OmittedRollupAttempts != 0 {
		t.Fatalf("after completion boundary: %+v", justAfter)
	}
	partial, err := s.Context(context.Background(), Query{Since: time.UnixMicro(bucket + 43200000000), Until: time.UnixMicro(bucket + 86400000000)})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Requests != 0 || partial.Attempts != 0 || partial.OmittedRollupRequests != 9 || partial.OmittedRollupAttempts != 9 || !partial.Partial {
		t.Fatalf("boundary rollup: %+v", partial)
	}
}
