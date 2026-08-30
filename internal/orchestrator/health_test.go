package orchestrator

import (
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestSuccessfulProbeOnlyRecoversRuntimeDownHealth(t *testing.T) {
	observedAt := time.Now().Add(-time.Hour)
	tests := []struct {
		name     string
		previous contract.Health
		wantOK   bool
	}{
		{
			name: "runtime down",
			previous: contract.Health{
				State:      contract.HealthDown,
				ObservedAt: observedAt,
			},
			wantOK: true,
		},
		{
			name:     "declarative down",
			previous: contract.Health{State: contract.HealthDown},
		},
		{
			name: "runtime alive",
			previous: contract.Health{
				State:      contract.HealthAlive,
				ObservedAt: observedAt,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recovered, ok := healthAfterSuccessfulProbe(tt.previous)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v; health = %+v", ok, tt.wantOK, recovered)
			}
			if !ok {
				return
			}
			if recovered.State != contract.HealthAlive {
				t.Errorf("state = %v, want alive", recovered.State)
			}
			if recovered.Reason == "" {
				t.Error("runtime recovery has no reason")
			}
		})
	}
}
