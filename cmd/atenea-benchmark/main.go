// Command atenea-benchmark runs Atenea's reproducible test and benchmark
// evidence suite and writes JSON, raw output and Markdown summaries.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/benchmark"
)

type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

type packageCoverage struct {
	total   int
	covered int
}

type testRun struct {
	Totals   benchmark.TestTotals
	Suites   []benchmark.TestSuite
	Coverage float64
	Err      error
}

type benchSpec struct {
	Name      string
	Category  string
	Package   string
	Function  string
	Benchtime string
}

var testPackages = []string{"./..."}
var docsRoot = "."

func main() {
	ctx := context.Background()
	output := flag.String("output", "benchmarks/runs/latest", "directory for run artifacts")
	profile := flag.String("profile", "quick", "quick, standard, qualification or stress")
	runs := flag.Int("benchmark-runs", 3, "independent process runs per benchmark")
	renderOnly := flag.Bool("render-only", false, "render docs from an existing summary")
	validateOnly := flag.Bool("validate-only", false, "validate an existing summary")
	input := flag.String("input", "benchmarks/runs/latest/summary.json", "summary used with render-only")
	flag.Parse()

	if *renderOnly || *validateOnly {
		data, err := os.ReadFile(*input)
		if err != nil {
			fatal(err)
		}
		var summary benchmark.Summary
		if err := json.Unmarshal(data, &summary); err != nil {
			fatal(err)
		}
		if err := benchmark.ValidateSummary(summary); err != nil {
			fatal(fmt.Errorf("validate benchmark summary: %w", err))
		}
		if *validateOnly {
			fmt.Printf("valid benchmark summary: %s\n", *input)
			return
		}
		if err := renderDocs(summary); err != nil {
			fatal(err)
		}
		return
	}
	if *runs < 1 {
		fatal(errors.New("--benchmark-runs must be positive"))
	}
	if (*profile == "qualification" || *profile == "stress") && *runs < 10 {
		*runs = 10
	}
	if err := os.RemoveAll(filepath.Join(*output, "raw")); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(*output, "raw"), 0o755); err != nil {
		fatal(err)
	}

	manifest := benchmark.NewManifest(ctx, *profile, "go run ./cmd/atenea-benchmark --profile "+*profile)
	tests := runTests(ctx, *output)
	results := runBenchmarks(ctx, *output, *runs, *profile)
	applyBaseline(results, *output, manifest)
	summary := benchmark.Summary{
		Manifest: manifest, Tests: tests.Totals, CoveragePercent: tests.Coverage,
		CoverageFloor: 60, CoverageTarget: 80, Suites: tests.Suites, Benchmarks: results,
	}
	if tests.Err != nil {
		summary.Warnings = append(summary.Warnings, tests.Err.Error())
	}
	summary.OverallStatus = benchmark.OverallStatus(summary)
	if err := benchmark.ValidateSummary(summary); err != nil {
		fatal(fmt.Errorf("validate benchmark summary: %w", err))
	}

	for _, path := range []string{
		filepath.Join(*output, "summary.json"), "benchmarks/summary.json",
	} {
		if err := benchmark.WriteJSON(path, summary); err != nil {
			fatal(err)
		}
	}
	if err := writeReport(filepath.Join(*output, "summary.md"), summary); err != nil {
		fatal(err)
	}
	if err := writeReport("benchmarks/summary.md", summary); err != nil {
		fatal(err)
	}
	if err := renderDocs(summary); err != nil {
		fatal(err)
	}
	fmt.Printf("%s tests=%d/%d coverage=%.1f%% benchmarks=%d\n",
		summary.OverallStatus.Indicator(), summary.Tests.Passed, summary.Tests.Executed,
		summary.CoveragePercent, len(summary.Benchmarks))
	if tests.Err != nil {
		os.Exit(1)
	}
}

func runTests(ctx context.Context, output string) testRun {
	return runTestsForPackages(ctx, output, testPackages...)
}

func runTestsForPackages(ctx context.Context, output string, packages ...string) testRun {
	profile := filepath.Join(output, "coverage.out")
	args := []string{"test", "-json", "-count=1", "-coverprofile", profile}
	args = append(args, packages...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Env = benchmarkEnvironment()
	cmd.Dir = repositoryRoot()
	var raw bytes.Buffer
	cmd.Stdout, cmd.Stderr = &raw, &raw
	err := cmd.Run()
	_ = os.WriteFile(filepath.Join(output, "raw", "tests.jsonl"), raw.Bytes(), 0o644)

	byPackage := map[string]*benchmark.TestSuite{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(raw.Bytes()))
	for scanner.Scan() {
		var event testEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Package == "" {
			continue
		}
		suite := byPackage[event.Package]
		if suite == nil {
			suite = &benchmark.TestSuite{Package: event.Package}
			byPackage[event.Package] = suite
		}
		if event.Test == "" {
			if event.Action == "pass" || event.Action == "fail" {
				suite.DurationMS = event.Elapsed * 1000
			}
			continue
		}
		key := event.Package + ":" + event.Test
		switch event.Action {
		case "run":
			if !seen[key] {
				suite.Discovered++
				seen[key] = true
			}
		case "pass":
			suite.Executed++
			suite.Passed++
		case "fail":
			suite.Executed++
			suite.Failed++
		case "skip":
			suite.Skipped++
		}
	}
	coverage, byCoverage, coverageErr := readCoverage(profile)
	if err == nil {
		err = coverageErr
	}
	var suites []benchmark.TestSuite
	totals := benchmark.TestTotals{}
	for _, suite := range byPackage {
		if suite.Discovered == 0 {
			continue
		}
		suite.CoveragePercent = byCoverage[suite.Package]
		suite.PassRate = benchmark.PassRate(suite.Passed, suite.Executed)
		suite.Status = benchmark.SuiteStatus(*suite, 60, 80)
		suites = append(suites, *suite)
		totals.Discovered += suite.Discovered
		totals.Executed += suite.Executed
		totals.Passed += suite.Passed
		totals.Failed += suite.Failed
		totals.Skipped += suite.Skipped
	}
	sortSuites(suites)
	totals.PassRate = benchmark.PassRate(totals.Passed, totals.Executed)
	return testRun{Totals: totals, Suites: suites, Coverage: coverage, Err: err}
}

func readCoverage(path string) (float64, map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	var total packageCoverage
	byPackage := map[string]packageCoverage{}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}
		total.total += statements
		if count > 0 {
			total.covered += statements
		}
		file := strings.SplitN(fields[0], ":", 2)[0]
		pkg := packageFromProfilePath(file)
		entry := byPackage[pkg]
		entry.total += statements
		if count > 0 {
			entry.covered += statements
		}
		byPackage[pkg] = entry
	}
	percent := 0.0
	if total.total > 0 {
		percent = float64(total.covered) / float64(total.total) * 100
	}
	percentByPackage := map[string]float64{}
	for pkg, value := range byPackage {
		if value.total > 0 {
			percentByPackage[pkg] = float64(value.covered) / float64(value.total) * 100
		}
	}
	return percent, percentByPackage, nil
}

func packageFromProfilePath(path string) string {
	const module = "github.com/Tutitoos/atenea/"
	path = strings.TrimPrefix(path, module)
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return module + path[:index]
	}
	return module + path
}

func runBenchmarks(ctx context.Context, output string, runs int, profile string) []benchmark.BenchmarkResult {
	specs := []benchSpec{
		{Name: "select-medium-catalog", Category: "micro", Package: "./internal/selector", Function: "BenchmarkSelectMediumCatalog", Benchtime: "100ms"},
		{Name: "record-measurement", Category: "micro", Package: "./internal/metrics", Function: "BenchmarkRecord", Benchtime: "1x"},
		{Name: "flush-persisted-measurement", Category: "persistence", Package: "./internal/metrics", Function: "BenchmarkFlushMeasurement", Benchtime: "100ms"},
		{Name: "plan-layers-medium-dag", Category: "micro", Package: "./pkg/contract", Function: "BenchmarkPlanLayersMediumDAG", Benchtime: "100ms"},
		{Name: "run-plan-concurrent-medium-dag", Category: "load", Package: "./internal/orchestrator", Function: "BenchmarkRunPlanConcurrentMediumDAG", Benchtime: "100ms"},
	}
	profileDir := filepath.Join(output, "profiles")
	if profile == "qualification" || profile == "stress" {
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			fatal(err)
		}
	}
	results := make([]benchmark.BenchmarkResult, 0, len(specs))
	for _, spec := range specs {
		var nsValues []float64
		var bytesOp, allocsOp float64
		var maxRSS, maxCPU float64
		var profilePaths []string
		for sample := 1; sample <= runs; sample++ {
			args := []string{"test", spec.Package, "-run", "^$", "-bench", "^" + spec.Function + "$", "-benchmem", "-benchtime=" + spec.Benchtime, "-count=1"}
			if profile == "qualification" || profile == "stress" {
				cpuProfile := filepath.Join(profileDir, fmt.Sprintf("%s-%02d.cpu.pprof", spec.Name, sample))
				memProfile := filepath.Join(profileDir, fmt.Sprintf("%s-%02d.mem.pprof", spec.Name, sample))
				args = append(args, "-cpuprofile", cpuProfile, "-memprofile", memProfile)
				profilePaths = append(profilePaths, cpuProfile, memProfile)
			}
			cmd := exec.CommandContext(ctx, "go", args...)
			cmd.Env = benchmarkEnvironment()
			cmd.Dir = repositoryRoot()
			raw, err := cmd.CombinedOutput()
			rawPath := filepath.Join(output, "raw", fmt.Sprintf("%s-%02d.txt", spec.Name, sample))
			_ = os.WriteFile(rawPath, raw, 0o644)
			ns, b, a, ok := parseBenchmarkOutput(string(raw))
			if err == nil && ok {
				nsValues = append(nsValues, ns)
				bytesOp, allocsOp = b, a
			}
			usage := benchmark.Usage(cmd.ProcessState)
			if float64(usage.RSSBytes) > maxRSS {
				maxRSS = float64(usage.RSSBytes)
			}
			if usage.CPUTimeMS > maxCPU {
				maxCPU = usage.CPUTimeMS
			}
		}
		result := benchmark.BenchmarkResult{
			Name: spec.Name, Category: spec.Category, Package: spec.Package,
			Command: "go test " + spec.Package + " -bench " + spec.Function,
			Samples: len(nsValues), BytesOp: bytesOp, AllocsOp: allocsOp, Valid: len(nsValues) > 0,
			RSSBytes: int64(maxRSS), CPUTimeMS: maxCPU,
			Profiles:    profilePaths,
			Limitations: []string{"Compare only with the same profile, dataset and compatible hardware."},
		}
		if spec.Benchtime == "1x" {
			result.Limitations = append(result.Limitations, "BenchmarkRecord is measured once per process because its bounded buffer intentionally changes the cost after saturation; a long calibration run would measure buffer shifting rather than a stable operation.")
		}
		if len(nsValues) == 0 {
			result.Status = benchmark.Red
			result.Limitations = append(result.Limitations, "No valid benchmark sample was produced.")
		} else {
			result.NanosecondsOp = benchmark.DistributionOf(nsValues).Median
			result.Throughput = 1e9 / result.NanosecondsOp
			result.LatencyMS = benchmark.DistributionOf(scale(nsValues, 1e-6))
			result.Status = benchmark.Green
			if len(nsValues) < 5 || result.LatencyMS.CoefficientVar > 10 {
				result.Status = benchmark.Orange
			}
		}
		results = append(results, result)
	}
	return results
}

func benchmarkEnvironment() []string {
	env := append([]string(nil), os.Environ()...)
	for index, value := range env {
		if strings.HasPrefix(value, "TMPDIR=") {
			env[index] = "TMPDIR=/tmp"
			return env
		}
	}
	return append(env, "TMPDIR=/tmp")
}

func repositoryRoot() string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}

func applyBaseline(results []benchmark.BenchmarkResult, output string, current benchmark.Manifest) {
	data, err := os.ReadFile(filepath.Join(output, "summary.json"))
	if err != nil {
		return
	}
	var previous benchmark.Summary
	if json.Unmarshal(data, &previous) != nil {
		return
	}
	if current.SourceDirty || previous.Manifest.SourceDirty ||
		current.Commit == "" || current.Commit != previous.Manifest.Commit ||
		current.Profile != previous.Manifest.Profile ||
		current.Environment.MachineModel != previous.Manifest.Environment.MachineModel ||
		current.Environment.MemoryGB != previous.Manifest.Environment.MemoryGB ||
		current.Environment.Arch != previous.Manifest.Environment.Arch {
		return
	}
	byName := map[string]benchmark.BenchmarkResult{}
	for _, result := range previous.Benchmarks {
		byName[result.Name] = result
	}
	for index := range results {
		old, ok := byName[results[index].Name]
		if !ok || old.NanosecondsOp <= 0 || results[index].NanosecondsOp <= 0 {
			continue
		}
		results[index].Baseline = previous.Manifest.RunID
		results[index].DeltaPercent = (results[index].NanosecondsOp - old.NanosecondsOp) / old.NanosecondsOp * 100
		if results[index].DeltaPercent > 10 {
			results[index].Status = benchmark.Red
		} else if results[index].DeltaPercent > 5 && results[index].Status == benchmark.Green {
			results[index].Status = benchmark.Orange
		}
	}
}

func parseBenchmarkOutput(raw string) (float64, float64, float64, bool) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		var ns float64
		var bytesOp, allocsOp float64
		for index := 0; index+1 < len(fields); index++ {
			value, parseErr := strconv.ParseFloat(fields[index], 64)
			if parseErr != nil {
				continue
			}
			if fields[index+1] == "ns/op" {
				ns = value
			}
			if fields[index+1] == "B/op" {
				bytesOp = value
			}
			if fields[index+1] == "allocs/op" {
				allocsOp = value
			}
		}
		if ns > 0 {
			return ns, bytesOp, allocsOp, true
		}
	}
	return 0, 0, 0, false
}

func scale(values []float64, factor float64) []float64 {
	result := make([]float64, len(values))
	for index, value := range values {
		result[index] = value * factor
	}
	return result
}

func sortSuites(suites []benchmark.TestSuite) {
	for i := 1; i < len(suites); i++ {
		for j := i; j > 0 && suites[j].Package < suites[j-1].Package; j-- {
			suites[j], suites[j-1] = suites[j-1], suites[j]
		}
	}
}

func writeReport(path string, summary benchmark.Summary) error {
	var out strings.Builder
	fmt.Fprintf(&out, "# Atenea benchmark summary\n\nRun: %s\nProfile: %s\nCommit: %s\nMachine: %s, %d GB\nOverall: %s\n\n",
		summary.Manifest.RunID, summary.Manifest.Profile, summary.Manifest.Commit,
		summary.Manifest.Environment.MachineModel, summary.Manifest.Environment.MemoryGB,
		summary.OverallStatus.Indicator())
	out.WriteString("## Tests\n\n| Package | Executed | Passed | Failed | Skipped | Coverage | Status |\n|---|---:|---:|---:|---:|---:|---|\n")
	for _, suite := range summary.Suites {
		fmt.Fprintf(&out, "| %s | %d | %d | %d | %d | %.1f%% | %s |\n", suite.Package, suite.Executed, suite.Passed, suite.Failed, suite.Skipped, suite.CoveragePercent, suite.Status.Indicator())
	}
	fmt.Fprintf(&out, "\nTotal: %d/%d passed, coverage %.1f%%.\n\n", summary.Tests.Passed, summary.Tests.Executed, summary.CoveragePercent)
	out.WriteString("## Benchmarks\n\n| Benchmark | Samples | ns/op | B/op | Allocs/op | Throughput/s | RSS | CPU ms | CV | Delta | Status |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, result := range summary.Benchmarks {
		fmt.Fprintf(&out, "| %s | %d | %.1f | %.1f | %.1f | %.1f | %d | %.1f | %.1f%% | %.1f%% | %s |\n", result.Name, result.Samples, result.NanosecondsOp, result.BytesOp, result.AllocsOp, result.Throughput, result.RSSBytes, result.CPUTimeMS, result.LatencyMS.CoefficientVar, result.DeltaPercent, result.Status.Indicator())
	}
	return benchmark.WriteText(path, out.String())
}

func renderDocs(summary benchmark.Summary) error {
	return renderDocsAt(summary, docsRoot)
}

func renderDocsAt(summary benchmark.Summary, root string) error {
	dir := filepath.Join(root, "docs", "content", "benchmarks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dataDir := filepath.Join(root, "docs", "data", "benchmarks")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := benchmark.WriteText(filepath.Join(dataDir, "latest.json"), string(data)+"\n"); err != nil {
		return err
	}
	status := summary.OverallStatus.Indicator()
	index := fmt.Sprintf("---\ntitle: Benchmarks\nweight: 8\ndashboard: overview\n---\n\n# Benchmarks y métricas\n\nÚltima ejecución: **%s** · Perfil: **%s** · Commit: **%s**.\n\nEntorno: **%s**, %d GB, **%s/%s**, Go **%s**.\n\nEstado global: **%s**.\n\nÁrbol de trabajo: **%s**. Las ejecuciones con cambios locales no se usan como baseline de release.\n\n| Tests ejecutados | Pasados | Fallidos | Omitidos | Cobertura | Estado |\n|---:|---:|---:|---:|---:|---|\n| %d | %d | %d | %d | %.1f%% | %s |\n\n- [Metrics](metrics/)\n- [Test inventory](test-inventory/)\n- [Benchmark catalog](benchmark-catalog/)\n",
		summary.Manifest.GeneratedAt.Format(time.RFC3339), summary.Manifest.Profile, summary.Manifest.Commit,
		summary.Manifest.Environment.MachineModel, summary.Manifest.Environment.MemoryGB,
		summary.Manifest.Environment.OS, summary.Manifest.Environment.Arch, summary.Manifest.Environment.Go,
		status, sourceState(summary.Manifest.SourceDirty), summary.Tests.Executed, summary.Tests.Passed, summary.Tests.Failed, summary.Tests.Skipped,
		summary.CoveragePercent, status)
	if err := benchmark.WriteText(filepath.Join(dir, "_index.md"), index); err != nil {
		return err
	}
	var metrics strings.Builder
	metrics.WriteString("---\ntitle: Metrics\nweight: 1\ndashboard: metrics\n---\n\n# Metrics de tests\n\n| Package | Ejecutados | Pasados | Fallidos | Omitidos | Pass rate | Cobertura | Estado |\n|---|---:|---:|---:|---:|---:|---:|---|\n")
	for _, suite := range summary.Suites {
		fmt.Fprintf(&metrics, "| %s | %d | %d | %d | %d | %.1f%% | %.1f%% | %s |\n", suite.Package, suite.Executed, suite.Passed, suite.Failed, suite.Skipped, suite.PassRate, suite.CoveragePercent, suite.Status.Indicator())
	}
	metrics.WriteString("\n## Semáforo\n\n- 🟢 GREEN: tests correctos y cobertura objetivo.\n- 🟠 ORANGE: advertencias, skips o cobertura incompleta.\n- 🔴 RED: fallos, regresiones o evidencia inválida.\n")
	if err := benchmark.WriteText(filepath.Join(dir, "metrics.md"), metrics.String()); err != nil {
		return err
	}
	var inventory strings.Builder
	inventory.WriteString("---\ntitle: Test inventory\nweight: 2\ndashboard: inventory\n---\n\n# Inventario de tests\n\n| Paquete | Descubiertos | Ejecutados | Pasados | Fallidos | Omitidos |\n|---|---:|---:|---:|---:|---:|\n")
	for _, suite := range summary.Suites {
		fmt.Fprintf(&inventory, "| %s | %d | %d | %d | %d | %d |\n", suite.Package, suite.Discovered, suite.Executed, suite.Passed, suite.Failed, suite.Skipped)
	}
	if err := benchmark.WriteText(filepath.Join(dir, "test-inventory.md"), inventory.String()); err != nil {
		return err
	}
	var catalog strings.Builder
	catalog.WriteString("---\ntitle: Benchmark catalog\nweight: 3\ndashboard: catalog\n---\n\n# Catálogo de benchmarks\n\n| Benchmark | Categoría | Muestras | ns/op | B/op | Allocs/op | Throughput/s | RSS | CPU ms | CV | Delta | Estado |\n|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, result := range summary.Benchmarks {
		fmt.Fprintf(&catalog, "| %s | %s | %d | %.1f | %.1f | %.1f | %.1f | %d | %.1f | %.1f%% | %.1f%% | %s |\n", result.Name, result.Category, result.Samples, result.NanosecondsOp, result.BytesOp, result.AllocsOp, result.Throughput, result.RSSBytes, result.CPUTimeMS, result.LatencyMS.CoefficientVar, result.DeltaPercent, result.Status.Indicator())
	}
	catalog.WriteString("\nLos resultados detallados se conservan en benchmarks/runs/latest/.\n")
	return benchmark.WriteText(filepath.Join(dir, "benchmark-catalog.md"), catalog.String())
}

func sourceState(dirty bool) string {
	if dirty {
		return "DIRTY"
	}
	return "CLEAN"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
