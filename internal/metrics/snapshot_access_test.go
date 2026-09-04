package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/dbaccess"
)

// TestSnapshotExclusionPreservesBufferedMeasurements prevents disk writes without dropping queued data.
func TestSnapshotExclusionPreservesBufferedMeasurements(t *testing.T) {
	s := store(t, Options{})
	release, err := dbaccess.Acquire(t.Context(), s.Path(), true)
	if err != nil {
		t.Fatal(err)
	}
	s.Record(attempt(time.Now(), "search", "provider"))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err = s.Flush(ctx); err == nil {
		t.Fatal("writer bypassed snapshot lease")
	}
	_ = release()
	if err = s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := s.connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM measurement`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("buffer lost or duplicated: %d %v", count, err)
	}
}
