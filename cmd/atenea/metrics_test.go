package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// based writes a settings file whose measurement base is a throwaway file, and
// fills it with a record shaped like the one that started this: a provider
// doing real work, and a provider that only ever refuses.
func based(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "base.duckdb")
	path := filepath.Join(dir, "atenea.toml")
	if err := os.WriteFile(path, []byte(settings+"\n[metrics]\npath = \""+base+"\"\n"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	store, err := metrics.Open(base, metrics.Options{})
	if err != nil {
		t.Fatalf("open the base: %v", err)
	}
	now := time.Now().UTC()
	for i := range 3 {
		store.Record(metrics.Measurement{
			At: now.Add(time.Duration(i) * time.Second), RunID: "r", StepID: "s",
			Capability: "code.search", Implementation: "ripgrep", Provider: "ripgrep",
			Repository: "api", ToolVersion: "14.1.0",
			Spent: contract.Sample{Duration: 90 * time.Millisecond}, OK: true,
		})
		store.Record(metrics.Measurement{
			At: now.Add(time.Duration(i) * time.Second), RunID: "r", StepID: "s",
			Capability: "code.search", Implementation: "fixture.search", Provider: "fixture",
			Repository: "api", ToolVersion: "1.28.1",
			Spent:       contract.Sample{Duration: 20 * time.Millisecond},
			FailureKind: string(contract.FailureUnavailable),
			Failure:     "unavailable: no language server for go",
		})
	}
	if err := store.Close(); err != nil {
		t.Fatalf("flush the base: %v", err)
	}
	return path
}

// The base decides every routing answer and used to be the one thing nobody
// could look at. The three counts have to sit next to each other, because the
// gap between them is the diagnosis.
func TestTheBaseCanBeRead(t *testing.T) {
	out, err := cli(t, "--config", based(t), "metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	for _, want := range []string{"ripgrep", "fixture.search", "tries", "failed", "priced"} {
		if !strings.Contains(out, want) {
			t.Errorf("the base reads without %q:\n%s", want, out)
		}
	}
	// The provider that only refused must show its record and no price, or the
	// screen is telling the same lie the funnel used to.
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "fixture.search") {
			continue
		}
		if !strings.Contains(line, "3        3        0") {
			t.Errorf("fixture.search reads %q, want three tries, three failed, none priced", line)
		}
		if !strings.Contains(line, "-") {
			t.Errorf("a provider with no successful call was given an average: %q", line)
		}
	}
}

// Emptying the whole base is the one act here that destroys something nothing
// can rebuild, so the word 'clear' is not enough on its own.
func TestClearingEverythingHasToBeSaidOutLoud(t *testing.T) {
	out, err := cli(t, "--config", based(t), "metrics", "clear")
	if err == nil {
		t.Fatalf("a bare clear emptied the base:\n%s", out)
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", got)
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("the refusal %q does not say how to mean it", err)
	}
}

// The surgical half, and the reason this verb exists at all: the poisoned rows
// go and the honest ones stay. Deleting the file was the old cure and it took
// both.
func TestClearingOneImplementationLeavesTheRest(t *testing.T) {
	cfg := based(t)
	out, err := cli(t, "--config", cfg, "metrics", "clear", "--implementation", "fixture.search")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !strings.Contains(out, "fixture.search") || !strings.Contains(out, "3 attempt") {
		t.Errorf("the clear does not say what went: %q", out)
	}

	after, err := cli(t, "--config", cfg, "metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if strings.Contains(after, "fixture.search") {
		t.Errorf("fixture.search survived its own clear:\n%s", after)
	}
	if !strings.Contains(after, "ripgrep") {
		t.Errorf("clearing one implementation took the others with it:\n%s", after)
	}
}

// A narrowing flag is itself a statement of intent, so it needs no --all. But
// naming something the base has never heard of must not read as success.
func TestClearingWhatWasNeverThereSaysSo(t *testing.T) {
	out, err := cli(t, "--config", based(t), "metrics", "clear", "--implementation", "grep")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !strings.Contains(out, "nothing to clear") {
		t.Errorf("clearing an unknown implementation reported %q", out)
	}
}

func TestClearingAllEmptiesTheBase(t *testing.T) {
	cfg := based(t)
	if _, err := cli(t, "--config", cfg, "metrics", "clear", "--all"); err != nil {
		t.Fatalf("clear --all: %v", err)
	}
	after, err := cli(t, "--config", cfg, "metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if !strings.Contains(after, "holds nothing") {
		t.Errorf("the base still answers after --all:\n%s", after)
	}
}
