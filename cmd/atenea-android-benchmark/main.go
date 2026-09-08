// Command atenea-android-benchmark records device-real Android bridge timings.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/android"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const schemaVersion = "android-bridge-benchmark/v1"

type sample struct {
	DurationMS float64 `json:"duration_ms"`
	Bytes      int     `json:"bytes,omitempty"`
	Outcome    string  `json:"outcome"`
	Error      string  `json:"error,omitempty"`
}

type operation struct {
	Name      string   `json:"name"`
	Samples   []sample `json:"samples"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	MedianMS  float64  `json:"median_ms"`
	P95MS     float64  `json:"p95_ms"`
	MedianB   int      `json:"median_bytes,omitempty"`
}

type report struct {
	SchemaVersion string      `json:"schema_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Serial        string      `json:"serial"`
	Warmup        int         `json:"warmup"`
	Repetitions   int         `json:"repetitions"`
	Host          string      `json:"host"`
	ADBVersion    string      `json:"adb_version"`
	ScrcpyVersion string      `json:"scrcpy_version"`
	Operations    []operation `json:"operations"`
	Limitations   []string    `json:"limitations"`
}

func main() {
	serial := flag.String("serial", "", "explicitly allowed Android serial")
	warmup := flag.Int("warmup", 5, "unrecorded warm-up repetitions per operation")
	repetitions := flag.Int("repetitions", 30, "recorded repetitions per operation")
	output := flag.String("output", "", "directory for benchmark JSON and Markdown")
	flag.Parse()
	if strings.TrimSpace(*serial) == "" || *warmup < 0 || *repetitions < 1 || strings.TrimSpace(*output) == "" {
		fatal(errors.New("--serial, a non-negative --warmup, positive --repetitions and --output are required"))
	}
	runner, err := android.New(android.Options{AllowedSerials: []string{*serial}})
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	if _, err := run(ctx, runner, android.CapabilityDevices, android.ImplementationDevices, nil); err != nil {
		fatal(fmt.Errorf("list authorized device: %w", err))
	}
	ops := []struct {
		name string
		run  func(context.Context, *android.Runner, string) (int, string, error)
	}{
		{name: "screenshot", run: measureScreenshot},
		{name: "inspect", run: measureInspect},
		{name: "key_home_sent", run: measureHome},
		{name: "scrcpy_lifecycle", run: measureMirror},
	}
	out := report{
		SchemaVersion: schemaVersion, GeneratedAt: time.Now().UTC(), Serial: *serial,
		Warmup: *warmup, Repetitions: *repetitions, Host: runtime.GOOS + "/" + runtime.GOARCH,
		ADBVersion: commandVersion("adb", "version"), ScrcpyVersion: commandVersion("scrcpy", "--version"),
		Limitations: []string{
			"key_home_sent records that Android accepted the command; it does not prove a task-specific UI outcome.",
			"No application form data is written by this benchmark.",
			"Compare runs only with the same serial, Android state and benchmark settings.",
		},
	}
	for _, spec := range ops {
		for i := 0; i < *warmup; i++ {
			_, _, _ = spec.run(ctx, runner, *serial)
		}
		op := operation{Name: spec.name, Samples: make([]sample, 0, *repetitions)}
		for i := 0; i < *repetitions; i++ {
			started := time.Now()
			bytes, outcome, err := spec.run(ctx, runner, *serial)
			s := sample{DurationMS: float64(time.Since(started).Microseconds()) / 1000, Bytes: bytes, Outcome: outcome}
			if err != nil {
				s.Outcome, s.Error = "failed", err.Error()
				op.Failed++
			} else {
				op.Succeeded++
			}
			op.Samples = append(op.Samples, s)
		}
		summarize(&op)
		out.Operations = append(out.Operations, op)
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fatal(err)
	}
	write(filepath.Join(*output, "android-bridge.json"), out)
	writeMarkdown(filepath.Join(*output, "android-bridge.md"), out)
	fmt.Printf("android benchmark serial=%s operations=%d output=%s\n", *serial, len(out.Operations), *output)
}

func measureScreenshot(ctx context.Context, runner *android.Runner, serial string) (int, string, error) {
	out, err := run(ctx, runner, android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": serial})
	if err != nil {
		return 0, "failed", err
	}
	return out.Result["bytes"].(int), "observed", nil
}

func measureInspect(ctx context.Context, runner *android.Runner, serial string) (int, string, error) {
	_, err := run(ctx, runner, android.CapabilityInspect, android.ImplementationInspect, map[string]any{"serial": serial})
	return 0, "observed", err
}

func measureHome(ctx context.Context, runner *android.Runner, serial string) (int, string, error) {
	shot, err := run(ctx, runner, android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": serial})
	if err != nil {
		return 0, "failed", err
	}
	_, err = run(ctx, runner, android.CapabilityKey, android.ImplementationKey, map[string]any{"serial": serial, "frame_id": shot.Result["frame_id"], "key": "home"})
	return 0, "sent", err
}

func measureMirror(ctx context.Context, runner *android.Runner, serial string) (int, string, error) {
	if _, err := run(ctx, runner, android.CapabilityMirror, android.ImplementationMirror, map[string]any{"serial": serial}); err != nil {
		return 0, "failed", err
	}
	_, err := run(ctx, runner, android.CapabilityUnmirror, android.ImplementationUnmirror, map[string]any{"serial": serial})
	return 0, "lifecycle_sent", err
}

func run(ctx context.Context, runner *android.Runner, capability, implementation string, payload map[string]any) (contract.Outcome, error) {
	return runner.Run(ctx, contract.RunRequest{Capability: contract.Capability{ID: capability}, Implementation: contract.Implementation{ID: implementation, Capability: capability}, Payload: payload})
}

func summarize(op *operation) {
	latencies, sizes := make([]float64, 0, len(op.Samples)), make([]int, 0, len(op.Samples))
	for _, s := range op.Samples {
		if s.Outcome != "failed" {
			latencies = append(latencies, s.DurationMS)
			if s.Bytes > 0 {
				sizes = append(sizes, s.Bytes)
			}
		}
	}
	sort.Float64s(latencies)
	sort.Ints(sizes)
	if len(latencies) > 0 {
		op.MedianMS = percentile(latencies, .50)
		op.P95MS = percentile(latencies, .95)
	}
	if len(sizes) > 0 {
		op.MedianB = sizes[(len(sizes)-1)/2]
	}
}

func percentile(values []float64, p float64) float64 { return values[int(float64(len(values)-1)*p)] }

func commandVersion(binary string, args ...string) string {
	out, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}

func write(path string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		fatal(err)
	}
}

func writeMarkdown(path string, value report) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Android bridge benchmark\n\nSerial: `%s` · warmup: %d · repetitions: %d\n\n| Operation | Success | Median | P95 | Median bytes |\n|---|---:|---:|---:|---:|\n", value.Serial, value.Warmup, value.Repetitions)
	for _, op := range value.Operations {
		fmt.Fprintf(&b, "| %s | %d/%d | %.2f ms | %.2f ms | %d |\n", op.Name, op.Succeeded, len(op.Samples), op.MedianMS, op.P95MS, op.MedianB)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "atenea-android-benchmark:", err); os.Exit(1) }
