package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceIdentityIgnoresCoverageProfile(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "-q"},
		{"git", "config", "user.email", "atenea-test@example.invalid"},
		{"git", "config", "user.name", "ATENEA Test"},
	} {
		if output, err := command(context.Background(), root, args, nil); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/coverage.out\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := command(context.Background(), root, []string{"git", "add", ".gitignore", "source.go"}, nil); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	if output, err := command(context.Background(), root, []string{"git", "commit", "-qm", "fixture"}, nil); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}
	commit, err := command(context.Background(), root, []string{"git", "rev-parse", "HEAD"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _, dirtyBefore, err := sourceIdentity(context.Background(), root, strings.TrimSpace(commit))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coverage.out"), []byte("mode: atomic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _, dirtyAfter, err := sourceIdentity(context.Background(), root, strings.TrimSpace(commit))
	if err != nil {
		t.Fatal(err)
	}
	if before != after || dirtyBefore || dirtyAfter {
		t.Fatalf("ignored coverage profile changed identity: before=%s after=%s dirty=%v/%v", before, after, dirtyBefore, dirtyAfter)
	}
}

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
