package toolstats

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestErrorPagesKeepCorrelationAndFreezeWindow(t *testing.T) {
	s := testStore(t)
	ctx := WithMetadata(context.Background(), Metadata{Client: "codex", ClientVersion: "1", Profile: "chatgpt", Origin: "synthetic"})
	since := time.Now().Add(-time.Second)
	for i := 0; i < 3; i++ {
		_, call := s.Begin(ctx, Event{Level: "request", Tool: "raw.device.wait", Provider: "device"})
		call.Event.Metadata.ReceiptID = "receipt"
		call.Finish("fail", "INVALID_ARGS", "missing condition")
	}
	q := ErrorQuery{Query: Query{Since: since}, Limit: 2, Client: "codex", Origin: "synthetic"}
	page, err := s.Errors(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || page.NextCursor == "" || len(page.Groups) != 1 || page.Groups[0].Calls != 3 {
		t.Fatalf("%+v", page)
	}
	if page.Rows[0].ReceiptID != "receipt" || page.Rows[0].ID == "" || page.Rows[0].ClientVersion != "1" {
		t.Fatal(page.Rows[0])
	}
	_, call := s.Begin(ctx, Event{Level: "request", Tool: "raw.device.wait", Provider: "device"})
	call.Finish("fail", "INVALID_ARGS", "later")
	q.Cursor = page.NextCursor
	next, err := s.Errors(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Rows) != 1 || next.NextCursor != "" || next.Rows[0].ID == page.Rows[1].ID || next.Groups[0].Calls != 3 {
		t.Fatalf("%+v", next)
	}
	q.Client = "different"
	if _, err = s.Errors(ctx, q); err == nil {
		t.Fatal("cursor accepted different filters")
	}
}

func TestContextCompactionRetainsCauseOnce(t *testing.T) {
	s := testStore(t)
	old := time.Now().UTC().AddDate(0, 0, -10)
	_, call := s.Begin(WithMetadata(t.Context(), Metadata{Client: "test", Origin: "synthetic"}), Event{Level: "request", Tool: "wait", At: old})
	call.Finish("fail", "INVALID_ARGS", "diagnosis")
	s.mu.Lock()
	err := s.compact(s.db, time.Now())
	if err == nil {
		err = s.compact(s.db, time.Now())
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Errors(t.Context(), ErrorQuery{Query: Query{Since: old.Add(-time.Hour)}, Client: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 0 || len(page.Groups) != 1 || page.Groups[0].Calls != 1 || page.Groups[0].Origin != "synthetic" {
		t.Fatalf("%+v", page)
	}
	var contexts int
	if err = s.db.QueryRow(`SELECT count(*) FROM event_context`).Scan(&contexts); err != nil || contexts != 0 {
		t.Fatalf("contexts=%d %v", contexts, err)
	}
}

func TestErrorQueryRejectsInvalidLimitsAndCursorWithoutCreatingStorage(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "absent.sqlite"))
	for _, q := range []ErrorQuery{{Limit: -1}, {Limit: 501}, {Cursor: "bad"}, {Origin: "guessed"}} {
		if _, err := s.Errors(t.Context(), q); err == nil {
			t.Fatal(q)
		}
	}
}

func TestLegacyDatabaseReadIsUnchangedAndUpgradePreservesUnknownHistory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "legacy.sqlite")
	db, err := sql.Open("sqlite", dsn(filename, "rwc"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UnixMicro()
	if _, err = db.Exec(schema, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO events(id,parent,level,tool,provider,repository,at,ended,outcome,code) VALUES('old','','request','old.tool','','',?,?,'fail','OLD')`, at, at+1); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.Chmod(filename, 0600)
	before, _ := os.ReadFile(filename)
	s := New(filename)
	page, err := s.Errors(t.Context(), ErrorQuery{})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filename)
	if string(before) != string(after) {
		t.Fatal("read mutated legacy DB")
	}
	if len(page.Rows) != 1 || page.Rows[0].Origin != "unknown" || page.Rows[0].Client != "" || page.Rows[0].ReceiptID != "" {
		t.Fatalf("legacy=%+v", page.Rows)
	}
	_, call := s.Begin(WithMetadata(t.Context(), Metadata{Client: "fixture", Origin: "synthetic"}), Event{Level: "request", Tool: "new.tool"})
	call.Finish("fail", "NEW", "")
	defer s.Close()
	page, err = s.Errors(t.Context(), ErrorQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("upgrade lost or duplicated history: %+v", page)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM event_context WHERE event='old'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("guessed historical metadata", n, err)
	}
}
