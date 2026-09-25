package decision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

type intentClassifierFunc func(context.Context, string) (IntentClassification, error)

func (f intentClassifierFunc) Classify(ctx context.Context, text string) (IntentClassification, error) {
	return f(ctx, text)
}

func TestRulesModeDoesNotCallLaya(t *testing.T) {
	calls := 0
	classifier := intentClassifierFunc(func(context.Context, string) (IntentClassification, error) {
		calls++
		return IntentClassification{Intent: KindPlan, Confidence: 0.99}, nil
	})
	plan, err := (Planner{Config: fixtureConfig("repo"), Classifier: classifier}).Build(Request{
		Text: "add the Laya classifier", Repository: "repo", BudgetUSD: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || plan.Intent != KindChange || plan.IntentEvidence.Mode != "rules" || plan.IntentEvidence.Source != "rules" {
		t.Fatalf("calls=%d intent=%s evidence=%+v", calls, plan.Intent, plan.IntentEvidence)
	}
}

func TestObserveModeRecordsLayaWithoutChangingThePlan(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Decision = config.DecisionSettings{Mode: "observe", MinimumConfidence: 0.8}
	plan, err := (Planner{Config: cfg, Classifier: intentClassifierFunc(func(context.Context, string) (IntentClassification, error) {
		return IntentClassification{Intent: KindPlan, Confidence: 0.93, Model: "multilingual", RoutingModel: "multilingual"}, nil
	})}).Build(Request{Text: "Do not make changes yet. Tell me how you would add Laya.", Repository: "repo", BudgetUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindPlan || plan.IntentEvidence.Source != "rules" || plan.IntentEvidence.Laya == nil || plan.IntentEvidence.Laya.Intent != KindPlan {
		t.Fatalf("plan intent/evidence = %s/%+v", plan.Intent, plan.IntentEvidence)
	}
	if !strings.Contains(plan.Reasons[0].Message, "from the request text") || !strings.Contains(plan.Reasons[2].Message, "deterministic intent remained selected") {
		t.Fatalf("intent reasons = %+v", plan.Reasons)
	}
}

func TestLayaModeSelectsAConfidentPlanWithoutGrantingEffects(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Decision = config.DecisionSettings{Mode: "laya", MinimumConfidence: 0.8}
	plan, err := (Planner{Config: cfg, Classifier: intentClassifierFunc(func(context.Context, string) (IntentClassification, error) {
		return IntentClassification{Intent: KindPlan, Confidence: 0.93, Model: "typed-decisions"}, nil
	})}).Build(Request{Text: "Do not make changes yet. Tell me how you would add Laya.", Repository: "repo", BudgetUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindPlan || plan.IntentEvidence.Source != "laya" || len(plan.Workflow.Steps) != 3 {
		t.Fatalf("intent=%s evidence=%+v steps=%d", plan.Intent, plan.IntentEvidence, len(plan.Workflow.Steps))
	}
	if len(plan.Effects) != 0 {
		t.Fatalf("Laya granted effects: %v", plan.Effects)
	}
}

func TestAcceptedPlanContinuationTakesPrecedenceOverLaya(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Decision = config.DecisionSettings{Mode: "laya", MinimumConfidence: 0.8}
	calls := 0
	plan, err := (Planner{Config: cfg, Classifier: intentClassifierFunc(func(context.Context, string) (IntentClassification, error) {
		calls++
		return IntentClassification{Intent: KindPlan, Confidence: 0.99, Model: "multilingual"}, nil
	})}).BuildContext(t.Context(), Request{
		Text: "hazlo", Repository: "repo", BudgetUSD: 10,
		Context: &IntentContext{
			Version: 1, Repository: "repo", AcceptedPlanID: "plan-123", AcceptedPlanRevision: "rev-1",
			AcceptedPlanCurrent: true, ActiveObjective: "implement the requested feature",
			ScopeFiles: []string{"internal/feature.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Laya classified an accepted-plan continuation %d times", calls)
	}
	if plan.Intent != KindChange || plan.Resolution != ResolutionResolved || !plan.ContextUsed ||
		plan.IntentEvidence.Mode != "laya" || plan.IntentEvidence.Source != "context" || plan.IntentEvidence.Laya != nil {
		t.Fatalf("continuation intent/evidence = %s/%+v", plan.Intent, plan.IntentEvidence)
	}
}

func TestLayaModeUsesRulesWhenConfidenceIsBelowTheConfiguredMinimum(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Decision = config.DecisionSettings{Mode: "laya", MinimumConfidence: 0.8}
	plan, err := (Planner{Config: cfg, Classifier: intentClassifierFunc(func(context.Context, string) (IntentClassification, error) {
		return IntentClassification{Intent: KindPlan, Confidence: 0.79, Model: "multilingual"}, nil
	})}).Build(Request{Text: "Do not make changes yet. Tell me how you would add Laya.", Repository: "repo", BudgetUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindPlan || plan.IntentEvidence.Source != "rules" || plan.IntentEvidence.Laya == nil ||
		!strings.Contains(plan.IntentEvidence.FallbackReason, "below the configured minimum") {
		t.Fatalf("intent/evidence = %s/%+v", plan.Intent, plan.IntentEvidence)
	}
}

func TestLayaFailuresUseRulesAndRemainVisible(t *testing.T) {
	cases := map[string]intentClassifierFunc{
		"unavailable": func(context.Context, string) (IntentClassification, error) {
			return IntentClassification{}, errors.New("service unavailable")
		},
		"unsupported intent": func(context.Context, string) (IntentClassification, error) {
			return IntentClassification{Intent: "delete", Confidence: 1}, nil
		},
		"invalid confidence": func(context.Context, string) (IntentClassification, error) {
			return IntentClassification{Intent: KindPlan, Confidence: math.NaN()}, nil
		},
	}
	for name, classify := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := fixtureConfig("repo")
			cfg.Decision = config.DecisionSettings{Mode: "laya", MinimumConfidence: 0.8}
			plan, err := (Planner{Config: cfg, Classifier: classify}).Build(Request{
				Text: "Do not make changes yet. Tell me how you would add Laya.", Repository: "repo", BudgetUSD: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Intent != KindPlan || plan.IntentEvidence.Source != "rules" || plan.IntentEvidence.FallbackReason == "" {
				t.Fatalf("intent/evidence = %s/%+v", plan.Intent, plan.IntentEvidence)
			}
		})
	}
}

func TestIntentEvaluationCorpusStaysPinnedToItsMeasuredBaseline(t *testing.T) {
	corpus, err := os.ReadFile("testdata/intent-evaluation.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	reportBytes, err := os.ReadFile("../../benchmarks/runs/laya-intent-2026-09-25/report.json")
	if err != nil {
		t.Fatal(err)
	}
	var measured struct {
		CorpusSHA256 string `json:"corpus_sha256"`
	}
	if err := json.Unmarshal(reportBytes, &measured); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(corpus)
	if measured.CorpusSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("historical Laya report corpus SHA-256 = %s, fixture SHA-256 = %s", measured.CorpusSHA256, hex.EncodeToString(hash[:]))
	}

	type item struct {
		Text        string `json:"text"`
		Expected    Kind   `json:"expected"`
		RulesIntent Kind   `json:"rules_intent"`
		Split       string `json:"split"`
	}
	counts := make(map[string]int)
	splits := make(map[string]int)
	scanner := bufio.NewScanner(strings.NewReader(string(corpus)))
	for scanner.Scan() {
		var row item
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if !valid(row.Expected) || !valid(row.RulesIntent) {
			t.Errorf("corpus has an unsupported label for %q: expected=%s rules=%s", row.Text, row.Expected, row.RulesIntent)
		}
		counts[string(row.Expected)]++
		splits[row.Split]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, intent := range []Kind{KindUnderstand, KindSearch, KindPlan, KindChange} {
		if counts[string(intent)] != 12 {
			t.Errorf("corpus has %d %s examples, want 12", counts[string(intent)], intent)
		}
	}
	if splits["calibration"] != 32 || splits["test"] != 16 || len(splits) != 2 {
		t.Errorf("corpus split counts = %v, want 32 calibration and 16 test", splits)
	}
}

func valid(kind Kind) bool {
	switch kind {
	case KindUnderstand, KindSearch, KindPlan, KindChange:
		return true
	default:
		return false
	}
}
