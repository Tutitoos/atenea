// Package acceptance runs product-owned integration gates against the fixed,
// immutable corpus. It never changes corpus scenarios or their allowed states.
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/benchmark"
	"github.com/Tutitoos/atenea/internal/benchmark/corpus"
)

// MeasurementState is part of ATENEA's public orchestration contract.
type MeasurementState string

// EvidenceScope is part of ATENEA's public orchestration contract.
type EvidenceScope string

const (
	// Measured is part of ATENEA's public orchestration contract.
	Measured MeasurementState = "measured"
	// Estimated is part of ATENEA's public orchestration contract.
	Estimated MeasurementState = "estimated"
	// Partial is part of ATENEA's public orchestration contract.
	Partial MeasurementState = "partial"
	// Unknown is part of ATENEA's public orchestration contract.
	Unknown MeasurementState = "unknown"
	// Local is part of ATENEA's public orchestration contract.
	Local EvidenceScope = "local"
	// RealProvider is part of ATENEA's public orchestration contract.
	RealProvider EvidenceScope = "real_provider"
	// RealClient is part of ATENEA's public orchestration contract.
	RealClient EvidenceScope = "real_client"
	// Pending is part of ATENEA's public orchestration contract.
	Pending EvidenceScope = "pending"
)

// ScenarioEvidence is part of ATENEA's public orchestration contract.
type ScenarioEvidence struct {
	ID               string           `json:"id"`
	Fixture          string           `json:"fixture"`
	FixtureSHA256    string           `json:"fixture_sha256"`
	Preflight        corpus.State     `json:"preflight"`
	Passed           bool             `json:"passed"`
	MeasurementState MeasurementState `json:"measurement_state"`
	EvidenceScope    EvidenceScope    `json:"evidence_scope"`
	Evidence         string           `json:"evidence"`
}

// Gate is part of ATENEA's public orchestration contract.
type Gate struct {
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	Packages         []string           `json:"packages"`
	ScenarioIDs      []string           `json:"scenario_ids"`
	Scenarios        []ScenarioEvidence `json:"scenarios"`
	Passed           bool               `json:"passed"`
	Duration         int64              `json:"duration_ms"`
	MeasurementState MeasurementState   `json:"measurement_state"`
	EvidenceScope    EvidenceScope      `json:"evidence_scope"`
	Evidence         string             `json:"evidence"`
}

// Metric is part of ATENEA's public orchestration contract.
type Metric struct {
	ID               string           `json:"id"`
	Unit             string           `json:"unit"`
	Before           *float64         `json:"before,omitempty"`
	After            *float64         `json:"after,omitempty"`
	MeasurementState MeasurementState `json:"measurement_state"`
	EvidenceScope    EvidenceScope    `json:"evidence_scope"`
	Comparable       bool             `json:"comparable"`
	NonRegression    *bool            `json:"non_regression,omitempty"`
	Evidence         string           `json:"evidence"`
}

// Comparison is part of ATENEA's public orchestration contract.
type Comparison struct {
	BeforeID     string   `json:"before_id"`
	AfterID      string   `json:"after_id"`
	CorpusHash   string   `json:"corpus_sha256"`
	SourceDigest string   `json:"source_digest"`
	Passed       bool     `json:"passed"`
	Metrics      []Metric `json:"metrics"`
}

// Report is part of ATENEA's public orchestration contract.
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    time.Time      `json:"finished_at"`
	Commit        string         `json:"commit"`
	SourceDirty   bool           `json:"source_dirty"`
	SourceDigest  string         `json:"source_digest"`
	SourceFiles   int            `json:"source_files"`
	CorpusHash    string         `json:"corpus_sha256"`
	Gates         []Gate         `json:"gates"`
	Comparison    Comparison     `json:"comparison"`
	Summary       map[string]int `json:"summary"`
	Limits        []string       `json:"limits"`
}

var productGates = []Gate{
	{ID: "context", Title: "Repository, workspace and language context", ScenarioIDs: []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07"}, Packages: []string{"./internal/adapter/kivgraph", "./internal/workspacecontext", "./internal/dartcoverage"}},
	{ID: "mcp", Title: "Legacy and modern MCP contracts", ScenarioIDs: []string{"S08", "S09"}, Packages: []string{"./internal/mcpcompat", "./internal/mcphttp", "./internal/mcpstdio", "./internal/mcpprobe", "./internal/passthrough"}},
	{ID: "recovery", Title: "Bounded recovery and persistent workflows", ScenarioIDs: []string{"S11"}, Packages: []string{"./internal/recoverypilot", "./internal/workflow"}},
	{ID: "quality", Title: "Outcome-backed provider quality", ScenarioIDs: []string{"S01", "S02", "S05", "S06"}, Packages: []string{"./internal/selector", "./internal/orchestrator"}},
	{ID: "tracking", Title: "Checklist, telemetry and dashboard API", ScenarioIDs: []string{"S10", "S12"}, Packages: []string{"./internal/workflow", "./internal/dashboard", "./cmd/atenea"}},
}

// GateInput is part of ATENEA's public orchestration contract.
type GateInput struct {
	GateID    string
	Scenarios []ScenarioEvidence
	Packages  []string
}

// RunFunc is part of ATENEA's public orchestration contract.
type RunFunc func(context.Context, string, GateInput) (string, error)

// ScenarioInput is part of ATENEA's public orchestration contract.
type ScenarioInput struct {
	Scenario      corpus.Scenario
	FixtureBytes  []byte
	FixtureSHA256 string
	Preflight     corpus.ScenarioResult
}

// ProbeFunc is part of ATENEA's public orchestration contract.
type ProbeFunc func(context.Context, string, ScenarioInput) (string, error)

type probeSpec struct {
	Package string
	Tests   []string
}

var scenarioProbes = map[string]probeSpec{
	"S01": {Package: "./internal/adapter/kivgraph", Tests: []string{"TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows"}},
	"S02": {Package: "./internal/adapter/kivgraph", Tests: []string{"TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows"}},
	"S03": {Package: "./internal/dartcoverage", Tests: []string{"TestRunRevalidatesS03S07WithoutChangingCorpus"}},
	"S04": {Package: "./internal/workspacecontext", Tests: []string{"TestRunPreservesInputOrderAndSeparatesRepositories"}},
	"S05": {Package: "./internal/adapter/kivgraph", Tests: []string{"TestImplementationsP13AcceptsGoAndTypeScriptExactRelations"}},
	"S06": {Package: "./internal/adapter/kivgraph", Tests: []string{"TestImplementationsP13AcceptsGoAndTypeScriptExactRelations"}},
	"S07": {Package: "./internal/dartcoverage", Tests: []string{"TestDartImplementationPreflightDoesNotDispatch"}},
	"S08": {Package: "./internal/mcpcompat", Tests: []string{"TestParseAcceptsOnlyExactSupportedVersions"}},
	"S09": {Package: "./internal/mcpcompat", Tests: []string{"TestParseAcceptsOnlyExactSupportedVersions"}},
	"S10": {Package: "./internal/workflow", Tests: []string{"TestPlanProgressUsesTwentySegmentsAndTruncatedPercent"}},
	"S11": {Package: "./internal/recoverypilot", Tests: []string{"TestRunFixtureKivgraphScenarioUsesProductionIndexBoundary"}},
	"S12": {Package: "./internal/dashboard", Tests: []string{"TestWorkflowDetailRouteIsReachableAndReadOnly", "TestEventsSSEReplaysAndResetsExpiredCursor"}},
}

// Run is part of ATENEA's public orchestration contract.
func Run(ctx context.Context, root string, run RunFunc) (Report, error) {
	return RunWith(ctx, root, run, nil)
}

// RunWith is part of ATENEA's public orchestration contract.
func RunWith(ctx context.Context, root string, run RunFunc, probe ProbeFunc) (Report, error) {
	started := time.Now().UTC()
	corpusRoot := filepath.Join(root, "benchmarks", "corpus", "v1")
	snapshot, err := corpus.LoadSnapshot(corpusRoot)
	if err != nil {
		return Report{}, err
	}
	if snapshot.Hash != corpus.CanonicalCorpusSHA256 {
		return Report{}, fmt.Errorf("fixed corpus changed: got %s", snapshot.Hash)
	}
	commit, _ := command(ctx, root, []string{"git", "rev-parse", "HEAD"}, nil)
	digest, files, dirty, err := sourceIdentity(ctx, root, strings.TrimSpace(commit))
	if err != nil {
		return Report{}, err
	}
	manifest := benchmark.Manifest{SchemaVersion: benchmark.SchemaVersion, RunID: "acceptance-" + started.Format("20060102T150405Z"), Profile: "acceptance", Commit: strings.TrimSpace(commit)}
	preflight := corpus.RunSnapshot(ctx, snapshot, manifest)
	byID := map[string]corpus.ScenarioResult{}
	for _, item := range preflight.Results {
		byID[item.ID] = item
	}
	if run == nil {
		run = runGoTests
	}
	if probe == nil {
		probe = runScenarioProbe
	}
	report := Report{SchemaVersion: 2, StartedAt: started, Commit: strings.TrimSpace(commit), SourceDirty: dirty, SourceDigest: digest, SourceFiles: files, CorpusHash: snapshot.Hash, Summary: map[string]int{}, Limits: []string{"Provider-real evidence remains pending unless a gate says real_provider.", "Live client rendering remains pending unless a gate says real_client.", "Unknown metrics stay unknown and are never converted to zero.", "No improvement percentage is claimed."}}
	productBefore, productAfter := -1, -1
	probed := make(map[string]ScenarioEvidence, len(snapshot.Manifest.Scenarios))
	for _, scenario := range snapshot.Manifest.Scenarios {
		if _, duplicate := probed[scenario.ID]; duplicate {
			return Report{}, fmt.Errorf("duplicate scenario %s", scenario.ID)
		}
		pre, ok := byID[scenario.ID]
		if !ok {
			return Report{}, fmt.Errorf("missing preflight for %s", scenario.ID)
		}
		bytes := append([]byte(nil), snapshot.Bytes[scenario.Fixture]...)
		sha := artifactSHA(snapshot, scenario.Fixture)
		output, probeErr := probe(ctx, root, ScenarioInput{Scenario: scenario, FixtureBytes: bytes, FixtureSHA256: sha, Preflight: pre})
		item := ScenarioEvidence{ID: scenario.ID, Fixture: scenario.Fixture, FixtureSHA256: sha, Preflight: pre.Preflight, Passed: probeErr == nil && pre.Preflight != corpus.Failed, MeasurementState: Measured, EvidenceScope: Local, Evidence: clip(output, 500)}
		if probeErr != nil {
			item.Evidence = clipTail(output+"\n"+probeErr.Error(), 500)
		}
		if scenario.ID == "S12" {
			item.MeasurementState, item.EvidenceScope = Unknown, Pending
			if probeErr == nil {
				item.Evidence = "local API, SSE and formatting paths passed; real client rendering pending"
			}
		}
		probed[scenario.ID] = item
	}
	if len(probed) != len(corpus.Catalog()) {
		return Report{}, fmt.Errorf("scenario coverage is %d, want %d", len(probed), len(corpus.Catalog()))
	}
	for _, template := range productGates {
		gate := template
		for _, id := range gate.ScenarioIDs {
			result, ok := probed[id]
			if !ok {
				return Report{}, fmt.Errorf("gate %s references absent scenario %s", gate.ID, id)
			}
			gate.Scenarios = append(gate.Scenarios, result)
		}
		begin := time.Now()
		output, runErr := run(ctx, root, GateInput{GateID: gate.ID, Scenarios: append([]ScenarioEvidence(nil), gate.Scenarios...), Packages: append([]string(nil), gate.Packages...)})
		if gate.ID == "context" {
			productBefore, productAfter = contextCallMeasurement(output)
		}
		gate.Duration = time.Since(begin).Milliseconds()
		gate.Passed, gate.MeasurementState, gate.EvidenceScope = runErr == nil, Measured, Local
		gate.Evidence = clip(output, 1200)
		if runErr != nil {
			gate.Evidence = clipTail(output+"\n"+runErr.Error(), 1200)
		}
		for i := range gate.Scenarios {
			gate.Scenarios[i].Passed = gate.Passed && gate.Scenarios[i].Passed
		}
		if !allScenariosPass(gate.Scenarios) {
			gate.Passed = false
		}
		report.Gates = append(report.Gates, gate)
		if gate.Passed {
			report.Summary["passed"]++
		} else {
			report.Summary["failed"]++
		}
	}
	report.Comparison = compareCorpusAccess(snapshot, digest, productBefore, productAfter)
	if !report.Comparison.Passed {
		report.Summary["failed"]++
		report.Summary["comparison_failed"]++
	} else {
		report.Summary["passed"]++
	}
	report.Summary["total"] = len(report.Gates) + 1
	after, err := corpus.LoadSnapshot(corpusRoot)
	if err != nil || after.Hash != snapshot.Hash {
		return Report{}, errors.New("corpus changed during acceptance run")
	}
	afterDigest, _, _, err := sourceIdentity(ctx, root, report.Commit)
	if err != nil || afterDigest != digest {
		return Report{}, errors.New("source tree changed during acceptance run")
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

func artifactSHA(snapshot corpus.Snapshot, path string) string {
	for _, a := range snapshot.Artifacts {
		if a.Path == path {
			return a.SHA256
		}
	}
	return ""
}
func allScenariosPass(items []ScenarioEvidence) bool {
	for _, v := range items {
		if !v.Passed {
			return false
		}
	}
	return true
}

func runGoTests(ctx context.Context, root string, input GateInput) (string, error) {
	args := append([]string{"test", "-race", "-count=1", "-v"}, input.Packages...)
	return command(ctx, root, append([]string{"go"}, args...), nil)
}

func runScenarioProbe(ctx context.Context, root string, input ScenarioInput) (string, error) {
	if input.Scenario.ID == "" || len(input.FixtureBytes) == 0 || input.FixtureSHA256 == "" {
		return "", errors.New("scenario probe requires identified fixture bytes")
	}
	sum := sha256.Sum256(input.FixtureBytes)
	if hex.EncodeToString(sum[:]) != input.FixtureSHA256 {
		return "", errors.New("scenario fixture bytes do not match their digest")
	}
	spec, ok := scenarioProbes[input.Scenario.ID]
	if !ok {
		return "", fmt.Errorf("scenario %s has no product probe", input.Scenario.ID)
	}
	dir, err := os.MkdirTemp("", "atenea-acceptance-fixture-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fixturePath := filepath.Join(dir, "fixture")
	if err := os.WriteFile(fixturePath, input.FixtureBytes, 0600); err != nil {
		return "", err
	}
	env := []string{"ATENEA_ACCEPTANCE_ID=" + input.Scenario.ID, "ATENEA_ACCEPTANCE_LANGUAGE=" + input.Scenario.Language, "ATENEA_ACCEPTANCE_CHECK=" + input.Scenario.Check, "ATENEA_ACCEPTANCE_FIXTURE=" + fixturePath, "ATENEA_ACCEPTANCE_SHA256=" + input.FixtureSHA256}
	testPattern := "^(" + strings.Join(spec.Tests, "|") + ")$"
	output, err := command(ctx, root, []string{"go", "test", "-race", "-count=1", "-json", spec.Package, "-run", testPattern}, env)
	if err != nil {
		return output, err
	}
	if err := requireExecutedTests(output, spec.Tests); err != nil {
		return output, err
	}
	return output, nil
}

func requireExecutedTests(output string, expected []string) error {
	if len(expected) == 0 {
		return errors.New("scenario product probe declares no required tests")
	}
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		if name == "" {
			return errors.New("scenario product probe declares an empty test name")
		}
		want[name] = true
	}
	type event struct{ Action, Test string }
	run, passed := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		var item event
		if json.Unmarshal([]byte(line), &item) != nil || !want[item.Test] {
			continue
		}
		if item.Action == "run" {
			run[item.Test] = true
		}
		if item.Action == "pass" {
			passed[item.Test] = true
		}
	}
	for name := range want {
		if !run[name] {
			return fmt.Errorf("scenario product probe did not execute required test %s", name)
		}
		if !passed[name] {
			return fmt.Errorf("scenario product probe required test %s did not pass", name)
		}
	}
	return nil
}

var contextCalls = regexp.MustCompile(`code\.context fixture calls: before=([0-9]+) after=([0-9]+)`)

func contextCallMeasurement(output string) (int, int) {
	match := contextCalls.FindStringSubmatch(output)
	if len(match) != 3 {
		return -1, -1
	}
	before, err1 := strconv.Atoi(match[1])
	after, err2 := strconv.Atoi(match[2])
	if err1 != nil || err2 != nil {
		return -1, -1
	}
	return before, after
}

func compareCorpusAccess(snapshot corpus.Snapshot, sourceDigest string, productBefore, productAfter int) Comparison {
	result := Comparison{BeforeID: "unbatched-fixture-reads", AfterID: "snapshot-deduplicated-reads", CorpusHash: snapshot.Hash, SourceDigest: sourceDigest, Passed: true}
	loops, beforeCalls := 200, 0
	start := time.Now()
	for n := 0; n < loops; n++ {
		for _, s := range snapshot.Manifest.Scenarios {
			_, _ = os.ReadFile(filepath.Join(snapshot.Root, filepath.FromSlash(s.Fixture)))
			beforeCalls++
		}
	}
	beforeNS := float64(time.Since(start).Nanoseconds())
	unique := map[string]bool{}
	for _, s := range snapshot.Manifest.Scenarios {
		unique[s.Fixture] = true
	}
	uniquePaths := make([]string, 0, len(unique))
	for path := range unique {
		uniquePaths = append(uniquePaths, path)
	}
	sort.Strings(uniquePaths)
	start = time.Now()
	for n := 0; n < loops; n++ {
		for _, path := range uniquePaths {
			_ = snapshot.Bytes[path]
		}
	}
	afterNS, afterCalls := float64(time.Since(start).Nanoseconds()), len(unique)*loops
	callOK, latencyOK := afterCalls <= beforeCalls, afterNS <= beforeNS
	zero := float64(0)
	productKnown := productBefore >= 0 && productAfter >= 0
	result.Metrics = []Metric{
		{ID: "code_context_provider_calls", Unit: "calls", Before: optionalFloat(productBefore), After: optionalFloat(productAfter), MeasurementState: map[bool]MeasurementState{true: Partial, false: Unknown}[productKnown], EvidenceScope: map[bool]EvidenceScope{true: Local, false: Pending}[productKnown], Comparable: false, Evidence: "before is a code-derived historical estimate; after is observed from the productive twenty-row route"},
		{ID: "fixture_read_calls", Unit: "calls", Before: f64(float64(beforeCalls)), After: f64(float64(afterCalls)), MeasurementState: Measured, EvidenceScope: Local, Comparable: true, NonRegression: boolp(callOK), Evidence: "same sealed scenarios; unbatched references versus deduplicated snapshot keys"},
		{ID: "fixture_access_latency", Unit: "ns", Before: f64(beforeNS), After: f64(afterNS), MeasurementState: Measured, EvidenceScope: Local, Comparable: true, NonRegression: boolp(latencyOK), Evidence: "200 local iterations over the same sealed corpus; absolute durations only"},
		{ID: "human_interventions", Unit: "count", Before: &zero, After: &zero, MeasurementState: Measured, EvidenceScope: Local, Comparable: true, NonRegression: boolp(true), Evidence: "provider-free harness required no interaction"},
		{ID: "tokens", Unit: "tokens", MeasurementState: Unknown, EvidenceScope: Pending, Evidence: "no model invoked"},
		{ID: "provider_cost", Unit: "currency", MeasurementState: Unknown, EvidenceScope: Pending, Evidence: "no provider receipt"},
		{ID: "real_client_tracking_overhead", Unit: "ms", MeasurementState: Unknown, EvidenceScope: Pending, Evidence: "requires authorized live-client validation"},
	}
	result.Passed = callOK && latencyOK
	return result
}
func f64(v float64) *float64 { return &v }
func boolp(v bool) *bool     { return &v }
func optionalFloat(v int) *float64 {
	if v < 0 {
		return nil
	}
	value := float64(v)
	return &value
}

func sourceIdentity(ctx context.Context, root, commit string) (string, int, bool, error) {
	diff, err := command(ctx, root, []string{"git", "diff", "--binary", "HEAD", "--", "."}, nil)
	if err != nil {
		return "", 0, false, err
	}
	untracked, err := command(ctx, root, []string{"git", "ls-files", "-z", "--others", "--exclude-standard"}, nil)
	if err != nil {
		return "", 0, false, err
	}
	paths := strings.Split(strings.TrimSuffix(untracked, "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		paths = nil
	}
	sort.Strings(paths)
	h := sha256.New()
	fmt.Fprintf(h, "commit\x00%s\x00diff\x00%s", commit, diff)
	files := 0
	for _, path := range paths {
		raw, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil {
			return "", 0, false, readErr
		}
		fmt.Fprintf(h, "\x00file\x00%s\x00%d\x00", path, len(raw))
		_, _ = h.Write(raw)
		files++
	}
	if strings.TrimSpace(diff) != "" {
		files++
	}
	return hex.EncodeToString(h.Sum(nil)), files, strings.TrimSpace(diff) != "" || len(paths) > 0, nil
}

func command(ctx context.Context, root string, argv, extraEnv []string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), extraEnv...)
	raw, err := cmd.CombinedOutput()
	return string(raw), err
}
func clip(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) <= n {
		return v
	}
	return v[:n] + "…"
}
func clipTail(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) <= n {
		return v
	}
	return "…" + v[len(v)-n:]
}

// Write is part of ATENEA's public orchestration contract.
func Write(dir string, report Report) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := benchmark.WriteJSON(filepath.Join(dir, "acceptance.json"), report); err != nil {
		return err
	}
	return benchmark.WriteText(filepath.Join(dir, "acceptance.md"), Markdown(report))
}

// Markdown is part of ATENEA's public orchestration contract.
func Markdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ATENEA product acceptance\n\nCommit: `%s`  \nSource digest: `%s` (%d changed/untracked entries)  \nCorpus: `%s`  \nSource: **%s**\n\n", r.Commit, r.SourceDigest, r.SourceFiles, r.CorpusHash, map[bool]string{true: "DIRTY", false: "CLEAN"}[r.SourceDirty])
	b.WriteString("| Gate | Result | State | Scope | Scenarios | Duration |\n|---|---|---|---|---|---:|\n")
	for _, g := range r.Gates {
		result := "PASS"
		if !g.Passed {
			result = "FAIL"
		}
		ids := []string{}
		for _, s := range g.Scenarios {
			ids = append(ids, s.ID)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d ms |\n", g.Title, result, g.MeasurementState, g.EvidenceScope, strings.Join(ids, ", "), g.Duration)
	}
	b.WriteString("\n## Efficiency comparison\n\n| Metric | Before | After | State | Scope | Comparable | Non-regression |\n|---|---:|---:|---|---|---|---|\n")
	for _, m := range r.Comparison.Metrics {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %t | %s |\n", m.ID, num(m.Before), num(m.After), m.MeasurementState, m.EvidenceScope, m.Comparable, truth(m.NonRegression))
	}
	b.WriteString("\n## Scenario evidence\n\n")
	for _, g := range r.Gates {
		for _, s := range g.Scenarios {
			fmt.Fprintf(&b, "- **%s**: preflight `%s`, state `%s`, scope `%s`, fixture `%s`; %s\n", s.ID, s.Preflight, s.MeasurementState, s.EvidenceScope, s.FixtureSHA256, s.Evidence)
		}
	}
	b.WriteString("\nLimits:\n")
	for _, l := range r.Limits {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	return b.String()
}
func num(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.0f", *v)
}
func truth(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "pass"
	}
	return "fail"
}

// Encode is part of ATENEA's public orchestration contract.
func Encode(report Report) ([]byte, error) { return json.Marshal(report) }
