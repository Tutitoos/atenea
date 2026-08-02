package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/orchestrator"
)

// measured builds a core over the fixture catalog with two real directories
// to search and a measurement base in a throwaway file, and returns the core
// with the path to that base.
//
// The repositories are real on purpose. A commission against paths that do not
// exist would still be measured -- a failed attempt is a measurement -- but it
// would prove the plumbing rather than the thing: that work actually done gets
// written down.
func measured(t *testing.T, extra string) (*core.Core, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "base.duckdb")
	body := catalog + "\n[metrics]\npath = \"" + base + "\"\n" + extra
	body = strings.Replace(body, `path = "/srv/api"`, `path = "`+corpus(t, "api.go")+`"`, 1)
	body = strings.Replace(body, `path = "/srv/scripts"`, `path = "`+corpus(t, "run.sh")+`"`, 1)
	return build(t, body), base
}

// corpus is a directory with one file in it that has something to find.
func corpus(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	body := "package main\n\n// TODO: this is the line the search is meant to find.\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	return dir
}

func readBase(t *testing.T, path string) []metrics.Row {
	t.Helper()
	store, err := metrics.Open(path, metrics.Options{})
	if err != nil {
		t.Fatalf("reopen the base: %v", err)
	}
	defer func() { _ = store.Close() }()
	rows, err := store.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	return rows
}

func commission(t *testing.T, atenea *core.Core) *orchestrator.Result {
	t.Helper()
	result, err := atenea.Do(context.Background(), orchestrator.Task{Text: "TODO"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	return result
}

// The brick, end to end: hand the core a commission, stop it, and find the
// work it did written down. Everything else in this file is a detail of this.
func TestATaskLeavesMeasurementsOnDisk(t *testing.T) {
	atenea, base := measured(t, "")
	result := commission(t, atenea)

	if _, err := os.Stat(base); err != nil {
		t.Fatalf("no database was created at %s: %v", base, err)
	}
	rows := readBase(t, base)
	if len(rows) == 0 {
		t.Fatal("a commission ran and the base is empty")
	}

	attempts := int64(0)
	for _, row := range rows {
		attempts += row.Attempts
		if row.Capability == "" || row.Implementation == "" || row.Repository == "" {
			t.Errorf("row is missing its key: %+v", row)
		}
		if row.Mean <= 0 {
			t.Errorf("%s on %s reports no time at all", row.Implementation, row.Repository)
		}
	}
	ran := 0
	for _, step := range result.Steps {
		if step.Decision.Chosen.ID != "" {
			ran++
		}
	}
	if int(attempts) != ran {
		t.Errorf("%d attempts on disk for %d dispatched steps", attempts, ran)
	}
}

// Nothing waits for the beat to reach disk: the shutdown path settles the
// batch itself. A CLI command lives for a second and would otherwise never
// tick, so this is the only reason a one-shot Atenea measures anything.
func TestTheBatchIsSettledOnTheWayDown(t *testing.T) {
	atenea, base := measured(t, "flush = \"1h\"\ncompact = \"24h\"\n")
	commission(t, atenea)
	if rows := readBase(t, base); len(rows) == 0 {
		t.Fatal("with an hour-long flush nothing reached disk, so the shutdown did not settle")
	}
}

// The base is read back per capability, implementation and repository, which
// is the shape the selector will consume. One blob for everything would be no
// use to a funnel that ranks a provider on a repository.
func TestTheBaseIsKeyedByCapabilityAndImplementation(t *testing.T) {
	atenea, base := measured(t, "")
	commission(t, atenea)

	rows := readBase(t, base)
	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Capability + "/" + row.Implementation + "/" + row.Repository
		if seen[key] {
			t.Errorf("%s appears twice; the summary is not grouping", key)
		}
		seen[key] = true
		if row.Capability != "code.search" {
			t.Errorf("capability = %q, want code.search", row.Capability)
		}
	}
	// The fixture puts two repositories in scope and the funnel picks a
	// different provider for each, so the base has to be able to tell them
	// apart. One row for both would mean the key is too coarse to rank on.
	if len(rows) < 2 {
		t.Fatalf("%d row(s) for two repositories with different winners: %+v", len(rows), rows)
	}
}

// The version of the tool that produced a number is filed with it, so that an
// upgrade starts a fresh baseline instead of averaging into the old one. The
// stand-in is Atenea itself, so the version it reports is the running binary's.
func TestTheBaseRecordsWhoProducedTheNumbers(t *testing.T) {
	atenea, base := measured(t, "")
	commission(t, atenea)

	for _, row := range readBase(t, base) {
		if row.ToolVersion == "" {
			t.Errorf("%s on %s was filed with no version at all",
				row.Implementation, row.Repository)
		}
	}
}

// Measuring switched off is a real choice: the core still works and no file is
// created. A store opened for a user who said no would be a lie on disk.
func TestWithMeasuringOffNothingIsWritten(t *testing.T) {
	atenea, base := measured(t, "enabled = false\n")
	commission(t, atenea)
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("a database exists at %s despite enabled = false (%v)", base, err)
	}
}
