package decision

import "github.com/Tutitoos/atenea/pkg/contract"

// Explanation is a bounded, versioned projection of planning evidence. Every
// string emitted here comes from a closed vocabulary, never from request text,
// free-form reasons, diagnostics, paths, or configured catalog identifiers.
type Explanation struct {
	Version             int                   `json:"version"`
	Intent              string                `json:"intent"`
	IntentSource        string                `json:"intent_source"`
	ClassifierMode      string                `json:"classifier_mode"`
	ClassifierResult    string                `json:"classifier_result"`
	Resolution          string                `json:"resolution"`
	ResolutionCode      string                `json:"resolution_code"`
	Context             string                `json:"context"`
	Readiness           string                `json:"readiness"`
	ReadinessReasons    []string              `json:"readiness_reasons"`
	ReasonsComplete     bool                  `json:"reasons_complete"`
	Budget              string                `json:"budget"`
	Effects             []string              `json:"effects"`
	EffectsOmitted      bool                  `json:"effects_omitted"`
	Capabilities        []ExplainedCapability `json:"capabilities"`
	CapabilitiesTotal   int                   `json:"capabilities_total"`
	CapabilitiesOmitted bool                  `json:"capabilities_omitted"`
}

// ExplainedCapability records only a fixed capability code and an ordinal for
// its repository. Plan does not carry typed provider or fallback evidence.
type ExplainedCapability struct {
	RepositoryOrdinal int    `json:"repository_ordinal"`
	Capability        string `json:"capability"`
	Selection         string `json:"selection"`
	Provider          string `json:"provider"`
	Fallback          string `json:"fallback"`
}

const maxExplainedCapabilities = 12

// Explain derives planning evidence without changing the plan or performing IO.
func Explain(plan Plan) Explanation {
	out := Explanation{Version: 1, Intent: intentCode(plan.Intent), IntentSource: sourceCode(plan.IntentEvidence.Source),
		ClassifierMode: modeCode(plan.IntentEvidence.Mode), ClassifierResult: classifierCode(plan.IntentEvidence),
		Resolution: resolutionCode(plan.Resolution), ResolutionCode: abstentionCode(plan.ResolutionReason),
		Context: "unknown", Readiness: "unknown", ReadinessReasons: []string{}, Budget: "unknown", Effects: []string{},
		Capabilities: []ExplainedCapability{}, CapabilitiesTotal: len(plan.Capabilities)}
	if plan.Resolution == ResolutionResolved {
		out.ResolutionCode = "none"
		out.Context = "not_used"
		if plan.ContextUsed {
			out.Context = "used"
		}
		out.Readiness = "invalid"
		if plan.Valid {
			out.Readiness = "valid"
			out.ReasonsComplete = true
		}
		out.Budget = "insufficient"
		if plan.Budget.Sufficient {
			out.Budget = "sufficient"
		}
		if !plan.Valid {
			if !plan.Budget.Sufficient {
				out.ReadinessReasons = append(out.ReadinessReasons, "budget_insufficient")
			}
			for _, model := range plan.Models {
				if !model.Available {
					out.ReadinessReasons = append(out.ReadinessReasons, "model_unavailable")
					break
				}
			}
			if len(out.ReadinessReasons) == 0 {
				out.ReadinessReasons = append(out.ReadinessReasons, "unknown")
			}
		}
	}
	seenEffects := map[string]bool{}
	for _, effect := range plan.Effects {
		code := effectCode(effect)
		if code == "unknown" {
			out.EffectsOmitted = true
			continue
		}
		if !seenEffects[code] {
			seenEffects[code] = true
			out.Effects = append(out.Effects, code)
		}
	}
	for i, choice := range plan.Capabilities {
		if i >= maxExplainedCapabilities {
			out.CapabilitiesOmitted = true
			break
		}
		item := ExplainedCapability{RepositoryOrdinal: repositoryOrdinal(plan.Repositories, choice.Repository),
			Capability: capabilityCode(choice.ID), Selection: "unknown", Provider: "unknown", Fallback: "unknown"}
		switch {
		case choice.Unavailable:
			item.Selection = "unavailable"
			item.Provider = "none"
		case choice.Chosen != "":
			item.Selection = "selected"
			// Chosen is an implementation ID, not a typed provider. Its prefix is
			// configurable and cannot establish provider identity.
		case len(choice.Providers) > 0:
			item.Selection = "candidates_only"
			item.Provider = "not_selected"
		}
		out.Capabilities = append(out.Capabilities, item)
	}
	return out
}

func intentCode(value Kind) string {
	switch value {
	case KindUnderstand, KindSearch, KindPlan, KindChange:
		return string(value)
	default:
		return "unknown"
	}
}

func sourceCode(value string) string {
	switch value {
	case "rules", "context", "laya":
		return value
	default:
		return "unknown"
	}
}

func modeCode(value string) string {
	switch value {
	case "rules", "observe", "laya":
		return value
	default:
		return "unknown"
	}
}

func classifierCode(e IntentEvidence) string {
	if e.Source == "context" {
		return "bypassed_by_context"
	}
	if e.Source == "laya" {
		return "selected"
	}
	if e.Mode == "rules" {
		return "not_used"
	}
	if e.Mode == "observe" && e.Laya != nil {
		return "observed_only"
	}
	if e.Mode == "observe" && e.FallbackReason != "" {
		return "observation_failed"
	}
	if e.Mode == "laya" && e.Source == "rules" && e.FallbackReason != "" {
		return "fallback_to_rules"
	}
	return "unknown"
}

func resolutionCode(value ResolutionStatus) string {
	switch value {
	case ResolutionResolved, ResolutionNeedsContext:
		return string(value)
	default:
		return "unknown"
	}
}

func abstentionCode(value string) string {
	switch value {
	case ResolutionReasonMissingAcceptedPlan, ResolutionReasonMissingPlanObjective, ResolutionReasonInvalidContext,
		ResolutionReasonRepositoryMismatch, ResolutionReasonUnboundContinuation, ResolutionReasonPlanFreshnessUnverified,
		ResolutionReasonScopeMismatch, "unverified_accepted_plan":
		return value
	default:
		return "unknown"
	}
}

func effectCode(value contract.Effect) string {
	switch value {
	case contract.EffectRead, contract.EffectWrite, contract.EffectExternal, contract.EffectProcess, contract.EffectDevice:
		return value.String()
	default:
		return "unknown"
	}
}

func capabilityCode(value string) string {
	switch value {
	case "symbol.intent_search", "code.context", "symbol.search", "symbol.overview", "symbol.dependencies",
		"symbol.consumers", "code.search", "symbol.definition", "symbol.references", "symbol.impact", "symbol.source":
		return value
	default:
		return "unknown"
	}
}

func repositoryOrdinal(repositories []string, choiceRepository string) int {
	if choiceRepository == "" {
		return 0
	}
	ordinal := 0
	for i, repository := range repositories {
		if repository != choiceRepository {
			continue
		}
		if ordinal != 0 {
			return 0 // duplicate repository IDs do not identify one position
		}
		ordinal = i + 1
	}
	return ordinal
}
