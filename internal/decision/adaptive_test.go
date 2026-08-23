package decision

import "testing"

type modelHistoryStub map[string]ModelPerformance

func (s modelHistoryStub) Performance(_, role, model string) (ModelPerformance, bool) {
	got, ok := s[role+"/"+model]
	return got, ok
}

func TestAdaptiveModelRankerNeedsEnoughEvidence(t *testing.T) {
	ranker := AdaptiveModelRanker{History: modelHistoryStub{
		"explore/sonnet": {Samples: 2, MedianUSD: 1},
		"explore/haiku":  {Samples: 2, MedianUSD: 0.5},
	}}
	got, reason := ranker.SelectModel("repo", "explore", "sonnet", []string{"sonnet", "haiku"})
	if got != "sonnet" || reason == "" {
		t.Fatalf("choice = %q, reason = %q; expected configured primary while evidence is insufficient", got, reason)
	}
}

func TestAdaptiveModelRankerNeverDowngradesPlanReasoning(t *testing.T) {
	ranker := AdaptiveModelRanker{History: modelHistoryStub{
		"plan/opus":   {Samples: 3, MedianUSD: 1.00},
		"plan/sonnet": {Samples: 3, MedianUSD: 0.80},
		"plan/haiku":  {Samples: 3, MedianUSD: 0.95},
	}}
	got, reason := ranker.SelectModel("repo", "plan", "opus", []string{"opus", "sonnet", "haiku"})
	if got != "opus" {
		t.Fatalf("choice = %q, want opus", got)
	}
	if reason == "" {
		t.Fatal("adaptive choice did not explain itself")
	}
}

func TestAdaptiveModelRankerNeverIntroducesUndeclaredModels(t *testing.T) {
	ranker := AdaptiveModelRanker{History: modelHistoryStub{
		"explore/sonnet": {Samples: 3, MedianUSD: 1.00},
		"explore/opus":   {Samples: 3, MedianUSD: 0.10},
	}}
	got, _ := ranker.SelectModel("repo", "explore", "sonnet", []string{"sonnet"})
	if got != "sonnet" {
		t.Fatalf("choice = %q, want sonnet: undeclared history must be ignored", got)
	}
}
