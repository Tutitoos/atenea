package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Workflow is how a graph of agent steps is scheduled: one ceiling per lane.
//
// The lanes exist so that auditing cannot be crowded out by the work it
// audits. One shared ceiling would fill every slot with agents at exactly the
// moment the machine is busiest, leaving the reviewers queued behind them and
// the answers piling up unjudged -- see [Pool].
type Workflow struct {
	// Profile is the versioned execution profile persisted with each workflow.
	// It is descriptive and never silently changes an existing run.
	Profile string
	// Profiles is the validated catalog from which Profile is selected. It is
	// copied into a workflow policy at creation time; changing this catalog
	// never widens an existing run.
	Profiles []WorkflowProfile
	// MaxBudgetUSD is the optional workflow-level ceiling. Zero preserves the
	// historical absence of a workflow budget ceiling.
	MaxBudgetUSD float64
	// MaxDuration bounds total active execution time for a workflow. Zero keeps
	// the historical unlimited behavior.
	MaxDuration time.Duration
	// MaxRetries is the shared retry ceiling for a workflow. Zero means no
	// automatic retries; explicit redo remains a separate approval.
	MaxRetries int
	// MaxParallelAgent caps how many steps in the agent lane run at once.
	// Zero means no ceiling, the same reading as orchestrator.max_parallel:
	// the real limit is the machine, and the machine differs everywhere.
	MaxParallelAgent int
	// MaxParallelReview caps the review lane.
	//
	// Sized the same as the agent lane by default. Nobody has measured what
	// reviewing costs against what it audits on this machine, and a default
	// that pretended otherwise would be a number with a story attached and no
	// reading behind it. What matters here is that the two are separate, not
	// that one is smaller.
	MaxParallelReview int
}

// WorkflowProfile is a selectable, immutable execution profile. Digest is a
// deterministic identity of the limits and is persisted with each workflow.
type WorkflowProfile struct {
	Name              string
	Version           string
	Digest            string
	MaxBudgetUSD      float64
	MaxDuration       time.Duration
	MaxRetries        int
	MaxParallelAgent  int
	MaxParallelReview int
}

var workflowProfileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (p WorkflowProfile) valid() error {
	if !workflowProfileName.MatchString(p.Name) {
		return contract.Fail(contract.FailureInvalidInput, "workflow profile name %q is invalid", p.Name)
	}
	if strings.TrimSpace(p.Version) == "" || p.MaxBudgetUSD < 0 || math.IsNaN(p.MaxBudgetUSD) || math.IsInf(p.MaxBudgetUSD, 0) ||
		p.MaxDuration < 0 || p.MaxRetries < 0 || p.MaxParallelAgent < 0 || p.MaxParallelReview < 0 {
		return contract.Fail(contract.FailureInvalidInput, "workflow profile %q has invalid limits", p.Name)
	}
	return nil
}

// ComputeWorkflowProfileDigest gives a stable, version-independent identity
// for the materialized limits. The version remains visible as operator input;
// the digest detects a changed definition even when its name is reused.
func ComputeWorkflowProfileDigest(p WorkflowProfile) string {
	canonical := struct {
		Name              string        `json:"name"`
		Version           string        `json:"version"`
		MaxBudgetUSD      float64       `json:"max_budget_usd"`
		MaxDuration       time.Duration `json:"max_duration_ns"`
		MaxRetries        int           `json:"max_retries"`
		MaxParallelAgent  int           `json:"max_parallel_agent"`
		MaxParallelReview int           `json:"max_parallel_review"`
	}{p.Name, p.Version, p.MaxBudgetUSD, p.MaxDuration, p.MaxRetries, p.MaxParallelAgent, p.MaxParallelReview}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ResolveWorkflowProfile returns the selected catalog entry. An empty name
// selects the first/default entry, while an explicit unknown name is rejected.
func ResolveWorkflowProfile(profiles []WorkflowProfile, name string) (WorkflowProfile, error) {
	name = strings.TrimSpace(name)
	if len(profiles) == 0 {
		return WorkflowProfile{}, contract.Fail(contract.FailureInvalidInput, "workflow profile catalog is empty")
	}
	for _, profile := range profiles {
		if profile.Name == name || (name == "" && profile.Name == profiles[0].Name) {
			return profile, nil
		}
	}
	return WorkflowProfile{}, contract.Fail(contract.FailureInvalidInput, "workflow profile %q is not defined", name)
}

// Cap is the ceiling for one lane, or zero for no ceiling.
func (w Workflow) Cap(pool Pool) int {
	if pool == PoolReview {
		return w.MaxParallelReview
	}
	return w.MaxParallelAgent
}

// The lane defaults. Four is the orchestrator's ceiling, for the same reason:
// it keeps a laptop responsive, and it is a fixed number rather than one
// derived from the measurement base.
const (
	defaultMaxParallelAgent  = 4
	defaultMaxParallelReview = 4
)

type fileWorkflow struct {
	Profile           string `toml:"profile"`
	MaxDuration       string `toml:"max_duration"`
	MaxRetries        *int   `toml:"max_retries"`
	MaxParallelAgent  *int   `toml:"max_parallel_agent"`
	MaxParallelReview *int   `toml:"max_parallel_review"`
}

type fileWorkflowProfile struct {
	Name              string   `toml:"name"`
	Version           string   `toml:"version"`
	MaxBudgetUSD      *float64 `toml:"max_budget_usd"`
	MaxDuration       string   `toml:"max_duration"`
	MaxRetries        *int     `toml:"max_retries"`
	MaxParallelAgent  *int     `toml:"max_parallel_agent"`
	MaxParallelReview *int     `toml:"max_parallel_review"`
}

func (w fileWorkflow) build(source string) (Workflow, error) {
	out := Workflow{
		Profile:           "workflow-v1",
		MaxParallelAgent:  defaultMaxParallelAgent,
		MaxParallelReview: defaultMaxParallelReview,
	}
	if w.Profile != "" {
		out.Profile = w.Profile
	}
	if w.MaxDuration != "" {
		duration, err := time.ParseDuration(w.MaxDuration)
		if err != nil || duration < 0 {
			return Workflow{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: workflow.max_duration must be a non-negative duration, got %q",
				source, w.MaxDuration)
		}
		out.MaxDuration = duration
	}
	if w.MaxRetries != nil {
		if *w.MaxRetries < 0 || *w.MaxRetries > 100 {
			return Workflow{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: workflow.max_retries must be between 0 and 100, got %d",
				source, *w.MaxRetries)
		}
		out.MaxRetries = *w.MaxRetries
	}
	lanes := []struct {
		key   string
		value *int
		out   *int
	}{
		{"max_parallel_agent", w.MaxParallelAgent, &out.MaxParallelAgent},
		{"max_parallel_review", w.MaxParallelReview, &out.MaxParallelReview},
	}
	for _, lane := range lanes {
		if lane.value == nil {
			continue
		}
		if *lane.value < 0 || *lane.value > maxMaxParallel {
			return Workflow{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: workflow.%s must be between 0 and %d, got %d",
				source, lane.key, maxMaxParallel, *lane.value)
		}
		*lane.out = *lane.value
	}
	profile := WorkflowProfile{Name: out.Profile, Version: out.Profile,
		MaxBudgetUSD: out.MaxBudgetUSD, MaxDuration: out.MaxDuration,
		MaxRetries: out.MaxRetries, MaxParallelAgent: out.MaxParallelAgent,
		MaxParallelReview: out.MaxParallelReview}
	if err := profile.valid(); err != nil {
		return Workflow{}, err
	}
	profile.Digest = ComputeWorkflowProfileDigest(profile)
	out.Profiles = []WorkflowProfile{profile}
	return out, nil
}

func buildWorkflowProfiles(source string, base Workflow, raws []fileWorkflowProfile) (Workflow, error) {
	if len(raws) == 0 {
		return base, nil
	}
	profiles := make([]WorkflowProfile, 0, len(raws))
	seen := make(map[string]bool, len(raws))
	for _, raw := range raws {
		p := WorkflowProfile{Name: strings.TrimSpace(raw.Name), Version: strings.TrimSpace(raw.Version),
			MaxBudgetUSD: base.MaxBudgetUSD, MaxDuration: base.MaxDuration,
			MaxRetries: base.MaxRetries, MaxParallelAgent: base.MaxParallelAgent,
			MaxParallelReview: base.MaxParallelReview}
		if p.Version == "" {
			p.Version = p.Name
		}
		if raw.MaxBudgetUSD != nil {
			p.MaxBudgetUSD = *raw.MaxBudgetUSD
		}
		if raw.MaxDuration != "" {
			duration, err := time.ParseDuration(raw.MaxDuration)
			if err != nil || duration < 0 {
				return Workflow{}, contract.Fail(contract.FailureInvalidInput, "settings %s: workflow profile %q max_duration is invalid", source, p.Name)
			}
			p.MaxDuration = duration
		}
		if raw.MaxRetries != nil {
			p.MaxRetries = *raw.MaxRetries
		}
		if raw.MaxParallelAgent != nil {
			p.MaxParallelAgent = *raw.MaxParallelAgent
		}
		if raw.MaxParallelReview != nil {
			p.MaxParallelReview = *raw.MaxParallelReview
		}
		if seen[p.Name] {
			return Workflow{}, contract.Fail(contract.FailureInvalidInput, "settings %s: workflow profile %q is duplicated", source, p.Name)
		}
		seen[p.Name] = true
		if err := p.valid(); err != nil {
			return Workflow{}, err
		}
		p.Digest = ComputeWorkflowProfileDigest(p)
		profiles = append(profiles, p)
	}
	if _, err := ResolveWorkflowProfile(profiles, base.Profile); err != nil {
		return Workflow{}, contract.Fail(contract.FailureInvalidInput, "settings %s: %v", source, err)
	}
	base.Profiles = profiles
	return base, nil
}
