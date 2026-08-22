// Package benchmark provides the shared evidence format for Atenea's
// reproducible tests and performance runs. It is deliberately separate from
// internal/metrics: that package stores provider observations, while this
// package stores an auditable experiment and its environment.
package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

type Status string

const (
	Green  Status = "GREEN"
	Orange Status = "ORANGE"
	Red    Status = "RED"
)

func (s Status) Indicator() string {
	switch s {
	case Green:
		return "🟢 GREEN"
	case Orange:
		return "🟠 ORANGE"
	default:
		return "🔴 RED"
	}
}

type Environment struct {
	MachineModel string `json:"machine_model"`
	Chip         string `json:"chip"`
	MemoryGB     int    `json:"memory_gb"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Kernel       string `json:"kernel,omitempty"`
	Go           string `json:"go"`
	LogicalCPUs  int    `json:"logical_cpus"`
}

type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	RunID         string      `json:"run_id"`
	Profile       string      `json:"profile"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Command       string      `json:"command"`
	Commit        string      `json:"commit"`
	SourceDirty   bool        `json:"source_dirty"`
	Environment   Environment `json:"environment"`
}

type TestSuite struct {
	Package         string  `json:"package"`
	Discovered      int     `json:"discovered"`
	Executed        int     `json:"executed"`
	Passed          int     `json:"passed"`
	Failed          int     `json:"failed"`
	Skipped         int     `json:"skipped"`
	DurationMS      float64 `json:"duration_ms"`
	CoveragePercent float64 `json:"coverage_percent"`
	PassRate        float64 `json:"pass_rate"`
	Status          Status  `json:"status"`
}

type Distribution struct {
	Samples        int     `json:"samples"`
	Min            float64 `json:"min"`
	Median         float64 `json:"median"`
	P50            float64 `json:"p50"`
	P90            float64 `json:"p90"`
	P95            float64 `json:"p95"`
	P99            float64 `json:"p99"`
	Max            float64 `json:"max"`
	Mean           float64 `json:"mean"`
	StdDev         float64 `json:"stddev"`
	CoefficientVar float64 `json:"coefficient_of_variation"`
}

type BenchmarkResult struct {
	Name          string       `json:"name"`
	Category      string       `json:"category"`
	Package       string       `json:"package"`
	Command       string       `json:"command"`
	Samples       int          `json:"samples"`
	NanosecondsOp float64      `json:"ns_per_op,omitempty"`
	BytesOp       float64      `json:"bytes_per_op,omitempty"`
	AllocsOp      float64      `json:"allocs_per_op,omitempty"`
	Throughput    float64      `json:"throughput_per_second,omitempty"`
	LatencyMS     Distribution `json:"latency_ms,omitempty"`
	RSSBytes      int64        `json:"rss_bytes,omitempty"`
	CPUTimeMS     float64      `json:"cpu_time_ms,omitempty"`
	DeltaPercent  float64      `json:"delta_percent,omitempty"`
	Baseline      string       `json:"baseline,omitempty"`
	Profiles      []string     `json:"profiles,omitempty"`
	Valid         bool         `json:"valid"`
	Limitations   []string     `json:"limitations,omitempty"`
	Status        Status       `json:"status"`
}

type TestTotals struct {
	Discovered int     `json:"discovered"`
	Executed   int     `json:"executed"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	Skipped    int     `json:"skipped"`
	PassRate   float64 `json:"pass_rate"`
}

type Summary struct {
	Manifest        Manifest          `json:"manifest"`
	Tests           TestTotals        `json:"tests"`
	CoveragePercent float64           `json:"coverage_percent"`
	CoverageFloor   float64           `json:"coverage_floor"`
	CoverageTarget  float64           `json:"coverage_target"`
	Suites          []TestSuite       `json:"suites"`
	Benchmarks      []BenchmarkResult `json:"benchmarks"`
	OverallStatus   Status            `json:"overall_status"`
	Warnings        []string          `json:"warnings,omitempty"`
}

func CollectEnvironment(ctx context.Context) Environment {
	env := Environment{OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version(), LogicalCPUs: runtime.NumCPU()}
	if runtime.GOOS != "darwin" {
		return env
	}
	text := commandOutput(ctx, "system_profiler", "SPHardwareDataType")
	model := valueAfter(text, "Model Name:")
	chip := valueAfter(text, "Chip:")
	memory := valueAfter(text, "Memory:")
	if model != "" && chip != "" {
		env.MachineModel = model + " " + strings.TrimPrefix(chip, "Apple ")
	} else {
		env.MachineModel = model
	}
	env.Chip = chip
	if fields := strings.Fields(memory); len(fields) > 0 {
		env.MemoryGB, _ = strconv.Atoi(fields[0])
	}
	env.Kernel = commandOutput(ctx, "uname", "-r")
	return env
}

func commandOutput(ctx context.Context, name string, args ...string) string {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func valueAfter(text, label string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), label))
		}
	}
	return ""
}

func GitState(ctx context.Context) (commit string, dirty bool) {
	commit = commandOutput(ctx, "git", "rev-parse", "HEAD")
	cmd := exec.CommandContext(ctx, "git", "diff", "--quiet")
	dirty = cmd.Run() != nil
	if !dirty {
		cmd = exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
		dirty = cmd.Run() != nil
	}
	return commit, dirty
}

func NewManifest(ctx context.Context, profile, command string) Manifest {
	commit, dirty := GitState(ctx)
	now := time.Now().UTC()
	return Manifest{
		SchemaVersion: SchemaVersion,
		RunID:         now.Format("20060102T150405Z"),
		Profile:       profile,
		GeneratedAt:   now,
		Command:       command,
		Commit:        commit,
		SourceDirty:   dirty,
		Environment:   CollectEnvironment(ctx),
	}
}

func DistributionOf(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mean := 0.0
	for _, value := range sorted {
		mean += value
	}
	mean /= float64(len(sorted))
	variance := 0.0
	for _, value := range sorted {
		delta := value - mean
		variance += delta * delta
	}
	if len(sorted) > 1 {
		variance /= float64(len(sorted) - 1)
	}
	stddev := math.Sqrt(variance)
	cv := 0.0
	if mean != 0 {
		cv = stddev / mean * 100
	}
	return Distribution{
		Samples: len(sorted), Min: sorted[0], Median: percentile(sorted, 0.50),
		P50: percentile(sorted, 0.50), P90: percentile(sorted, 0.90),
		P95: percentile(sorted, 0.95), P99: percentile(sorted, 0.99),
		Max: sorted[len(sorted)-1], Mean: mean, StdDev: stddev, CoefficientVar: cv,
	}
}

func percentile(sorted []float64, fraction float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := fraction * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

func PassRate(passed, executed int) float64 {
	if executed == 0 {
		return 0
	}
	return float64(passed) / float64(executed) * 100
}

func SuiteStatus(s TestSuite, floor, target float64) Status {
	if s.Failed > 0 || s.Executed == 0 || s.CoveragePercent < floor {
		return Red
	}
	if s.CoveragePercent < target || s.Skipped > 0 {
		return Orange
	}
	return Green
}

func OverallStatus(summary Summary) Status {
	status := Green
	for _, suite := range summary.Suites {
		if suite.Status == Red {
			return Red
		}
		if suite.Status == Orange {
			status = Orange
		}
	}
	for _, result := range summary.Benchmarks {
		if result.Status == Red {
			return Red
		}
		if result.Status == Orange {
			status = Orange
		}
	}
	if summary.Tests.Failed > 0 || summary.CoveragePercent < summary.CoverageFloor {
		return Red
	}
	if summary.CoveragePercent < summary.CoverageTarget {
		status = Orange
	}
	return status
}

func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

func WriteText(path, contents string) error {
	return atomicWrite(path, []byte(contents))
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}
