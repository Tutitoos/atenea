package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestPrintStatusExplainsRedAndObservationFreshness(t *testing.T) {
	observed := time.Date(2026, 10, 7, 7, 0, 0, 0, time.UTC)
	status := core.Status{
		Light:     core.LightRed,
		RedCauses: []core.StatusCause{{Kind: "capability unavailable", Capability: "code.search", Implementation: "graph.search", Repository: "api", State: "down", Evidence: "measurements", Reason: "Measurements report this implementation unavailable.", ObservedAt: &observed}},
		Capabilities: []core.CapabilityStatus{{ID: "code.search", Offered: true, Implementations: []core.ImplementationStatus{
			{ID: "graph.search", Provider: "graph", Repository: "api", HealthSource: "measurements", Health: contract.Health{State: contract.HealthDown, Reason: "provider unavailable", ObservedAt: observed}},
			{ID: "backup.search", Provider: "backup", HealthSource: "measurements", Health: contract.Health{State: contract.HealthUnknown}},
			{ID: "old.search", Provider: "old", HealthExpired: true, Health: contract.Health{State: contract.HealthUnknown, ObservedAt: observed}},
		}}},
	}
	var out bytes.Buffer
	if err := printStatus(&out, status); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"red causes", "code.search / graph.search repo=api state=down checked=2026-10-07T07:00:00Z",
		"checked=unknown (time unknown; source=measurements)", "checked=2026-10-07T07:00:00Z (expired observation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
}
