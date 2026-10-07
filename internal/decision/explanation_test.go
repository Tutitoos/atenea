package decision

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestExplainResolvedAndAbstained(t *testing.T) {
	resolved := Plan{Resolution: ResolutionResolved, Intent: KindPlan,
		IntentEvidence: IntentEvidence{Mode: "observe", Source: "rules", Laya: &IntentClassification{Intent: KindChange}},
		ContextUsed:    true, Valid: true, Budget: BudgetSummary{Sufficient: true},
		Effects:      []contract.Effect{contract.EffectRead, contract.EffectWrite},
		Repositories: []string{"work"},
		Capabilities: []CapabilityChoice{{ID: "code.search", Repository: "work", Chosen: "codex.search"}, {ID: "symbol.references", Repository: "work", Unavailable: true}}}
	got := Explain(resolved)
	if got.Version != 1 || got.Intent != "plan" || got.IntentSource != "rules" || got.ClassifierResult != "observed_only" ||
		got.Context != "used" || got.Readiness != "valid" || !got.ReasonsComplete || got.Budget != "sufficient" || len(got.Capabilities) != 2 ||
		got.Capabilities[0].Provider != "unknown" || got.Capabilities[0].RepositoryOrdinal != 1 || got.Capabilities[1].Selection != "unavailable" {
		t.Fatalf("resolved explanation = %+v", got)
	}
	abstained := Explain(Plan{Resolution: ResolutionNeedsContext, ResolutionReason: ResolutionReasonMissingAcceptedPlan})
	if abstained.Resolution != "needs_context" || abstained.ResolutionCode != "missing_accepted_plan" ||
		abstained.Context != "unknown" || abstained.Readiness != "unknown" || abstained.Budget != "unknown" {
		t.Fatalf("abstained explanation = %+v", abstained)
	}
	laya := Explain(Plan{Resolution: ResolutionResolved, Intent: KindChange, IntentEvidence: IntentEvidence{Mode: "laya", Source: "laya"}, Budget: BudgetSummary{Sufficient: false}})
	if laya.ClassifierResult != "selected" || laya.Budget != "insufficient" || laya.Readiness != "invalid" ||
		laya.ReasonsComplete || len(laya.ReadinessReasons) != 1 || laya.ReadinessReasons[0] != "budget_insufficient" {
		t.Fatalf("laya explanation = %+v", laya)
	}
	fallback := Explain(Plan{IntentEvidence: IntentEvidence{Mode: "laya", Source: "rules", FallbackReason: "service request failed"}})
	if fallback.ClassifierResult != "fallback_to_rules" {
		t.Fatalf("fallback explanation = %+v", fallback)
	}
	context := Explain(Plan{Resolution: ResolutionResolved, IntentEvidence: IntentEvidence{Mode: "observe", Source: "context"}})
	if context.ClassifierResult != "bypassed_by_context" {
		t.Fatalf("context classifier explanation = %+v", context)
	}
	failedObservation := Explain(Plan{IntentEvidence: IntentEvidence{Mode: "observe", Source: "rules", FallbackReason: "service request failed"}})
	if failedObservation.ClassifierResult != "observation_failed" {
		t.Fatalf("observe classifier explanation = %+v", failedObservation)
	}
	combined := Explain(Plan{Resolution: ResolutionResolved, Valid: false, Budget: BudgetSummary{Sufficient: false},
		Models: []ModelChoice{{Role: "plan", Available: false}}, Reasons: []Reason{{Stage: "workflow", Message: "unclassified compile failure"}}})
	if combined.ReasonsComplete || len(combined.ReadinessReasons) != 2 || combined.ReadinessReasons[0] != "budget_insufficient" ||
		combined.ReadinessReasons[1] != "model_unavailable" {
		t.Fatalf("combined invalid causes presented as complete: %+v", combined)
	}
}

func TestExplainMultiRepositoryChoicesAndTruncation(t *testing.T) {
	secret := "SECRET-PRIVATE-REPOSITORY"
	plan := Plan{Repositories: []string{secret, "another-private-repository"}, Capabilities: []CapabilityChoice{
		{ID: "code.search", Repository: secret, Chosen: "codex.private-adapter"},
		{ID: "code.search", Repository: "another-private-repository", Unavailable: true},
	}}
	for i := 2; i < maxExplainedCapabilities+2; i++ {
		plan.Capabilities = append(plan.Capabilities, CapabilityChoice{ID: "code.search", Repository: secret, Chosen: "codex.private-adapter"})
	}
	got := Explain(plan)
	if got.CapabilitiesTotal != maxExplainedCapabilities+2 || len(got.Capabilities) != maxExplainedCapabilities || !got.CapabilitiesOmitted ||
		got.Capabilities[0].RepositoryOrdinal != 1 || got.Capabilities[1].RepositoryOrdinal != 2 ||
		got.Capabilities[0].Selection != "selected" || got.Capabilities[0].Provider != "unknown" || got.Capabilities[1].Selection != "unavailable" {
		t.Fatalf("multi-repo explanation = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private-adapter") {
		t.Fatalf("untrusted identifiers leaked: %s", raw)
	}
	if ordinal := repositoryOrdinal([]string{"duplicate", "duplicate"}, "duplicate"); ordinal != 0 {
		t.Fatalf("ambiguous repository ordinal = %d", ordinal)
	}
}

func TestExplainNeverCopiesUntrustedContentAndBoundsOutput(t *testing.T) {
	secret := "SECRET-PATH-/Users/person/private-key"
	plan := Plan{Text: secret, Criterion: secret, Intent: Kind(secret), Resolution: ResolutionStatus(secret),
		ResolutionReason: secret, IntentEvidence: IntentEvidence{Mode: secret, Source: secret, FallbackReason: secret},
		Reasons: []Reason{{Stage: secret, Message: secret}}, Effects: []contract.Effect{contract.Effect(255)}}
	for i := 0; i < 1000; i++ {
		plan.Capabilities = append(plan.Capabilities, CapabilityChoice{ID: secret, Repository: secret, Chosen: secret, Reason: secret, Providers: []string{secret}})
	}
	got := Explain(plan)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || len(raw) > 3500 || !got.CapabilitiesOmitted || !got.EffectsOmitted ||
		got.CapabilitiesTotal != 1000 || len(got.Capabilities) != maxExplainedCapabilities {
		t.Fatalf("privacy/size boundary failed: bytes=%d explanation=%+v", len(raw), got)
	}
	if got.Intent != "unknown" || got.ResolutionCode != "unknown" || got.Capabilities[0].Capability != "unknown" {
		t.Fatalf("unknowns not explicit: %+v", got)
	}
}
