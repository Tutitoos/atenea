package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// measured seeds a base the metrics screen will read, and returns the settings
// file pointing at it. The rows are deliberately shaped like the ones a real
// machine produces: names longer than any column a person would guess at, and
// one implementation measured at two versions of its tool -- which is what a
// failure that never reached the far side looks like beside the calls that
// did, because an attempt with no version is still an attempt.
func measured(t *testing.T, rows ...metrics.Measurement) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "base.duckdb")
	path := filepath.Join(dir, "atenea.toml")
	body := settings + "\n[metrics]\npath = \"" + base + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := metrics.Open(base, metrics.Options{})
	if err != nil {
		t.Fatalf("open the base: %v", err)
	}
	for _, m := range rows {
		store.Record(m)
	}
	if err := store.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// The long names are the real ones: symbol.implementations answered by
// codebase-memory.overview is not a pairing this catalog ships, but both
// strings are shipped lengths, and the screen has to hold the longest of each
// column rather than a width somebody guessed once.
func longRows() []metrics.Measurement {
	at := time.Now()
	return []metrics.Measurement{{
		At: at, RunID: "r1", StepID: "s1",
		Capability: "symbol.implementations", Implementation: "codebase-memory.overview",
		Provider: "codebase-memory", Repository: "current",
		ToolVersion: "codebase-memory-mcp 0.9.0",
		Spent:       contract.Sample{Duration: 40 * time.Millisecond}, OK: true,
	}, {
		At: at, RunID: "r2", StepID: "s1",
		Capability: "symbol.implementations", Implementation: "codebase-memory.overview",
		Provider: "codebase-memory", Repository: "current",
		// No version: the call was refused before anything ran, so nobody
		// could ask the far side what it was.
		Spent: contract.Sample{Duration: 147 * time.Microsecond}, OK: false,
		Failure: "permission_denied", FailureKind: "permission_denied",
	}}
}

// A table that splits one implementation into two rows has to say what split
// them. The base keys on the tool version on purpose -- yesterday's numbers
// for yesterday's binary are history, not a baseline -- so the version is a
// column, not a footnote: without it the same capability, implementation and
// repository appear twice on one screen with no visible reason, and the honest
// reading of that is "this screen is broken".
func TestTheMetricsScreenNamesWhatSplitTheRows(t *testing.T) {
	out, err := cli(t, "--config", measured(t, longRows()...), "metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if !strings.Contains(out, "codebase-memory-mcp 0.9.0") {
		t.Errorf("the screen splits rows by tool version and never prints one:\n%s", out)
	}
	// The attempt that never reached a tool still has to be readable as a row
	// rather than as a blank the eye slides over.
	body := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var refused string
	for _, line := range body {
		if strings.Contains(line, "permission_denied") || strings.HasSuffix(line, "147µs") {
			refused = line
		}
	}
	if refused == "" {
		t.Fatalf("the refused attempt is not on the screen at all:\n%s", out)
	}
}

// Every column has to start where its header does. A fixed width guessed
// before the catalog existed silently turns into a shifted table the first
// time a real name outgrows it, and the reader who notices is the one holding
// a screen that no longer lines up.
func TestTheMetricsScreenLinesUp(t *testing.T) {
	out, err := cli(t, "--config", measured(t, longRows()...), "metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	lines := strings.Split(out, "\n")
	var header string
	var rows []string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "capability "):
			header = line
		case strings.Contains(line, "current"):
			rows = append(rows, line)
		}
	}
	if header == "" || len(rows) == 0 {
		t.Fatalf("no table on the screen:\n%s", out)
	}
	// Every boundary a name sits on, not one. Checking a single column only
	// proves the columns before it: a header narrower than its own data shifts
	// the columns after it and leaves the one under test exactly where it was
	// -- which is how a mutation dropping the header out of the width
	// calculation went unnoticed the first time this was written.
	//
	// The value is required to START at the header's offset rather than merely
	// appear somewhere on the line, because a version string contains spaces
	// and a "-" appears in two different columns: searching would find the
	// wrong one and call a shifted table aligned.
	at := func(row, column, value string) {
		off := strings.Index(header, column)
		if off < 0 {
			t.Fatalf("the header has no %s column:\n%s", column, header)
		}
		if off > len(row) || !strings.HasPrefix(row[off:], value) {
			t.Errorf("%s does not start under %q at %d:\n%s\n%s",
				value, column, off, header, row)
		}
	}
	for _, row := range rows {
		at(row, "capability", "symbol.implementations")
		at(row, "implementation", "codebase-memory.overview")
		at(row, "repository", "current")
		// The refused attempt has no version to print, and the dash is the
		// column doing its job rather than a blank to skip over.
		version := "-"
		if strings.Contains(row, "codebase-memory-mcp") {
			version = "codebase-memory-mcp 0.9.0"
		}
		at(row, "version", version)
	}
}
