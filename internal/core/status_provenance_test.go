package core

import (
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestReconcileStatusHealthAttributesSelectedMeasurementsWithDifferentScore(t *testing.T) {
	declared := contract.Health{State: contract.HealthUnknown, Score: 0.91}
	measured := contract.Health{State: contract.HealthAlive, Score: 0.12, Reason: "recent success"}
	got, source, repository := reconcileStatusHealth(declared, measured, "measurements", "api", "configuration", "")
	if got.State != contract.HealthAlive || got.Score != declared.Score || source != "measurements" || repository != "api" {
		t.Fatalf("selected measured health = %+v source=%q repository=%q", got, source, repository)
	}
	if got == measured {
		t.Fatal("fixture must prove full Health equality cannot identify the selected source")
	}
	ignored, source, repository := reconcileStatusHealth(contract.Health{State: contract.HealthDown}, measured,
		"measurements", "elsewhere", "runtime observation", "api")
	if ignored.State != contract.HealthDown || source != "runtime observation" || repository != "api" {
		t.Fatalf("ignored measurement displaced selected source: %+v %q %q", ignored, source, repository)
	}
}

func TestStatusCauseDoesNotCopyProviderDetails(t *testing.T) {
	at := time.Now().UTC()
	impl := ImplementationStatus{ID: "graph.search", Repository: "api", HealthSource: "measurements", Health: contract.Health{
		State: contract.HealthDown, ObservedAt: at,
		Reason: "failed at /Users/private/work\nBearer sensitive-value", Raw: "private response",
	}}
	cause := implementationCause("code.search", impl)
	if cause.Repository != "api" || cause.Evidence != "measurements" || cause.ObservedAt == nil ||
		strings.Contains(cause.Reason, "/Users/") || strings.Contains(cause.Reason, "sensitive-value") ||
		strings.Contains(cause.Reason, "\n") || len(cause.Reason) > 120 {
		t.Fatalf("unsafe cause = %+v", cause)
	}
}
