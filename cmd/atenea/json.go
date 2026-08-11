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
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type jsonResult struct {
	Run     string `json:"run"`
	Task    string `json:"task"`
	Verdict string `json:"verdict"`
	Matches *int   `json:"matches,omitempty"`
	SpentMS int64  `json:"spent_ms"`
	// ElapsedMS is the wall beside the sum. A reader with only the sum cannot
	// tell a wave from a queue, and the sum is the bigger number of the two.
	ElapsedMS   int64           `json:"elapsed_ms"`
	ChargedUSD  float64         `json:"charged_usd,omitempty"`
	Phases      []jsonPhase     `json:"phases,omitempty"`
	Discoveries []jsonDiscovery `json:"discoveries,omitempty"`
	Waves       [][]string      `json:"waves,omitempty"`
	PlanError   string          `json:"plan_error,omitempty"`
	Steps       []jsonStep      `json:"steps"`
}

type jsonPhase struct {
	Name      string `json:"name"`
	Steps     int    `json:"steps"`
	SpentMS   int64  `json:"spent_ms"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type jsonDiscovery struct {
	Level string `json:"level"`
	Note  string `json:"note"`
}

type jsonStep struct {
	ID             string `json:"id"`
	Phase          string `json:"phase"`
	Capability     string `json:"capability"`
	Repository     string `json:"repository"`
	Implementation string `json:"implementation,omitempty"`
	SpentMS        int64  `json:"spent_ms"`
	// ClosedAt and SpentMS are an interval, which is how a script sees that two
	// steps of one wave overlapped. Stamped by the step itself, so a quick step
	// beside a slow one reports the moment it finished rather than the moment
	// the wave it belonged to did.
	ClosedAt     time.Time      `json:"closed_at"`
	ChargedUSD   float64        `json:"charged_usd,omitempty"`
	OverspentUSD float64        `json:"overspent_usd,omitempty"`
	Review       *jsonReview    `json:"review,omitempty"`
	Failure      string         `json:"failure,omitempty"`
	FailureKind  string         `json:"failure_kind,omitempty"`
	Raw          string         `json:"raw,omitempty"`
	Notices      []string       `json:"notices,omitempty"`
	Scope        []string       `json:"scope,omitempty"`
	Dropped      []jsonDrop     `json:"dropped,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
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
		ElapsedMS:  result.Elapsed.Milliseconds(),
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
			Name: phase.Name, Steps: phase.Steps,
			SpentMS:   phase.Spent.Duration.Milliseconds(),
			ElapsedMS: phase.Elapsed.Milliseconds(),
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
		ClosedAt:       step.ClosedAt,
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

type jsonServerProbe struct {
	ID         string `json:"id"`
	Transport  string `json:"transport"`
	Where      string `json:"where"`
	Expose     string `json:"expose"`
	Reachable  bool   `json:"reachable"`
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	Reason     string `json:"reason,omitempty"`
	TookMS     int64  `json:"took_ms"`
	PinnedPath bool   `json:"pinned_path"`
}

// jsonDetection is what `atenea detect --json` answers.
//
// An object with two keys, where this used to be a bare array of index
// reports. The array could not grow a second kind of row without every reader
// having to guess which kind it was holding, and a detection that reported
// indexes while staying silent about whether the providers behind them are
// even reachable is the shape of report this whole change exists to end. The
// break is deliberate and belongs to a 0.x CLI, not to the contract in
// pkg/contract, which is untouched.
type jsonDetection struct {
	Servers []jsonServerProbe `json:"servers"`
	Indexes []jsonIndexReport `json:"indexes"`
}

// printDetectionJSON is the machine-readable twin of the two prose sections.
func printDetectionJSON(out io.Writer, servers []core.ServerProbe, reports []core.IndexReport) {
	js := jsonDetection{
		Servers: make([]jsonServerProbe, len(servers)),
		Indexes: make([]jsonIndexReport, len(reports)),
	}
	for i, server := range servers {
		js.Servers[i] = jsonServerProbe{
			ID:         server.ID,
			Transport:  server.Transport,
			Where:      server.Where,
			Expose:     server.Expose,
			Reachable:  server.OK,
			Name:       server.Name,
			Version:    server.Version,
			Reason:     server.Reason,
			TookMS:     server.Took.Milliseconds(),
			PinnedPath: server.PinnedPath,
		}
	}
	for i, report := range reports {
		js.Indexes[i] = jsonIndexReport{
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
