package toolstats

import (
	"context"
	"testing"
	"time"
)

func TestAuditCompactionChangesHistoricalCounts(t *testing.T) {
	s := testStore(t)
	at := time.Now().UTC().AddDate(0, 0, -12).Truncate(24 * time.Hour).Add(23*time.Hour + 59*time.Minute)
	_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "slow", At: at})
	c.End(nil)
	if _, err := s.db.Exec(`UPDATE events SET ended=?,duration=? WHERE id=?`, at.Add(2*time.Minute).UnixMicro(), (2 * time.Minute).Microseconds(), c.Event.ID); err != nil {
		t.Fatal(err)
	}
	q := Query{Since: at.Truncate(24 * time.Hour), Until: at.Add(time.Minute)}
	before := total(t, snapshot(t, s, q), "request")
	if err := s.compact(s.db, time.Now()); err != nil {
		t.Fatal(err)
	}
	after := total(t, snapshot(t, s, q), "request")
	if before.Calls != after.Calls {
		t.Fatal("compaction invented completed calls")
	}
	if !snapshot(t, s, q).Coverage.Partial {
		t.Fatal("missing partial coverage")
	}
	if err := s.compact(s.db, time.Now()); err != nil {
		t.Fatal(err)
	}
	if total(t, snapshot(t, s, q), "request").Calls != after.Calls {
		t.Fatal("compaction not idempotent")
	}
}
