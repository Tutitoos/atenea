package contract_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestPortableContractHelpers(t *testing.T) {
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

func TestPortableChargeHelpers(t *testing.T) {
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
		Repository:     contract.NewRepository("repo", "/tmp/repo", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified),
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
