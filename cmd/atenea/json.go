// The --json renderer.
//
// printResult and printAnswer are for an eye: columns, indentation, a line
// omitted because a human already knows what zero would have meant. A script
// needs the same facts without any of that judgment, so this is a second,
// parallel renderer rather than a mode bolted onto the first -- the two would
// only fight over what a blank line means.
//
// The shapes below are a projection of orchestrator.Result, not the domain
// type reused with tags. Result carries internals that are Atenea's business,
// not a caller's: a raw time.Duration, the funnel's own Implementation. What
// crosses the wire is exactly what printResult already shows, restructured,
// and it is always complete -- --trace thins the screen for an eye that would
// otherwise have to scan past it; a script reading JSON pays no such cost and
// gets the whole receipt every time.
package main

import (
	"encoding/json"
	"io"
	"slices"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type jsonResult struct {
	Run         string          `json:"run"`
	Task        string          `json:"task"`
	Verdict     string          `json:"verdict"`
	Matches     *int            `json:"matches,omitempty"`
	SpentMS     int64           `json:"spent_ms"`
	ChargedUSD  float64         `json:"charged_usd,omitempty"`
	Phases      []jsonPhase     `json:"phases,omitempty"`
	Discoveries []jsonDiscovery `json:"discoveries,omitempty"`
	Waves       [][]string      `json:"waves,omitempty"`
	PlanError   string          `json:"plan_error,omitempty"`
	Steps       []jsonStep      `json:"steps"`
}

type jsonPhase struct {
	Name    string `json:"name"`
	Steps   int    `json:"steps"`
	SpentMS int64  `json:"spent_ms"`
}

type jsonDiscovery struct {
	Level string `json:"level"`
	Note  string `json:"note"`
}

type jsonStep struct {
	ID             string         `json:"id"`
	Phase          string         `json:"phase"`
	Capability     string         `json:"capability"`
	Repository     string         `json:"repository"`
	Implementation string         `json:"implementation,omitempty"`
	SpentMS        int64          `json:"spent_ms"`
	ChargedUSD     float64        `json:"charged_usd,omitempty"`
	OverspentUSD   float64        `json:"overspent_usd,omitempty"`
	Review         *jsonReview    `json:"review,omitempty"`
	Failure        string         `json:"failure,omitempty"`
	FailureKind    string         `json:"failure_kind,omitempty"`
	Raw            string         `json:"raw,omitempty"`
	Notices        []string       `json:"notices,omitempty"`
	Scope          []string       `json:"scope,omitempty"`
	Dropped        []jsonDrop     `json:"dropped,omitempty"`
	Result         map[string]any `json:"result,omitempty"`
}

// jsonReview is nil on a canceled step. There was no review to report --
// printing zero-value verdicts would dress an opinion nobody holds as a
// finding, the same reason printResult skips the line on the screen.
type jsonReview struct {
	Child     string `json:"child"`
	Parent    string `json:"parent"`
	Reason    string `json:"reason,omitempty"`
	Disagreed bool   `json:"disagreed,omitempty"`
	Reply     string `json:"reply,omitempty"`
}

type jsonDrop struct {
	Implementation string `json:"implementation"`
	Reason         string `json:"reason"`
	Raw            string `json:"raw,omitempty"`
}

// printResultJSON is the machine-readable twin of printResult and
// printAnswer. It ignores --trace: there is no eye to spare here, so the
// answer, the plan and every step are always on the wire.
func printResultJSON(out io.Writer, result *orchestrator.Result) {
	js := jsonResult{
		Run:        result.RunID,
		Task:       result.Task,
		Verdict:    result.Verdict.String(),
		SpentMS:    result.Spent.Duration.Milliseconds(),
		ChargedUSD: result.SpentUSD,
		Steps:      make([]jsonStep, 0, len(result.Steps)),
	}
	// Matches are counted out of the split-up commission; see printResult for
	// why a direct ask leaves this out rather than printing a zero nobody
	// measured.
	if slices.ContainsFunc(result.Phases, func(p orchestrator.Phase) bool {
		return p.Name == orchestrator.PhaseWork
	}) {
		matches := result.Matches
		js.Matches = &matches
	}
	for _, phase := range result.Phases {
		js.Phases = append(js.Phases, jsonPhase{
			Name: phase.Name, Steps: phase.Steps, SpentMS: phase.Spent.Duration.Milliseconds(),
		})
	}
	for _, found := range result.Discoveries {
		js.Discoveries = append(js.Discoveries, jsonDiscovery{Level: found.Level.String(), Note: found.Note})
	}
	if waves, err := result.Plan.Layers(); err != nil {
		js.PlanError = err.Error()
	} else {
		for _, wave := range waves {
			names := make([]string, 0, len(wave))
			for _, step := range wave {
				names = append(names, step.ID)
			}
			js.Waves = append(js.Waves, names)
		}
	}
	for _, step := range result.Steps {
		js.Steps = append(js.Steps, jsonStepOf(step))
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(js)
}

func jsonStepOf(step orchestrator.StepResult) jsonStep {
	js := jsonStep{
		ID:             step.Step.ID,
		Phase:          step.Phase,
		Capability:     step.Step.Capability,
		Repository:     step.Step.Repository,
		Implementation: step.Decision.Chosen.ID,
		SpentMS:        step.Spent.Duration.Milliseconds(),
		ChargedUSD:     step.Outcome.SpentUSD,
		OverspentUSD:   orchestrator.Overspend(step),
		Failure:        step.Failure,
		Raw:            step.Raw,
		Notices:        step.Outcome.Notices,
		Scope:          scopeOf(step.Step.Payload),
		Result:         step.Outcome.Result,
	}
	if step.FailureKind != contract.FailureUnspecified {
		js.FailureKind = step.FailureKind.String()
	}
	if step.FailureKind != contract.FailureCanceled {
		js.Review = &jsonReview{
			Child:     step.Review.Child.String(),
			Parent:    step.Review.Parent.String(),
			Reason:    step.Review.Reason,
			Disagreed: step.Review.Disagreed,
			Reply:     step.Review.Reply,
		}
	}
	for _, stage := range step.Decision.Stages {
		for _, dropped := range stage.Dropped {
			js.Dropped = append(js.Dropped, jsonDrop{
				Implementation: dropped.Implementation,
				Reason:         dropped.Reason,
				Raw:            dropped.Raw,
			})
		}
	}
	return js
}

type jsonIndexReport struct {
	Repository string `json:"repository"`
	Provider   string `json:"provider"`
	Ready      bool   `json:"ready"`
	Hint       string `json:"hint,omitempty"`
	Err        string `json:"error,omitempty"`
}

// printIndexReportsJSON is the machine-readable twin of printIndexReports.
func printIndexReportsJSON(out io.Writer, reports []core.IndexReport) {
	js := make([]jsonIndexReport, len(reports))
	for i, report := range reports {
		js[i] = jsonIndexReport{
			Repository: report.Repository,
			Provider:   report.Provider,
			Ready:      report.Ready,
			Hint:       report.Hint,
			Err:        report.Err,
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(js)
}
