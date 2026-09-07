package selector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestQualityNeedsValidatedCompleteSamplesAndDoesNotFilterUnknown(t *testing.T) {
	book := selector.NewQualityBook(2)
	partial := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"rows": 1}, Evidence: []contract.QueryEvidence{{Completeness: "partial"}}}
	q := selector.Observe("code.context", "repo", "impl", "go", "v1", partial, nil)
	book.Record(q)
	if got, ok := book.Snapshot("code.context", "repo", "impl", "go"); !ok || got.Tested(1) {
		t.Fatalf("partial observation became tested: %+v, %v", got, ok)
	}
	if _, ok := book.Snapshot("code.context", "repo", "missing", "go"); ok {
		t.Fatal("unknown implementation acquired a quality observation")
	}
}

func TestQualityRanksOnlyTwoTestedImplementations(t *testing.T) {
	s, err := selector.New(selector.Config{QualityMinimumSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	repo := contract.NewRepository("repo", t.TempDir(), []string{"go"}, contract.ScaleSmall, contract.VCSAbsent, nil)
	impl := func(id string) contract.Implementation {
		return contract.Implementation{ID: id, Provider: id, Capability: "code.context", Health: contract.Health{State: contract.HealthAlive}, Cost: contract.Cost{Estimated: contract.Sample{Tokens: 1}}}
	}
	d, err := s.Select(selector.Request{Capability: "code.context", Repository: repo, Candidates: []contract.Implementation{impl("fast"), impl("slow")}, Reachable: []string{"fast", "slow"}, Language: "go", Quality: map[string]selector.QualityObservation{
		"fast": {ValidOutcome: true, Accepted: true, Complete: true, Samples: 1, Score: 0.2, Language: "go"},
		"slow": {ValidOutcome: true, Accepted: true, Complete: true, Samples: 1, Score: 0.9, Language: "go"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen.ID != "slow" {
		t.Fatalf("quality chose %s, want slow", d.Chosen.ID)
	}
	if len(d.Stages) < 4 || d.Stages[len(d.Stages)-2].Name != selector.StageQuality {
		t.Fatalf("quality stage missing: %+v", d.Stages)
	}
}

func TestPreferenceStillOutranksQuality(t *testing.T) {
	s, err := selector.New(selector.Config{QualityMinimumSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	repo := contract.NewRepository("repo", t.TempDir(), []string{"go"}, contract.ScaleSmall, contract.VCSAbsent, nil)
	impl := func(id string) contract.Implementation {
		return contract.Implementation{ID: id, Provider: id, Capability: "code.context", Health: contract.Health{State: contract.HealthAlive}}
	}
	out := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}}
	s.RecordOutcome("code.context", "repo", "quality", "go", "v1", out, nil)
	d, err := s.Select(selector.Request{Capability: "code.context", Repository: repo, Candidates: []contract.Implementation{impl("quality"), impl("preferred")}, Reachable: []string{"quality", "preferred"}, Prefer: "preferred", Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Chosen.ID != "preferred" {
		t.Fatalf("preference chose %s", d.Chosen.ID)
	}
}

func TestQualityBookPersistsAggregatesAndDoesNotMixToolVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.json")
	book := selector.NewQualityBookWithPath(2, path)
	ok := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}}
	failure := contract.Outcome{Verdict: contract.VerdictFailed, Result: nil}
	book.Record(selector.Observe("code.context", "repo", "impl", "go", "v1", ok, nil))
	book.Record(selector.Observe("code.context", "repo", "impl", "go", "v1", failure, context.Canceled))
	if got, ok := book.SnapshotVersion("code.context", "repo", "impl", "go", "v1"); !ok || got.Total != 2 || got.AcceptedCount != 1 || got.FailureCount != 1 || got.Score != 0.5 {
		t.Fatalf("aggregate = %+v, found=%v", got, ok)
	}
	book.Record(selector.Observe("code.context", "repo", "impl", "go", "v2", ok, nil))
	reloaded := selector.NewQualityBookWithPath(2, path)
	if got, ok := reloaded.SnapshotVersion("code.context", "repo", "impl", "go", "v1"); !ok || got.Total != 2 {
		t.Fatalf("v1 was not durable: %+v, found=%v", got, ok)
	}
	if _, ok := reloaded.SnapshotVersion("code.context", "repo", "impl", "go", "v3"); ok {
		t.Fatal("unknown current version inherited old quality")
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if !json.Valid(data) {
		t.Fatal("quality ledger is not JSON")
	}
}

func TestQualityCategoriesDoNotTurnPartialOrOutOfScopeIntoFailures(t *testing.T) {
	partial := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"rows": 1}, Evidence: []contract.QueryEvidence{{Completeness: "partial"}}}
	truncated := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"rows": 1}, Evidence: []contract.QueryEvidence{{Completeness: "COMPLETE", Truncated: true}}}
	outOfScope := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"rows": 1}, OutOfScope: 1, Evidence: []contract.QueryEvidence{{Completeness: "complete"}}}
	book := selector.NewQualityBook(1)
	for _, out := range []contract.Outcome{partial, truncated, outOfScope} {
		if err := book.Record(selector.Observe("code.context", "repo", "impl", "go", "v1", out, nil)); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := book.SnapshotVersion("code.context", "repo", "impl", "go", "v1")
	if !ok {
		t.Fatal("category observations were not recorded")
	}
	if got.Total != 3 || got.FailureCount != 0 || got.PartialCount != 2 || got.TruncatedCount != 1 || got.OutOfScope != 1 {
		t.Fatalf("category counts = %+v", got)
	}
}

func TestQualityPersistsAcceptedAndOutOfScopeFromSeparateSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.json")
	book := selector.NewQualityBookWithPath(1, path)
	accepted := contract.Outcome{
		Verdict: contract.VerdictOK, Result: map[string]any{"rows": 1},
		Evidence: []contract.QueryEvidence{{Completeness: "complete"}},
	}
	outOfScope := contract.Outcome{
		Verdict: contract.VerdictOK, Result: map[string]any{"rows": 1}, OutOfScope: 1,
		Evidence: []contract.QueryEvidence{{Completeness: "complete"}},
	}
	for _, out := range []contract.Outcome{accepted, outOfScope} {
		if err := book.Record(selector.Observe("code.context", "repo", "impl", "go", "v1", out, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := book.SnapshotVersion("code.context", "repo", "impl", "go", "v1"); !ok || got.Total != 2 || got.ValidCount != 2 || got.AcceptedCount != 1 || got.CompleteCount != 2 || got.OutOfScope != 1 || got.FailureCount != 0 || got.Score != 0.5 {
		t.Fatalf("aggregate = %+v, found=%v", got, ok)
	}
	reloaded := selector.NewQualityBookWithPath(1, path)
	if err := reloaded.LoadError(); err != nil {
		t.Fatalf("separate accepted/out-of-scope samples were not reloadable: %v", err)
	}
	if got, ok := reloaded.SnapshotVersion("code.context", "repo", "impl", "go", "v1"); !ok || got.Total != 2 || got.AcceptedCount != 1 || got.OutOfScope != 1 || got.Score != 0.5 {
		t.Fatalf("reloaded aggregate = %+v, found=%v", got, ok)
	}
}

func TestQualityFailureWithDiagnosticResultDoesNotBecomeCategory(t *testing.T) {
	tests := []struct {
		name   string
		result map[string]any
	}{
		{name: "complete diagnostic", result: map[string]any{"rows": 1}},
		{name: "structurally truncated diagnostic", result: map[string]any{"truncated": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "quality.json")
			book := selector.NewQualityBookWithPath(1, path)
			q := selector.Observe("code.context", "repo", "impl", "go", "v1", contract.Outcome{
				Verdict: contract.VerdictFailed, Result: test.result,
				Evidence: []contract.QueryEvidence{{Completeness: "COMPLETE", Truncated: true}},
			}, context.Canceled)
			if q.ValidCount != 0 || q.FailureCount != 1 || q.CompleteCount != 0 || q.PartialCount != 0 || q.TruncatedCount != 0 || q.Complete || q.Truncated {
				t.Fatalf("diagnostic failure was classified as a category: %+v", q)
			}
			if err := book.Record(q); err != nil {
				t.Fatal(err)
			}
			reloaded := selector.NewQualityBookWithPath(1, path)
			if err := reloaded.LoadError(); err != nil {
				t.Fatalf("diagnostic failure was not reloadable: %v", err)
			}
			got, ok := reloaded.SnapshotVersion("code.context", "repo", "impl", "go", "v1")
			if !ok || got.Total != 1 || got.ValidCount != 0 || got.FailureCount != 1 || got.CompleteCount != 0 || got.PartialCount != 0 || got.TruncatedCount != 0 {
				t.Fatalf("reloaded diagnostic failure = %+v, found=%v", got, ok)
			}
		})
	}
}

func TestQualityRejectsStructuralPartialPayloadDespiteCompleteEvidence(t *testing.T) {
	for _, result := range []map[string]any{
		{"truncated": true},
		{"source_trimmed": 3},
		{"rows": []any{map[string]any{"partial": "yes"}}},
	} {
		out := contract.Outcome{Verdict: contract.VerdictOK, Result: result, Evidence: []contract.QueryEvidence{{Completeness: "COMPLETE"}}}
		q := selector.Observe("code.context", "repo", "impl", "go", "v1", out, nil)
		if q.Accepted || q.Complete || q.TruncatedCount != 1 || q.PartialCount != 1 || q.FailureCount != 0 {
			t.Fatalf("structural partial was accepted: result=%v observation=%+v", result, q)
		}
	}
}

func TestQualityBooksMergeConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.json")
	first := selector.NewQualityBookWithPath(1, path)
	second := selector.NewQualityBookWithPath(1, path)
	for _, book := range []*selector.QualityBook{first, second} {
		out := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}, Evidence: []contract.QueryEvidence{{Completeness: "COMPLETE"}}}
		if err := book.Record(selector.Observe("code.context", "repo", "impl", "go", "v1", out, nil)); err != nil {
			t.Fatal(err)
		}
	}
	reloaded := selector.NewQualityBookWithPath(1, path)
	got, ok := reloaded.SnapshotVersion("code.context", "repo", "impl", "go", "v1")
	if !ok || got.Total != 2 {
		t.Fatalf("merged total = %+v, found=%v", got, ok)
	}
}

func TestQualityScopeUsesPhysicalRepositoryRootAcrossPathForms(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	path := filepath.Join(base, "quality.json")
	book := selector.NewQualityBookWithPath(1, path)
	out := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}, Evidence: []contract.QueryEvidence{{Completeness: "COMPLETE"}}}
	q := selector.Observe("code.context", "same-id", "impl", "go", "v1", out, nil)
	q.RepositoryRoot = repo
	if err := book.Record(q); err != nil {
		t.Fatal(err)
	}
	if got, ok := book.SnapshotVersion("code.context", alias, "impl", "go", "v1"); !ok || got.Total != 1 {
		t.Fatalf("alias did not resolve physical scope: %+v, found=%v", got, ok)
	}
	reloaded := selector.NewQualityBookWithPath(1, path)
	if got, ok := reloaded.SnapshotVersion("code.context", repo, "impl", "go", "v1"); !ok || got.Total != 1 {
		t.Fatalf("restart did not retain physical scope: %+v, found=%v", got, ok)
	}
}

func TestSelectorRejectsCorruptQualityLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.json")
	if err := os.WriteFile(path, []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := selector.New(selector.Config{QualityPath: path}); err == nil {
		t.Fatal("corrupt quality ledger was accepted as empty history")
	}
}

func TestSelectorRejectsIncoherentQualityCountersAndDoesNotPublishPartialBook(t *testing.T) {
	valid := selector.Observe("code.context", "repo", "impl", "go", "v1", contract.Outcome{
		Verdict: contract.VerdictOK, Result: map[string]any{"ok": true},
		Evidence: []contract.QueryEvidence{{Completeness: "complete"}},
	}, nil)
	cases := []struct {
		name   string
		mutate func(*selector.QualityObservation)
	}{
		{name: "accepted exceeds total", mutate: func(q *selector.QualityObservation) { q.Total = 2; q.Samples = 2; q.AcceptedCount = 20 }},
		{name: "total zero with counts", mutate: func(q *selector.QualityObservation) { q.Total = 0; q.Samples = 0; q.ValidCount = 1 }},
		{name: "score out of range", mutate: func(q *selector.QualityObservation) { q.Score = 10 }},
		{name: "complete plus partial exceeds valid", mutate: func(q *selector.QualityObservation) { q.PartialCount = 1 }},
		{name: "accepted flag disagrees", mutate: func(q *selector.QualityObservation) { q.Accepted = false }},
		{name: "valid outcome flag disagrees", mutate: func(q *selector.QualityObservation) { q.ValidOutcome = false }},
		{name: "validated flag disagrees", mutate: func(q *selector.QualityObservation) { q.Validated = false }},
		{name: "truncated complete overlap", mutate: func(q *selector.QualityObservation) { q.Truncated = true; q.TruncatedCount = 1 }},
		{name: "empty persisted row", mutate: func(q *selector.QualityObservation) {
			*q = selector.QualityObservation{Capability: "code.context", Repository: "repo", Implementation: "impl", Language: "go", ToolVersion: "v1"}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			path := filepath.Join(t.TempDir(), "quality.json")
			data, err := json.Marshal([]selector.QualityObservation{row})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o640); err != nil {
				t.Fatal(err)
			}
			book := selector.NewQualityBookWithPath(1, path)
			if book.LoadError() == nil {
				t.Fatal("incoherent quality row was accepted")
			}
			if _, ok := book.SnapshotVersion("code.context", "repo", "impl", "go", "v1"); ok {
				t.Fatal("corrupt row was published into the book")
			}
			if _, err := selector.New(selector.Config{QualityPath: path}); err == nil {
				t.Fatal("selector published corrupt quality history")
			}
		})
	}
	{
		path := filepath.Join(t.TempDir(), "quality.json")
		data, err := json.Marshal([]selector.QualityObservation{valid})
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(`"Score":1`), []byte(`"Score":NaN`), 1)
		if err := os.WriteFile(path, data, 0o640); err != nil {
			t.Fatal(err)
		}
		if selector.NewQualityBookWithPath(1, path).LoadError() == nil {
			t.Fatal("non-finite JSON score was accepted")
		}
	}
}

func TestQualityLedgerRecoversDeadLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.json")
	if err := os.WriteFile(path+".lock", []byte("99999999\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	book := selector.NewQualityBookWithPath(1, path)
	out := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}, Evidence: []contract.QueryEvidence{{Completeness: "COMPLETE"}}}
	if err := book.Record(selector.Observe("code.context", "repo", "impl", "go", "v1", out, nil)); err != nil {
		t.Fatalf("stale lock was not recovered: %v", err)
	}
}
