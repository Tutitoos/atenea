package contract_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestPortableContractHelpers(t *testing.T) {
	for _, name := range []string{
		"unspecified", "invalid_input", "not_found", "permission_denied",
		"external_denied", "unavailable", "timeout", "canceled",
	} {
		if got, err := contract.ParseFailureKind("  " + name + " "); err != nil || got.String() != name {
			t.Fatalf("failure %q round trip = %v, %v", name, got, err)
		}
	}
	if _, err := contract.ParseFailureKind("unknown"); err == nil {
		t.Fatal("unknown failure kind was accepted")
	}
	if got := contract.MessageOf(contract.Fail(contract.FailureTimeout, "slow")); got != "slow" {
		t.Fatalf("MessageOf failure = %q", got)
	}
	if got := contract.MessageOf(errors.New("plain")); got != "plain" {
		t.Fatalf("MessageOf plain = %q", got)
	}
	if got := contract.MessageOf(nil); got != "" {
		t.Fatalf("MessageOf nil = %q", got)
	}
	if got := contract.StopKind(context.Canceled); got != contract.FailureCanceled {
		t.Fatalf("canceled context = %v", got)
	}
	if got := contract.StopKind(context.DeadlineExceeded); got != contract.FailureTimeout {
		t.Fatalf("deadline context = %v", got)
	}
	if got := contract.Stopped(context.Canceled, "runner", time.Second); got.Kind != contract.FailureCanceled {
		t.Fatalf("stopped cancellation = %v", got)
	}
	if got := contract.Stopped(context.DeadlineExceeded, "runner", time.Second); got.Kind != contract.FailureTimeout {
		t.Fatalf("stopped deadline = %v", got)
	}

	if got := contract.VCS(99).String(); got != "vcs(99)" {
		t.Fatalf("unknown VCS = %q", got)
	}
	if got := contract.HealthState(99).String(); got != "unknown" {
		t.Fatalf("unknown health = %q", got)
	}
	if got := contract.ScopeGuarantee(99).String(); got != "scope(99)" {
		t.Fatalf("unknown scope guarantee = %q", got)
	}
	for _, name := range []string{"", "filtered", "confined"} {
		if _, err := contract.ParseScopeGuarantee(name); err != nil {
			t.Fatalf("scope %q: %v", name, err)
		}
	}
	if _, err := contract.ParseScopeGuarantee("guessed"); err == nil {
		t.Fatal("unknown scope guarantee was accepted")
	}

	if !(contract.Cost{Attempts: 2}).Barren(0) {
		t.Fatal("a failed implementation was not marked barren")
	}
	if (contract.Cost{Samples: 1, Attempts: 2}).Barren(2) {
		t.Fatal("a measured implementation was marked barren")
	}
}

func TestPortableAssignmentHelpers(t *testing.T) {
	task := contract.Task{Objective: "inspect code", Criterion: "report findings", Files: []string{"main.go"}}
	spec := contract.AgentTypeSpec{
		Name:   "reviewer",
		Kind:   contract.AgentSpecialized,
		Result: []contract.Field{{Name: "summary", Type: contract.TypeString, Required: true}},
	}
	if _, err := spec.ResultSchema(); err != nil {
		t.Fatalf("ResultSchema: %v", err)
	}
	if err := spec.ValidateResult(map[string]any{"summary": "done"}); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	clone := spec.Clone()
	clone.Result[0].Name = "details"
	if spec.Result[0].Name == clone.Result[0].Name {
		t.Fatal("AgentTypeSpec.Clone shared result fields")
	}

	root := contract.RootAssignment("root", "planner", contract.AgentOrchestrator, task,
		contract.Limits{MaxDuration: time.Minute, MaxTokens: 1000})
	root.Context = []contract.ContextLevel{contract.ContextRepository}
	if !root.Causes(contract.EffectRead) || root.Causes(contract.EffectWrite) {
		t.Fatal("Causes returned the wrong effect grant")
	}
	if !root.Sees(contract.ContextRepository) || root.Sees(contract.ContextGlobal) {
		t.Fatal("Sees returned the wrong context grant")
	}

	subject := contract.Subject{
		RunID: "child", TypeName: "worker", Task: task, Attempt: 1,
		Result: map[string]any{"summary": "done"}, Verdict: contract.VerdictOK,
	}
	if err := subject.Validate(); err != nil {
		t.Fatalf("Subject.Validate: %v", err)
	}
	subjectClone := subject.Clone()
	subjectClone.Result["summary"] = "changed"
	if subject.Result["summary"] == subjectClone.Result["summary"] {
		t.Fatal("Subject.Clone shared result map")
	}

	reason := contract.Reason{Kind: contract.FailureNotFound, Text: "no match"}
	if err := reason.Validate(); err != nil {
		t.Fatalf("Reason.Validate: %v", err)
	}
	if reason.String() != "not_found: no match" || reason.Empty() {
		t.Fatalf("reason helpers = %q, empty=%v", reason.String(), reason.Empty())
	}
	if !(contract.Reason{}).Empty() {
		t.Fatal("zero Reason was not empty")
	}

	firstUSD, secondUSD := 0.10, 0.20
	first := contract.Charge{InputTokens: 2, CacheReadTokens: 3, USD: &firstUSD, PricedBy: "one"}
	second := contract.Charge{OutputTokens: 4, CacheWriteTokens: 5, USD: &secondUSD, PricedBy: "two"}
	if !first.Measured() || first.Tokens() != 5 {
		t.Fatalf("charge measurement = %v, tokens=%d", first.Measured(), first.Tokens())
	}
	combined := first.Plus(second)
	if combined.Tokens() != 14 || combined.USD == nil || *combined.USD < 0.299 || *combined.USD > 0.301 || combined.PricedBy != "one and two" {
		t.Fatalf("combined charge = %+v", combined)
	}
	if got := (contract.Charge{}).Plus(first); got.USD == nil || *got.USD != firstUSD {
		t.Fatalf("unmeasured plus measured = %+v", got)
	}
	if got := first.Plus(contract.Charge{}); got.Tokens() != first.Tokens() {
		t.Fatalf("measured plus empty = %+v", got)
	}
	if got := first.Plus(contract.Charge{OutputTokens: 1}); got.USD != nil {
		t.Fatal("partially priced charge kept an unsupported USD total")
	}

	report := contract.Report{Result: map[string]any{"summary": "done"}, Verdict: contract.VerdictOK}
	created := report.Subject("run-1", "worker", 1, task)
	if created.RunID != "run-1" || created.TypeName != "worker" || created.Verdict != contract.VerdictOK {
		t.Fatalf("Report.Subject = %+v", created)
	}
}

func TestPortableRunRequestAndOutputSchema(t *testing.T) {
	capability := contract.Capability{
		ID: "code.search", Version: contract.Current, Summary: "search",
		Inputs:  []contract.Field{{Name: "query", Type: contract.TypeString, Required: true}},
		Outputs: []contract.Field{{Name: "count", Type: contract.TypeInt, Required: true}},
		Effects: []contract.Effect{contract.EffectRead, contract.EffectProcess},
	}
	if _, err := capability.OutputSchema(); err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	request := contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: "searcher", Provider: "local", Capability: capability.ID},
		Repository:     contract.NewRepository("repo", "/tmp/repo", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
		Payload:        map[string]any{"query": "needle"},
		Permission:     contract.Permission{Task: "search", Effects: []contract.Effect{contract.EffectRead}},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("RunRequest.Validate: %v", err)
	}
	if effect, ok := request.Allowed(); ok || effect != contract.EffectProcess {
		t.Fatalf("Allowed missing effect = %v, %v", effect, ok)
	}
	request.Permission = request.Permission.Grant([]contract.Effect{contract.EffectProcess})
	if effect, ok := request.Allowed(); !ok || effect != 0 {
		t.Fatalf("Allowed complete grant = %v, %v", effect, ok)
	}
	request.Implementation.Capability = "other.capability"
	if err := request.Validate(); err == nil {
		t.Fatal("request with mismatched implementation was accepted")
	}
}
