package toolstats

import (
	"strings"
	"testing"
)

// TestDiagnosticRedactionAtStorageAndRead protects both newly recorded and historical secrets.
func TestDiagnosticRedactionAtStorageAndRead(t *testing.T) {
	s := testStore(t)
	_, c := s.Begin(t.Context(), Event{Level: "request", Tool: "synthetic"})
	c.Finish("fail", "synthetic", `{"token":"SYNTHETIC_NEW"}`)
	db, err := s.writer()
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = db.QueryRow(`SELECT reason FROM events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SYNTHETIC") {
		t.Fatal("new secret persisted")
	}
	if _, err = db.Exec(`UPDATE events SET reason=?`, `{"token":"SYNTHETIC_OLD"}`); err != nil {
		t.Fatal(err)
	}
	out := snapshot(t, s, Query{})
	if len(out.Errors) != 1 || strings.Contains(out.Errors[0].Reason, "SYNTHETIC") {
		t.Fatalf("old secret displayed: %+v", out.Errors)
	}
}
