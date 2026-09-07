package acceptance

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireExecutedTestsRejectsPartialExpectedSet(t *testing.T) {
	output := strings.Join([]string{
		`{"Action":"run","Test":"TestAPI"}`,
		`{"Action":"pass","Test":"TestAPI"}`,
	}, "\n")
	if err := requireExecutedTests(output, []string{"TestAPI", "TestSSE"}); err == nil || !strings.Contains(err.Error(), "TestSSE") {
		t.Fatalf("partial expected set was accepted: %v", err)
	}
}

func TestRequireExecutedTestsRequiresRunAndPassForEveryTest(t *testing.T) {
	output := strings.Join([]string{
		`{"Action":"run","Test":"TestAPI"}`,
		`{"Action":"pass","Test":"TestAPI"}`,
		`{"Action":"run","Test":"TestSSE"}`,
		`{"Action":"pass","Test":"TestSSE"}`,
	}, "\n")
	if err := requireExecutedTests(output, []string{"TestAPI", "TestSSE"}); err != nil {
		t.Fatal(err)
	}
}

func TestProductReportKeepsCorpusFixedAndSeparatesLimits(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	seen := map[string]bool{}
	probes := 0
	report, err := RunWith(context.Background(), root, func(_ context.Context, _ string, input GateInput) (string, error) {
		calls++
		if input.GateID == "" || len(input.Packages) == 0 || len(input.Scenarios) == 0 {
			t.Fatalf("gate input = %+v", input)
		}
		for _, scenario := range input.Scenarios {
			if scenario.FixtureSHA256 == "" || scenario.Preflight == "" {
				t.Fatalf("scenario input = %+v", scenario)
			}
			seen[scenario.ID] = true
		}
		output := "ok " + strings.Join(input.Packages, ",")
		if input.GateID == "context" {
			output += "\ncode.context fixture calls: before=22 after=2"
		}
		return output, nil
	}, func(_ context.Context, _ string, input ScenarioInput) (string, error) {
		probes++
		if input.Scenario.ID == "" || len(input.FixtureBytes) == 0 || input.FixtureSHA256 == "" {
			t.Fatalf("scenario probe input = %+v", input)
		}
		return "product probe passed for " + input.Scenario.ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.CorpusHash == "" || report.SourceDigest == "" || calls != len(productGates) || probes != 12 || len(seen) != 12 || len(report.Limits) != 4 {
		t.Fatalf("report = %+v calls=%d", report, calls)
	}
	if !report.Comparison.Passed || len(report.Comparison.Metrics) < 6 || report.Summary["passed"] != len(productGates)+1 {
		t.Fatalf("comparison = %+v summary=%+v", report.Comparison, report.Summary)
	}
	if !strings.Contains(Markdown(report), "Provider-real evidence remains pending") {
		t.Fatal("report hid its evidence boundary")
	}
}
