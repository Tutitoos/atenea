package decision

import (
	"bufio"
	"context"
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
	if plan.Intent != KindChange || plan.IntentEvidence.Source != "rules" || plan.IntentEvidence.Laya == nil || plan.IntentEvidence.Laya.Intent != KindPlan {
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

func TestLayaModeUsesRulesWhenConfidenceIsBelowTheConfiguredMinimum(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Decision = config.DecisionSettings{Mode: "laya", MinimumConfidence: 0.8}
	plan, err := (Planner{Config: cfg, Classifier: intentClassifierFunc(func(context.Context, string) (IntentClassification, error) {
		return IntentClassification{Intent: KindPlan, Confidence: 0.79, Model: "multilingual"}, nil
	})}).Build(Request{Text: "Do not make changes yet. Tell me how you would add Laya.", Repository: "repo", BudgetUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindChange || plan.IntentEvidence.Source != "rules" || plan.IntentEvidence.Laya == nil ||
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
			if plan.Intent != KindChange || plan.IntentEvidence.Source != "rules" || plan.IntentEvidence.FallbackReason == "" {
				t.Fatalf("intent/evidence = %s/%+v", plan.Intent, plan.IntentEvidence)
			}
		})
	}
}

func TestIntentEvaluationCorpusIsBalancedAndMatchesTheRulesBaseline(t *testing.T) {
	file, err := os.Open("testdata/intent-evaluation.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	type item struct {
		Text        string `json:"text"`
		Expected    Kind   `json:"expected"`
		RulesIntent Kind   `json:"rules_intent"`
		Split       string `json:"split"`
	}
	counts := make(map[string]int)
	splits := make(map[string]int)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row item
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if got := infer(row.Text); got != row.RulesIntent {
			t.Errorf("rules baseline for %q = %s, corpus says %s", row.Text, got, row.RulesIntent)
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
