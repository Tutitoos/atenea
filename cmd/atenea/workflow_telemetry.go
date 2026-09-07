package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/dashboard"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func flagsBeforeArgs(args []string, values map[string]bool) []string {
	flags, positional := make([]string, 0, len(args)), make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		name := args[i]
		if cut := strings.IndexByte(name, '='); cut >= 0 {
			name = name[:cut]
		}
		if strings.HasPrefix(name, "--") {
			flags = append(flags, args[i])
			if values[name] && !strings.Contains(args[i], "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, args[i])
	}
	return append(flags, positional...)
}

func renderWorkflowStatus(out io.Writer, run workflow.Run, telemetry []workflow.PlanPointTelemetry, format string) error {
	progress := workflow.PlanProgress{Revision: run.PlanRevision, Points: run.Points}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "compact":
		tokens, duration, tokenState, durationState := telemetryTotals(telemetry)
		_, err := fmt.Fprintf(out, "%s state=%s progress=%d/%d tokens=%s duration=%s\n", run.ID, runState(run), progress.Accepted(), progress.Total(), measuredValue(tokens, tokenState), measuredDuration(duration, durationState))
		return err
	case "markdown":
		fmt.Fprintln(out, progress.Markdown(""))
		fmt.Fprintln(out, "\n| Punto | Agentes | Herramientas | Tokens | Duración | Medición |\n|---|---:|---:|---:|---:|---|")
		for _, point := range telemetry {
			tokens := point.InputTokens + point.OutputTokens + point.CacheReadTokens + point.CacheWriteTokens
			state := aggregateMeasurement(point.Measured, point.Estimated, point.Partial, point.Unknown)
			duration := point.AgentDuration + point.ToolDuration
			fmt.Fprintf(out, "| %s | %d | %d | %s | %s | M:%d E:%d P:%d U:%d |\n", point.PointID, point.AgentRuns, point.ToolUses, measuredValue(tokens, state), measuredDuration(duration, durationMeasurement(duration)), point.Measured, point.Estimated, point.Partial, point.Unknown)
		}
		return nil
	case "json":
		return json.NewEncoder(out).Encode(struct {
			Run       workflow.Run                  `json:"run"`
			Progress  workflow.PlanProgress         `json:"progress"`
			Telemetry []workflow.PlanPointTelemetry `json:"telemetry"`
		}{run, progress, telemetry})
	default:
		return contract.Fail(contract.FailureInvalidInput, "workflow status: format must be compact, markdown or json")
	}
}

func aggregateMeasurement(measured, estimated, partial, unknown int) workflow.MeasurementState {
	if measured+estimated+partial+unknown == 0 || unknown > 0 && measured+estimated+partial == 0 {
		return workflow.MeasurementUnknown
	}
	if estimated > 0 && measured+partial+unknown == 0 {
		return workflow.MeasurementEstimated
	}
	if partial > 0 || unknown > 0 || estimated > 0 {
		return workflow.MeasurementPartial
	}
	return workflow.MeasurementMeasured
}
func telemetryTotals(rows []workflow.PlanPointTelemetry) (int64, time.Duration, workflow.MeasurementState, workflow.MeasurementState) {
	var tokens int64
	var duration time.Duration
	measured, estimated, partial, unknown := 0, 0, 0, 0
	for _, p := range rows {
		tokens += p.InputTokens + p.OutputTokens + p.CacheReadTokens + p.CacheWriteTokens
		duration += p.AgentDuration + p.ToolDuration
		measured += p.Measured
		estimated += p.Estimated
		partial += p.Partial
		unknown += p.Unknown
	}
	return tokens, duration, aggregateMeasurement(measured, estimated, partial, unknown), durationMeasurement(duration)
}

func durationMeasurement(duration time.Duration) workflow.MeasurementState {
	if duration > 0 {
		return workflow.MeasurementMeasured
	}
	return workflow.MeasurementUnknown
}
func measuredValue(value int64, state workflow.MeasurementState) string {
	if state == workflow.MeasurementUnknown {
		return "unknown"
	}
	if state == workflow.MeasurementMeasured {
		return fmt.Sprint(value)
	}
	return fmt.Sprintf("%d(%s)", value, state)
}
func measuredDuration(value time.Duration, state workflow.MeasurementState) string {
	if state == workflow.MeasurementUnknown {
		return "unknown"
	}
	if state == workflow.MeasurementMeasured {
		return value.String()
	}
	return fmt.Sprintf("%s(%s)", value, state)
}

func workflowExport(args []string, out io.Writer) error {
	format := "markdown"
	tracePath := ""
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			i++
			if i >= len(args) {
				return contract.Fail(contract.FailureInvalidInput, "workflow export: --format requires a value")
			}
			format = args[i]
		case "--traces":
			i++
			if i >= len(args) {
				return contract.Fail(contract.FailureInvalidInput, "workflow export: --traces requires a value")
			}
			tracePath = args[i]
		default:
			positionals = append(positionals, args[i])
		}
	}
	if len(positionals) != 1 || (format != "markdown" && format != "json") {
		return contract.Fail(contract.FailureInvalidInput, "workflow export takes one id and --format markdown|json")
	}
	forward := []string{positionals[0], "--format", format}
	if tracePath != "" {
		forward = append(forward, "--traces", tracePath)
	}
	return workflowShow(forward, out)
}

func workflowCompare(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow compare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database")
	if err := flags.Parse(flagsBeforeArgs(args, map[string]bool{"--traces": true})); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return contract.Fail(contract.FailureInvalidInput, "workflow compare takes two workflow ids")
	}
	store, err := workflow.Open(context.Background(), *tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	totals := func(id string) (int64, time.Duration, workflow.MeasurementState, workflow.MeasurementState, error) {
		if _, err := store.Load(context.Background(), id); err != nil {
			return 0, 0, workflow.MeasurementUnknown, workflow.MeasurementUnknown, err
		}
		rows, err := store.PointTelemetry(context.Background(), id)
		if err != nil {
			return 0, 0, workflow.MeasurementUnknown, workflow.MeasurementUnknown, err
		}
		tokens, duration, tokenState, durationState := telemetryTotals(rows)
		return tokens, duration, tokenState, durationState, nil
	}
	aTok, aDur, aState, aDurationState, err := totals(flags.Arg(0))
	if err != nil {
		return err
	}
	bTok, bDur, bState, bDurationState, err := totals(flags.Arg(1))
	if err != nil {
		return err
	}
	deltaTokens, deltaDuration := "unknown", "unknown"
	if aState == workflow.MeasurementMeasured && bState == workflow.MeasurementMeasured {
		deltaTokens = fmt.Sprint(bTok - aTok)
	}
	if aDurationState == workflow.MeasurementMeasured && bDurationState == workflow.MeasurementMeasured {
		deltaDuration = (bDur - aDur).String()
	}
	_, err = fmt.Fprintf(out, "workflow_a=%s tokens=%s duration=%s\nworkflow_b=%s tokens=%s duration=%s\ndelta_tokens=%s delta_duration=%s\n", flags.Arg(0), measuredValue(aTok, aState), measuredDuration(aDur, aDurationState), flags.Arg(1), measuredValue(bTok, bState), measuredDuration(bDur, bDurationState), deltaTokens, deltaDuration)
	return err
}

func workflowPanel(settingsPath string, args []string, out io.Writer) error {
	open := false
	var id string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--open" {
			open = true
		} else if id == "" {
			id = arg
		} else {
			return contract.Fail(contract.FailureInvalidInput, "workflow panel takes one workflow id")
		}
	}
	if id == "" {
		return contract.Fail(contract.FailureInvalidInput, "workflow panel takes one workflow id")
	}
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	if !cfg.Dashboard.Enabled {
		return contract.Fail(contract.FailureUnavailable, "workflow panel requires [dashboard] enabled = true")
	}
	store, err := workflow.Open(context.Background(), "")
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Load(context.Background(), id); err != nil {
		return err
	}
	address := cfg.Dashboard.Listen
	if address == "" {
		address = "127.0.0.1:8788"
	}
	raw := "http://" + address + "/workflows/" + url.PathEscape(id)
	fmt.Fprintln(out, raw)
	if open {
		return dashboard.DefaultLauncher().Open(raw)
	}
	return nil
}

func cmdAgents(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "scorecard" {
		return contract.Fail(contract.FailureInvalidInput, "agents needs subcommand scorecard")
	}
	tracePath := ""
	if len(args) == 3 && args[1] == "--traces" {
		tracePath = args[2]
	} else if len(args) != 1 {
		return contract.Fail(contract.FailureInvalidInput, "agents scorecard accepts only --traces PATH")
	}
	store, err := workflow.Open(context.Background(), tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	rows, err := store.AgentScorecard(context.Background())
	if err != nil {
		return err
	}
	for _, row := range rows {
		fmt.Fprintf(out, "%s runs=%d tokens=%d duration=%s measured=%d estimated=%d partial=%d unknown=%d\n", row.Agent, row.Runs, row.Tokens, row.Duration, row.Measured, row.Estimated, row.Partial, row.Unknown)
	}
	return nil
}
