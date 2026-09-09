package workflow

import (
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestWatchdogOutlivesTheLongestConfiguredAgentTurn(t *testing.T) {
	types := []config.AgentType{
		{Limits: contract.Limits{MaxDuration: 30 * time.Second}},
		{Limits: contract.Limits{MaxDuration: 10 * time.Minute}},
	}

	if got, want := watchdogFor(types), 11*time.Minute; got != want {
		t.Fatalf("watchdog = %s, want %s", got, want)
	}
}

func TestWatchdogKeepsABoundedDefaultWithoutAnAgentDuration(t *testing.T) {
	types := []config.AgentType{{Limits: contract.Limits{}}}

	if got := watchdogFor(types); got != defaultWatchdog {
		t.Fatalf("watchdog = %s, want default %s", got, defaultWatchdog)
	}
}

func TestWatchdogDurationCannotOverflow(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	types := []config.AgentType{{Limits: contract.Limits{MaxDuration: maxDuration}}}

	if got := watchdogFor(types); got != maxDuration {
		t.Fatalf("watchdog = %s, want saturated %s", got, maxDuration)
	}
}
