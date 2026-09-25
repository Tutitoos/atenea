package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tutitoos/atenea/internal/decision"
)

func TestPairedDryRunUsesOneLayaCallPerCaseAndKeepsPrivateTextOutOfReport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			State map[string]string `json:"state"`
			Model string            `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("request body: %v", err)
		}
		if request.Model != "multilingual" {
			t.Errorf("requested model = %q", request.Model)
		}
		intent := "change"
		if strings.Contains(request.State["body"], "Do not make changes") {
			intent = "plan"
		}
		_, _ = w.Write([]byte(`{"model":"test-model","routing":{"model":"multilingual"},"answers":{"intent":{"type":"choice","choice":"` + intent + `","answer_confidence":0.95}}}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	cases := filepath.Join(dir, "cases.jsonl")
	labels := filepath.Join(dir, "labels.jsonl")
	reportFile := filepath.Join(dir, "report.json")
	packetFile := filepath.Join(dir, "packet.jsonl")
	if err := os.WriteFile(cases, []byte("{\"id\":\"one\",\"split\":\"test\",\"text\":\"Do not make changes yet. Tell me how you would add Laya.\"}\n"+
		"{\"id\":\"two\",\"split\":\"test\",\"text\":\"Implement the fix now.\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(labels, []byte("{\"id\":\"one\",\"expected\":\"plan\"}\n"+
		"{\"id\":\"two\",\"expected\":\"change\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"--cases", cases, "--labels", labels, "--settings", testSettings(t),
		"--endpoint", server.URL + "/v1/systemone", "--model", "multilingual",
		"--report", reportFile, "--review-packet", packetFile}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("service calls = %d, want one per case", calls.Load())
	}
	bytes, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "Do not make changes") || strings.Contains(string(bytes), "Implement the fix") {
		t.Fatal("metrics report contains private commission text")
	}
	var got report
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatal(err)
	}
	metrics := got.BySplit["test"]
	if got.Labeled != 2 || metrics == nil || metrics.RulesCorrect != 2 || metrics.GatedCorrect != 2 ||
		metrics.GatedWins != 0 || metrics.FalseChangeRules != 0 || metrics.FalseChangeGated != 0 ||
		metrics.UnsafePlans != 0 {
		t.Fatalf("report metrics = %+v", got)
	}
	if got.Cases[0].Rules != decision.KindPlan || got.Cases[0].Gated != decision.KindPlan ||
		got.Cases[0].Model != "test-model" || got.Cases[0].RoutingModel != "multilingual" {
		t.Fatalf("first case = %+v", got.Cases[0])
	}
	packet, err := os.ReadFile(packetFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packet), "Do not make changes") || strings.Contains(string(packet), "test-model") ||
		strings.Contains(string(packet), "gated") || strings.Contains(string(packet), "rules") {
		t.Fatal("review packet is missing the request or leaks variant identity")
	}
	for _, path := range []string{reportFile, packetFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("private artifact %s mode = %v", path, info.Mode().Perm())
		}
	}
}

func TestReadLabelsRequiresCompleteIndependentGoldSet(t *testing.T) {
	cases := []sample{{ID: "one", Text: "plan this", Split: "test"}, {ID: "two", Text: "fix this", Split: "test"}}
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":\"one\",\"expected\":\"plan\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLabels(path, cases); err == nil {
		t.Fatal("accepted a partial gold set")
	}
}

func TestEvaluationEndpointRequiresLoopbackForPlaintext(t *testing.T) {
	for _, endpoint := range []string{
		"http://laya.example.test/v1/systemone",
		"https://user:secret@example.test/v1/systemone",
		"https://example.test/v1/systemone?token=secret",
		"http://127.0.0.1:8000/predict",
	} {
		if err := validateEndpoint(endpoint); err == nil {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
	if err := validateEndpoint("http://127.0.0.1:8000/v1/systemone"); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailableServiceKeepsRulesPlanAndReportsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":\"one\",\"split\":\"shadow\",\"text\":\"Plan the migration.\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"--cases", path, "--settings", testSettings(t),
		"--endpoint", server.URL + "/v1/systemone"}, &out); err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ServiceFailures != 1 || got.ServiceResponses != 0 || got.Cases[0].Rules != got.Cases[0].Gated ||
		!got.Cases[0].Safe || got.Cases[0].Fallback == "" {
		t.Fatalf("fallback report = %+v", got)
	}
}

func TestScopedContextContinuationAndNeedsContextAreScoredWithoutFalseServiceFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"model":"test-model","routing":{"model":"multilingual"},"answers":{"intent":{"type":"choice","choice":"change","answer_confidence":0.95}}}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	cases := filepath.Join(dir, "cases.jsonl")
	labels := filepath.Join(dir, "labels.jsonl")
	reportFile := filepath.Join(dir, "report.json")
	packetFile := filepath.Join(dir, "packet.jsonl")
	caseRows := "{\"id\":\"continue\",\"split\":\"context\",\"text\":\"hazlo\",\"context\":{\"version\":1,\"repository\":\"current\",\"accepted_plan_id\":\"plan-secret-47\",\"accepted_plan_revision\":\"r2\",\"accepted_plan_current\":true,\"active_objective\":\"OBJECTIVE_SECRET keep migration compatible\",\"scope_files\":[\"internal/decision/eval.go\"],\"constraints\":[\"CONSTRAINT_SECRET preserve behavior\"]}}\n" +
		"{\"id\":\"scope\",\"split\":\"context\",\"text\":\"Implement the migration now.\",\"files\":[\"docs/outside.md\"],\"context\":{\"version\":1,\"repository\":\"current\",\"active_objective\":\"SCOPE_OBJECTIVE_SECRET\",\"scope_files\":[\"internal/decision/eval.go\"]}}\n" +
		"{\"id\":\"invalid\",\"split\":\"context\",\"text\":\"Implement the migration now.\",\"context\":{\"version\":2,\"repository\":\"current\",\"active_objective\":\"INVALID_CONTEXT_SECRET\"}}\n" +
		"{\"id\":\"missing\",\"split\":\"context\",\"text\":\"hazlo\"}\n"
	if err := os.WriteFile(cases, []byte(caseRows), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(labels, []byte("{\"id\":\"continue\",\"expected\":\"change\",\"expected_resolution\":\"resolved\"}\n"+
		"{\"id\":\"scope\",\"expected_resolution\":\"needs_context\"}\n"+
		"{\"id\":\"invalid\",\"expected_resolution\":\"needs_context\"}\n"+
		"{\"id\":\"missing\",\"expected_resolution\":\"needs_context\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"--cases", cases, "--labels", labels, "--settings", testSettings(t),
		"--endpoint", server.URL + "/v1/systemone", "--model", "multilingual",
		"--report", reportFile, "--review-packet", packetFile}, &out); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("Laya calls = %d, want one baseline call for each ordinary request", calls.Load())
	}
	reportBytes, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hazlo", "OBJECTIVE_SECRET", "CONSTRAINT_SECRET", "SCOPE_OBJECTIVE_SECRET", "INVALID_CONTEXT_SECRET", "plan-secret-47", "internal/decision/eval.go"} {
		if strings.Contains(string(reportBytes), secret) {
			t.Fatalf("metrics report leaks private input %q", secret)
		}
	}
	var got report
	if err := json.Unmarshal(reportBytes, &got); err != nil {
		t.Fatal(err)
	}
	metrics := got.BySplit["context"]
	if got.ServiceResponses != 2 || got.ServiceFailures != 0 || got.ServiceIntentionalSkips != 5 ||
		got.ContextServiceResponses != 0 || got.ContextServiceFailures != 0 || got.ContextServiceIntentionalSkips != 3 ||
		metrics == nil || metrics.Count != 4 || metrics.ContextCount != 3 || metrics.ContextLabeled != 3 ||
		metrics.RulesCorrect != 1 || metrics.GatedCorrect != 1 ||
		metrics.ContextCorrect != 3 || metrics.ContextGatedCorrect != 3 || metrics.ContextGatedWins != 3 ||
		metrics.ContextGatedLosses != 0 || metrics.FalseChangeContextGated != 0 || metrics.UnsafePlans != 0 {
		t.Fatalf("context report metrics = %+v, report=%+v", metrics, got)
	}
	if got.Cases[0].RulesPlan.Resolution != decision.ResolutionNeedsContext ||
		got.Cases[0].ContextPlan.Intent != decision.KindChange || !got.Cases[0].ContextPlan.Valid ||
		got.Cases[0].LayaDisposition != "skipped_needs_context" ||
		got.Cases[0].ContextLayaDisposition != "skipped_context_resolved" ||
		got.Cases[1].ContextPlan.Resolution != decision.ResolutionNeedsContext || len(got.Cases[1].ContextPlan.Steps) != 0 ||
		got.Cases[2].ContextPlan.Resolution != decision.ResolutionNeedsContext ||
		got.Cases[2].ContextPlan.ResolutionReason != decision.ResolutionReasonInvalidContext || len(got.Cases[2].ContextPlan.Steps) != 0 ||
		got.Cases[3].RulesPlan.Resolution != decision.ResolutionNeedsContext || len(got.Cases[3].RulesPlan.Steps) != 0 {
		t.Fatalf("context plans or Laya dispositions = %+v", got.Cases)
	}
	packet, err := os.ReadFile(packetFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"hazlo", "OBJECTIVE_SECRET", "CONSTRAINT_SECRET", "SCOPE_OBJECTIVE_SECRET", "internal/decision/eval.go"} {
		if !strings.Contains(string(packet), privateValue) {
			t.Fatalf("private blinded review packet is missing %q", privateValue)
		}
	}
	if strings.Contains(string(packet), "test-model") || strings.Contains(string(packet), "claude-opus") ||
		strings.Contains(string(packet), "plan-secret-47") || strings.Contains(string(packet), `"accepted_plan_revision":"r2"`) ||
		strings.Contains(string(packet), `"repository":"current"`) {
		t.Fatal("review packet contains classifier/model identity or accepted-plan identifier")
	}
}

func TestContextAugmentsLayaRequestAndContextMetricsUseOnlySuppliedRows(t *testing.T) {
	bodies := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			State map[string]string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("request body: %v", err)
		}
		bodies <- request.State["body"]
		_, _ = w.Write([]byte(`{"model":"test-model","routing":{"model":"multilingual"},"answers":{"intent":{"type":"choice","choice":"plan","answer_confidence":0.95}}}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	cases := filepath.Join(dir, "cases.jsonl")
	labels := filepath.Join(dir, "labels.jsonl")
	if err := os.WriteFile(cases, []byte("{\"id\":\"plain\",\"split\":\"test\",\"text\":\"Planifica la migración del índice.\"}\n"+
		"{\"id\":\"contextual\",\"split\":\"test\",\"text\":\"Planifica la migración del índice.\",\"context\":{\"version\":1,\"repository\":\"current\",\"active_objective\":\"OBJECTIVE_SECRET preserve tenant boundaries\",\"constraints\":[\"CONSTRAINT_SECRET avoid downtime\"]}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(labels, []byte("{\"id\":\"plain\",\"expected\":\"plan\"}\n{\"id\":\"contextual\",\"expected\":\"plan\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"--cases", cases, "--labels", labels, "--settings", testSettings(t),
		"--endpoint", server.URL + "/v1/systemone", "--model", "multilingual"}, &out); err != nil {
		t.Fatal(err)
	}
	plainBody, baselineBody, contextBody := <-bodies, <-bodies, <-bodies
	if plainBody != "Planifica la migración del índice." || baselineBody != plainBody ||
		!strings.Contains(contextBody, "OBJECTIVE_SECRET preserve tenant boundaries") ||
		!strings.Contains(contextBody, "CONSTRAINT_SECRET avoid downtime") ||
		!strings.Contains(contextBody, "Current user request:\nPlanifica la migración del índice.") {
		t.Fatalf("Laya input bodies = %q / %q / %q", plainBody, baselineBody, contextBody)
	}
	var got report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	metrics := got.BySplit["test"]
	if metrics == nil || metrics.Count != 2 || metrics.ContextCount != 1 || metrics.ContextLabeled != 1 ||
		metrics.ContextCorrect != 1 || metrics.ContextGatedCorrect != 1 || metrics.ContextLayaLabeled != 1 ||
		metrics.ContextLayaCorrect != 1 || got.ServiceResponses != 3 || got.ContextServiceResponses != 1 {
		t.Fatalf("context subset metrics = %+v, report=%+v", metrics, got)
	}
}

func TestReadLabelsAcceptsNeedsContextWithoutIntentAndRejectsMixedGold(t *testing.T) {
	cases := []sample{{ID: "needs", Text: "hazlo", Split: "test"}}
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":\"needs\",\"expected_resolution\":\"needs_context\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	labels, err := readLabels(path, cases)
	if err != nil || expectedResolution(labels["needs"]) != decision.ResolutionNeedsContext {
		t.Fatalf("needs_context label = %+v, error=%v", labels, err)
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"needs\",\"expected\":\"change\",\"expected_resolution\":\"needs_context\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLabels(path, cases); err == nil {
		t.Fatal("accepted a needs_context label with an intent")
	}
}

func testSettings(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../internal/config/default.toml")
	if err != nil {
		t.Fatal(err)
	}
	settings := string(raw)
	for original, replacement := range map[string]string{
		`path = "."`:   `path = ` + strconv.Quote(t.TempDir()),
		`explore = ""`: `explore = "sonnet"`,
		`plan = ""`:    `plan = "claude-opus-5"`,
	} {
		if !strings.Contains(settings, original) {
			t.Fatalf("default settings did not contain %q", original)
		}
		settings = strings.Replace(settings, original, replacement, 1)
	}
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
