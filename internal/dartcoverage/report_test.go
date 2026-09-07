package dartcoverage

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
	"github.com/Tutitoos/atenea/internal/benchmark/corpus"
)

func TestDefaultReportIsClosedAndHonest(t *testing.T) {
	report := DefaultReport()
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 3 {
		t.Fatalf("rows = %d, want three capability rows", len(report.Rows))
	}
	for _, row := range report.Rows {
		if row.Language != "dart" {
			t.Fatalf("row language = %q", row.Language)
		}
		if row.Connected != Unknown && row.EvidenceLevel != RealProvider && row.EvidenceLevel != RealClient {
			t.Fatalf("row %s overclaims connection: %+v", row.Capability, row)
		}
	}
	implementation := findRow(report, CapabilityImplementations)
	if implementation.Declared != Unsupported || implementation.FunctionallyTested != Unsupported {
		t.Fatalf("Dart implementations were presented as supported: %+v", implementation)
	}
	if implementation.Connected != Unknown {
		t.Fatalf("unsupported Dart implementation connection should remain unknown: %+v", implementation)
	}
	if len(report.Components) != 1 || report.Components[0].Status != Proven || report.Components[0].EvidenceLevel != ComponentLocal {
		t.Fatalf("component evidence was not separated: %+v", report.Components)
	}
	if len(report.Attempts) != 1 || report.Attempts[0].Status != "failed" || report.Attempts[0].EvidenceLevel != Pending || !strings.Contains(report.Attempts[0].Error, "LadybugDB native support is unavailable") {
		t.Fatalf("provider attempt evidence = %+v", report.Attempts)
	}
	if report.ProviderBase.Revision != KivgraphRevision || report.ProviderBase.Name != "Kivgraph" {
		t.Fatalf("provider baseline = %+v", report.ProviderBase)
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(data))
	for _, claim := range []string{"parsed by", "syntax valid", "source parser"} {
		if strings.Contains(lower, claim) {
			t.Fatalf("report retained an unsupported source interpretation claim %q", claim)
		}
	}
}

func TestReportJSONIsDeterministic(t *testing.T) {
	report := DefaultReport()
	first, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	second, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two serializations of the same report differ")
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["date"] != "2026-09-06" || decoded["matrix_version"] != MatrixVersion {
		t.Fatalf("report identity = %#v", decoded)
	}
	provider, ok := decoded["provider_base"].(map[string]any)
	if !ok || provider["revision"] != KivgraphRevision {
		t.Fatalf("provider baseline metadata = %#v", decoded["provider_base"])
	}
	markdown, err := report.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markdown, "/Users/") || strings.Contains(markdown, "| proven | proven |") {
		t.Fatalf("markdown leaked an absolute path or an unearned real claim: %s", markdown)
	}
	if !strings.Contains(markdown, "LadybugDB native support is unavailable") || !strings.Contains(markdown, "Provider attempts") {
		t.Fatalf("markdown omitted the failed provider attempt: %s", markdown)
	}
}

func TestValidateRejectsRealClaimsWithoutEvidence(t *testing.T) {
	report := DefaultReport()
	row := &report.Rows[0]
	row.Connected = Proven
	row.EvidenceLevel = Local
	if err := report.Validate(); err == nil {
		t.Fatal("local evidence was accepted as a real connection")
	}

	report = DefaultReport()
	report.Rows[2].FunctionallyTested = Proven
	if err := report.Validate(); err == nil {
		t.Fatal("Dart semantic implementations were accepted as proven")
	}
}

func TestValidateRejectsMissingRowsAndVocabulary(t *testing.T) {
	report := DefaultReport()
	report.Rows = report.Rows[:2]
	if err := report.Validate(); err == nil {
		t.Fatal("report with a missing capability row was accepted")
	}

	report = DefaultReport()
	report.Rows[0].Connected = Status("maybe")
	if err := report.Validate(); err == nil {
		t.Fatal("unknown status was accepted")
	}
}

func TestRunRevalidatesS03S07WithoutChangingCorpus(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S03")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if active && (fixture.Language != "dart" || !strings.Contains(string(fixture.Bytes), "MemoryRepository implements Repository")) {
		t.Fatal("sealed Dart context fixture was not consumed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(file)
	fixtureRoot := filepath.Join(root, "testdata")
	corpusRoot := filepath.Join(root, "..", "..", "benchmarks", "corpus", "v1")
	before, err := corpus.LoadSnapshot(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(Options{FixtureRoot: fixtureRoot, CorpusRoot: corpusRoot, CorpusSHA256: corpus.CanonicalCorpusSHA256, Date: "2026-09-06"})
	if err != nil {
		t.Fatal(err)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(report.Baseline) != 2 || report.Baseline[0].ID != "S03" || report.Baseline[1].ID != "S07" {
		t.Fatalf("baseline = %+v", report.Baseline)
	}
	if report.Baseline[0].State != "passed" || report.Baseline[1].State != "unsupported" {
		t.Fatalf("baseline states = %+v", report.Baseline)
	}
	after, err := corpus.LoadSnapshot(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash != after.Hash || after.Hash != corpus.CanonicalCorpusSHA256 {
		t.Fatalf("corpus changed: before=%s after=%s", before.Hash, after.Hash)
	}
	if report.FixtureSHA256 == "" || report.CorpusSHA256 != corpus.CanonicalCorpusSHA256 {
		t.Fatalf("report hashes = %+v", report)
	}
	if strings.Contains(report.Rows[2].Evidence[0].Detail, "fixture set") {
		t.Fatal("symbol.implementations inherited a fixture validation claim")
	}
}

func TestDartImplementationPreflightDoesNotDispatch(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S07")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if active && (fixture.Check != "dart_implementations" || !strings.Contains(string(fixture.Bytes), "implements Repository")) {
		t.Fatal("sealed Dart implementation fixture was not consumed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	report, err := Run(Options{
		FixtureRoot:  filepath.Join(filepath.Dir(file), "testdata"),
		CorpusRoot:   filepath.Join(filepath.Dir(file), "..", "..", "benchmarks", "corpus", "v1"),
		CorpusSHA256: corpus.CanonicalCorpusSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	implementation := findRow(report, CapabilityImplementations)
	if implementation.FunctionallyTested != Unsupported {
		t.Fatalf("Dart semantic implementation state = %s", implementation.FunctionallyTested)
	}
}

func findRow(report Report, capability string) Row {
	for _, row := range report.Rows {
		if row.Capability == capability {
			return row
		}
	}
	return Row{}
}
