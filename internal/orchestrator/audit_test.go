package orchestrator

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A receipt for a device step has to say what the machine was pointed at.
// "desktop.inspect ran" leaves out the only thing an auditor opened the
// receipt to find.
func TestADeviceStepRecordsWhatItWasPointedAt(t *testing.T) {
	step := contract.Step{
		Capability: "desktop.inspect",
		Payload:    map[string]any{"application": "com.apple.finder"},
		Permission: contract.Permission{
			Effects: []contract.Effect{contract.EffectRead, contract.EffectDevice},
		},
	}
	got := auditableInputs(step)
	if !strings.Contains(got, "com.apple.finder") {
		t.Errorf("inputs = %q, want the application it was pointed at", got)
	}
}

// And every other step does not, because recording them all would double the
// size of every receipt on disk to answer a question nobody asks of a code
// search -- whose query is recoverable from the commission anyway.
func TestAStepWithoutTheDeviceEffectRecordsNothing(t *testing.T) {
	step := contract.Step{
		Capability: "code.search",
		Payload:    map[string]any{"query": "TODO"},
		Permission: contract.Permission{
			Effects: []contract.Effect{contract.EffectRead, contract.EffectProcess},
		},
	}
	if got := auditableInputs(step); got != "" {
		t.Errorf("inputs = %q, want nothing recorded", got)
	}
}

// A payload is caller-supplied and a receipt is durable. This is the one field
// where somebody else's secret could otherwise come to rest on disk, so it
// goes through the same redaction as provider text.
func TestACredentialInAPayloadDoesNotReachTheReceipt(t *testing.T) {
	step := contract.Step{
		Capability: "desktop.type",
		Payload:    map[string]any{"text": "password: hunter2seventeen"},
		Permission: contract.Permission{
			Effects: []contract.Effect{contract.EffectDevice, contract.EffectWrite},
		},
	}
	got := auditableInputs(step)
	if strings.Contains(got, "hunter2seventeen") {
		t.Fatalf("a credential reached the receipt: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("inputs = %q, want the redaction to be visible rather than silent", got)
	}
}
