package metrics

import (
	"context"
	"fmt"
	"strings"
)

// Filter narrows what a clear removes. Every field left empty matches
// everything, so the zero value is "all of it" -- which is the honest default
// for a verb whose whole job is emptying something, as long as the caller has
// to say so out loud.
type Filter struct {
	Capability     string
	Implementation string
	Repository     string
}

// Empty reports whether this filter narrows anything at all.
func (f Filter) Empty() bool {
	return f.Capability == "" && f.Implementation == "" && f.Repository == ""
}

// String describes the filter the way an operator would say it.
func (f Filter) String() string {
	var parts []string
	for _, p := range []struct{ name, value string }{
		{"capability", f.Capability},
		{"implementation", f.Implementation},
		{"repository", f.Repository},
	} {
		if p.value != "" {
			parts = append(parts, p.name+" "+p.value)
		}
	}
	if len(parts) == 0 {
		return "everything"
	}
	return strings.Join(parts, ", ")
}

// where builds the shared predicate and its arguments. Both tables carry the
// same three columns, so one predicate serves them both.
func (f Filter) where() (string, []any) {
	var clauses []string
	var args []any
	for _, p := range []struct{ column, value string }{
		{"capability", f.Capability},
		{"implementation", f.Implementation},
		{"repository", f.Repository},
	} {
		if p.value != "" {
			clauses = append(clauses, p.column+" = ?")
			args = append(args, p.value)
		}
	}
	if len(clauses) == 0 {
		return "TRUE", nil
	}
	return strings.Join(clauses, " AND "), args
}

// Cleared is what a clear removed, so the caller can say it rather than guess.
type Cleared struct {
	Attempts int64
	Rollups  int64
}

// Total is every row that went.
func (c Cleared) Total() int64 { return c.Attempts + c.Rollups }

// Clear removes measurements from the base.
//
// It exists because a poisoned baseline used to have exactly one cure:
// deleting the database file, which threw away every honest number alongside
// the bad ones. A provider that spent an afternoon misconfigured leaves a
// record that is true and useless -- true because those calls really did fail,
// useless because the machine it describes no longer exists.
//
// Both tables go together. Clearing the attempts and leaving the rollups would
// empty the recent half and let the folded half keep answering, which is worse
// than not clearing at all: the numbers would come back an hour later with no
// explanation.
//
// The buffer is dropped rather than flushed. Rows still in memory belong to
// the same history the caller just asked to be rid of, and writing them on the
// way out would put back a slice of exactly what was cleared.
func (s *Store) Clear(ctx context.Context, filter Filter) (Cleared, error) {
	s.mu.Lock()
	buffered := len(s.buf)
	s.buf = s.buf[:0]
	s.mu.Unlock()

	db, err := s.connect(ctx)
	if err != nil {
		return Cleared{}, err
	}
	defer func() { _ = db.Close() }()

	predicate, args := filter.where()
	var out Cleared
	for _, t := range []struct {
		table string
		into  *int64
	}{
		{"measurement", &out.Attempts},
		{"rollup", &out.Rollups},
	} {
		res, err := db.ExecContext(ctx, "DELETE FROM "+t.table+" WHERE "+predicate, args...)
		if err != nil {
			return out, fmt.Errorf("metrics: clear %s: %w", t.table, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			*t.into = n
		}
	}
	// Buffered rows never reached the disk, so they are not in either count
	// above, but they were just as surely thrown away.
	out.Attempts += int64(buffered)
	return out, nil
}
