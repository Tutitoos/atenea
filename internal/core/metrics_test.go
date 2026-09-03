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
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
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

// poison writes a run of identical failures straight into the base, which is
// what a provider that is simply down leaves behind. A second store on the
// same file is not a trick: the core holds the file only for the length of a
// flush, and two Ateneas taking turns is the ordinary case.
func poison(t *testing.T, path, implementation, provider string, times int) {
	t.Helper()
	store, err := metrics.Open(path, metrics.Options{})
	if err != nil {
		t.Fatalf("open the base: %v", err)
	}
	now := time.Now().UTC()
	for i := range times {
		store.Record(metrics.Measurement{
			At:             now.Add(time.Duration(i) * time.Second),
			RunID:          "audit",
			StepID:         "ask",
			Capability:     "code.search",
			Implementation: implementation,
			Provider:       provider,
			Repository:     "api",
			ToolVersion:    "1.0.0",
			Spent:          contract.Sample{Duration: 20 * time.Millisecond},
			FailureKind:    string(contract.FailureUnavailable),
			Failure:        "claude code is not logged in on this machine",
		})
	}
	if err := store.Close(); err != nil {
		t.Fatalf("flush the base: %v", err)
	}
}

// The defect this brick was written for, end to end.
//
// A provider that refuses every call used to stay in the funnel forever: the
// only thing that could mark it down was a probe, nothing probes, and the
// refusals themselves were being read as a very fast and very cheap call. The
// record on disk is the witness that a fresh CLI process still has.
func TestAProviderThatKeepsFailingLeavesTheFunnel(t *testing.T) {
	atenea, base := measured(t, "")
	before, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if before.Chosen.ID != "fixture.search" {
		t.Fatalf("chosen = %s before any failure, want fixture.search", before.Chosen.ID)
	}

	poison(t, base, "fixture.search", "fixture", metrics.FaultStreak)

	after, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select after the failures: %v", err)
	}
	if after.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s, want the funnel to move on to ripgrep", after.Chosen.ID)
	}

	// Moving on is half of it. The trace has to name who left and why, or the
	// operator is looking at a silent fallback again.
	var reason string
	for _, stage := range after.Stages {
		if stage.Name != selector.StageHealth {
			continue
		}
		for _, dropped := range stage.Dropped {
			if dropped.Implementation == "fixture.search" {
				reason = dropped.Reason
			}
		}
	}
	if reason == "" {
		t.Fatalf("the health stage never mentions fixture.search: %+v", after.Stages)
	}
	if !strings.Contains(reason, "in a row") || !strings.Contains(reason, "not logged in") {
		t.Errorf("reason %q does not say how many failed nor what the provider said", reason)
	}
}

// The way back, on the real funnel. A verdict with no expiry would be a
// provider nobody ever calls again, because health drops it before anything
// can prove it recovered.
func TestAStaleOutageStopsCountingSoTheProviderIsTriedAgain(t *testing.T) {
	atenea, base := measured(t, "")
	store, err := metrics.Open(base, metrics.Options{})
	if err != nil {
		t.Fatalf("open the base: %v", err)
	}
	old := time.Now().UTC().Add(-metrics.FaultWindow - time.Hour)
	for i := range metrics.FaultStreak {
		store.Record(metrics.Measurement{
			At:             old.Add(time.Duration(i) * time.Second),
			RunID:          "audit",
			StepID:         "ask",
			Capability:     "code.search",
			Implementation: "fixture.search",
			Provider:       "fixture",
			Repository:     "api",
			ToolVersion:    "1.0.0",
			FailureKind:    string(contract.FailureUnavailable),
			Failure:        "was down an hour ago",
		})
	}
	if err := store.Close(); err != nil {
		t.Fatalf("flush the base: %v", err)
	}

	decision, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "fixture.search" {
		t.Errorf("chosen = %s, want fixture.search: an hour-old outage still had it banned",
			decision.Chosen.ID)
	}
}

// The last flush gets more than one chance, and its failure gets written down.
//
// Store.Close is a single Flush, and a Flush that fails returns its rows to the
// in-memory buffer -- correct for a running service, whose next beat carries
// them, and useless at a stop, where there is no next beat and the buffer dies
// with the process. One transient failure -- another process holding the file,
// a filesystem not back yet -- was silently the end of that batch, with nothing
// in the notebook to say a baseline had gone short. The measurements really are
// lost when the retries run out; what must not be lost as well is the fact.
func TestTheLastFlushIsRetriedAndItsLossIsWrittenDown(t *testing.T) {
	atenea, base := measured(t, "")
	// A file that DuckDB cannot open is what a broken store looks like from
	// here: the driver refuses it, every flush fails, and the rows go back
	// into a buffer that is about to be freed. Broken before the work rather
	// than after it, so the measurements the commission produces are still in
	// that buffer when the stop begins.
	if err := os.WriteFile(base, []byte("this is not a database"), 0o600); err != nil {
		t.Fatalf("breaking the base: %v", err)
	}
	if _, err := atenea.Do(context.Background(), orchestrator.Task{Text: "TODO"}); err != nil {
		t.Fatalf("Do: %v", err)
	}

	start := time.Now()
	err := atenea.Shutdown()
	if err == nil {
		t.Fatal("a stop over a base that cannot be written reported success")
	}
	// Three attempts spaced by the backoff, so the elapsed time is the proof
	// that more than one was made: a single Flush returns in microseconds.
	if took := time.Since(start); took < 2*150*time.Millisecond {
		t.Errorf("the stop took %v: that is one attempt, not a retry inside a budget", took)
	}

	entries, readErr := os.ReadFile(notebook.DefaultPath())
	if readErr != nil {
		t.Fatalf("the notebook was never written: %v", readErr)
	}
	body := string(entries)
	if !strings.Contains(body, "metrics.settle") {
		t.Errorf("notebook = %s, want an incident for the batch that died with the process", body)
	}
	if !strings.Contains(body, "measurements are gone") {
		t.Errorf("notebook = %s, want the incident to count what was lost", body)
	}
}
