package toolstats

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// testStore creates an isolated private database path and closes its writer after the test.
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "stats.sqlite"))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// snapshot reads activity and fails the test on storage errors.
func snapshot(t *testing.T, s *Store, q Query) Snapshot {
	t.Helper()
	out, err := s.Read(context.Background(), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// total retrieves independent request or attempt totals from a snapshot.
func total(t *testing.T, s Snapshot, level string) Row {
	t.Helper()
	for _, r := range s.Totals {
		if r.Level == level {
			return r
		}
	}
	t.Fatalf("missing %s total", level)
	return Row{}
}

// TestCallsAreLinkedAndFinalizedOnlyOnce verifies parent identifiers and idempotent completion.
func TestCallsAreLinkedAndFinalizedOnlyOnce(t *testing.T) {
	s := testStore(t)
	ctx, request := s.Begin(context.Background(), Event{Level: "request", Tool: "code.search", Provider: "atenea", Repository: "app"})
	_, a := s.Begin(ctx, Event{Level: "attempt", Tool: "first", Provider: "p", Repository: "app"})
	a.End(contract.Fail(contract.FailureUnavailable, "offline"))
	_, b := s.Begin(ctx, Event{Level: "attempt", Tool: "second", Provider: "p", Repository: "app"})
	b.End(nil)
	request.End(nil)
	request.End(nil)
	out := snapshot(t, s, Query{})
	if r := total(t, out, "request"); r.Calls != 1 || r.OK != 1 {
		t.Fatalf("request %+v", r)
	}
	if r := total(t, out, "attempt"); r.Calls != 2 || r.OK != 1 || r.Fail != 1 {
		t.Fatalf("attempt %+v", r)
	}
	var parents int
	if err := s.db.QueryRow(`SELECT count(*) FROM events WHERE parent=?`, request.Event.ID).Scan(&parents); err != nil || parents != 2 {
		t.Fatalf("parents=%d err=%v", parents, err)
	}
}

// TestOutcomesFiltersAndActive checks result categories, filters, and active-call accounting.
func TestOutcomesFiltersAndActive(t *testing.T) {
	s := testStore(t)
	errors := []error{nil, contract.Fail(contract.FailurePermissionDenied, "denied"), contract.Fail(contract.FailureTimeout, "timeout"), context.Canceled}
	for _, err := range errors {
		_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "tool", Provider: "p", Repository: "app"})
		c.End(err)
	}
	_, active := s.Begin(context.Background(), Event{Level: "request", Tool: "tool", Provider: "p", Repository: "app"})
	out := snapshot(t, s, Query{Repository: "app", Provider: "p", Tool: "ool"})
	r := total(t, out, "request")
	if r.Calls != 4 || r.OK != 1 || r.Refused != 1 || r.Fail != 1 || r.Cancel != 1 || r.Active != 1 || r.Samples != 3 || *r.OKPercent != 25 {
		t.Fatalf("%+v", r)
	}
	if len(out.Errors) != 2 {
		t.Fatalf("errors=%+v", out.Errors)
	}
	if total(t, snapshot(t, s, Query{Repository: "other"}), "request").Calls != 0 {
		t.Fatal("filter leaked")
	}
	active.End(nil)
}

// TestReadDoesNotCreateStateAndCorruptionIsAnError distinguishes empty storage from unreadable data.
func TestReadDoesNotCreateStateAndCorruptionIsAnError(t *testing.T) {
	s := testStore(t)
	snapshot(t, s, Query{})
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("read created database")
	}
	if err := os.WriteFile(s.Path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), Query{}, nil); err == nil {
		t.Fatal("corruption reported as zero")
	}
}

// TestCompactionRestartAndBoundaryCoverage checks repeated rollups and partial interval reporting.
func TestCompactionRestartAndBoundaryCoverage(t *testing.T) {
	s := testStore(t)
	at := time.Now().UTC().AddDate(0, 0, -12).Truncate(24 * time.Hour).Add(time.Hour)
	for i := 0; i < 3; i++ {
		_, c := s.Begin(context.Background(), Event{Level: "request", Tool: "old", Provider: "p", At: at})
		c.End(nil)
	}
	if err := s.compact(s.db, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.compact(s.db, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = New(s.Path)
	out := snapshot(t, s, Query{})
	r := total(t, out, "request")
	if r.Calls != 3 || r.P95US != nil || r.MeanUS == nil || r.Last == nil {
		t.Fatalf("rollup %+v", r)
	}
	partial := snapshot(t, s, Query{Since: at.Add(-time.Minute)})
	if !partial.Coverage.Partial || total(t, partial, "request").Calls != 0 {
		t.Fatalf("partial %+v", partial)
	}
}

// TestCatalogUnknownAndUnused verifies discovery state and zero-activity catalog entries.
func TestCatalogUnknownAndUnused(t *testing.T) {
	s := testStore(t)
	s.Remember("p", []string{"raw.p.a", "raw.p.b"})
	catalog := []Tool{{Level: "request", Name: "search", Provider: "atenea"}, {Level: "catalog", Name: "raw.p.*", Provider: "p", State: "catalog_unknown"}}
	out, err := s.Read(context.Background(), Query{}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 5 {
		t.Fatalf("rows %+v", out.Rows)
	}
	for _, c := range out.Catalog {
		if c.State == "catalog_unknown" {
			t.Fatal("discovered provider still unknown")
		}
	}
	used, err := s.Read(context.Background(), Query{Used: true}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(used.Rows) != 0 {
		t.Fatal("unused tools present")
	}
}

// TestPercentileAndControlCharacters checks duration means and sanitization of terminal controls.
func TestPercentileAndControlCharacters(t *testing.T) {
	r := Row{Calls: 4, OK: 4, Samples: 4, SumUS: 106}
	calculate(&r)
	if *r.MeanUS != 26 {
		t.Fatalf("%+v", r)
	}
	clean := Clean("hi\x1b[31m red\x1b[0m\x1b]0;bad\x07\nthere", 100)
	if strings.ContainsAny(clean, "\x1b\n\x07") || strings.Contains(clean, "bad") || !strings.Contains(clean, "there") {
		t.Fatalf("%q", clean)
	}
}

// TestConcurrentStoresAndReaders checks that parallel writers preserve counts without dropped events.
func TestConcurrentStoresAndReaders(t *testing.T) {
	s := testStore(t)
	other := New(s.Path)
	defer func() { _ = other.Close() }()
	_, initial := s.Begin(context.Background(), Event{Level: "request", Tool: "seed", Provider: "p"})
	initial.End(nil)
	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			writer := s
			if i%2 == 0 {
				writer = other
			}
			ctx, c := writer.Begin(context.Background(), Event{Level: "request", Tool: "parallel", Provider: "p"})
			_, a := writer.Begin(ctx, Event{Level: "attempt", Tool: "parallel", Provider: "p"})
			a.End(nil)
			c.End(nil)
			done <- true
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
		snapshot(t, s, Query{})
	}
	out := snapshot(t, s, Query{})
	if r := total(t, out, "request"); r.Calls != 21 || r.Active != 0 {
		t.Fatalf("request %+v", r)
	}
	if r := total(t, out, "attempt"); r.Calls != 20 {
		t.Fatalf("attempt %+v", r)
	}
	if out.Coverage.Dropped != 0 {
		t.Fatalf("lost %+v", out.Coverage)
	}
}
