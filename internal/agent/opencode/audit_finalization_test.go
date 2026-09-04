package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestFinalizationUsesDenyAllIsolatedEnvironment checks the regression scenario: finalization uses deny all isolated environment.
func TestFinalizationUsesDenyAllIsolatedEnvironment(t *testing.T) {
	inherited := []string{"OPENCODE_CONFIG_CONTENT=unsafe", "OPENCODE_PERMISSION=unsafe", "XDG_CONFIG_HOME=/unsafe", "XDG_DATA_HOME=/session-data"}
	env := finalizationEnvironment(inherited, "/isolated")
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	if values["OPENCODE_PERMISSION"] != `{"*":"deny"}` || values["XDG_CONFIG_HOME"] != "/isolated" || values["XDG_DATA_HOME"] != "/session-data" || values["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" || values["OPENCODE_PURE"] != "1" {
		t.Fatal(values)
	}
	for key, want := range map[string]string{"OPENCODE_CONFIG_DIR": "/isolated", "OPENCODE_CONFIG": "", "OPENCODE_DISABLE_DEFAULT_PLUGINS": "1", "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1"} {
		if got, ok := values[key]; !ok || got != want {
			t.Fatalf("%s=%q present=%v, want %q", key, got, ok, want)
		}
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(values["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["permission"].(map[string]any)["*"] != "deny" {
		t.Fatal(cfg)
	}
	var expected map[string]any
	if err := json.Unmarshal([]byte(`{"permission":{"*":"deny"},"agent":{"atenea-finalize":{"mode":"primary","permission":{"*":"deny"}}},"default_agent":"atenea-finalize"}`), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("incomplete isolation: %+v", cfg)
	}

	binary := executable(t, `case " $* " in *" --session "*)
 [ "$OPENCODE_PERMISSION" = '{"*":"deny"}' ] || exit 4
 [ "$OPENCODE_DISABLE_PROJECT_CONFIG" = 1 ] || exit 5
 echo '{"type":"text","part":{"type":"text","text":"done"}}'
 echo '{"type":"step_finish","part":{"type":"step-finish","reason":"stop","tokens":{"input":1,"output":1},"cost":0.01}}';;
 *) echo '{"type":"step_finish","sessionID":"isolated","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":1,"output":1},"cost":0.01}}';sleep 5;;esac`)
	runner, err := New(Options{Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(t.Context(), Request{Prompt: "synthetic", BudgetUSD: 1, ReadTokens: 1}); err != nil {
		t.Fatal(err)
	}
}

// TestUnverifiedFinalizationVersionIsRefused checks the regression scenario: unverified finalization version is refused.
func TestUnverifiedFinalizationVersionIsRefused(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "old")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 1.17.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: bin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.finalize(t.Context(), Request{}, "session"); err == nil {
		t.Fatal("unsupported finalization accepted")
	}
}

// TestFinalizationRejectsEmittedTools exercises the emitted-event permission boundary.
func TestFinalizationRejectsEmittedTools(t *testing.T) {
	binary := executable(t, `echo '{"type":"tool_use","part":{"tool":"read"}}'
echo '{"type":"text","part":{"type":"text","text":"done"}}'
echo '{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}'`)
	runner, err := New(Options{Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.finalize(t.Context(), Request{}, "fixture")
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("tool accepted during finalization: %v", err)
	}
}

// TestStableFinalizationVersions rejects partial parses and prerelease versions.
func TestStableFinalizationVersions(t *testing.T) {
	for version, want := range map[string]bool{"1.18.20": true, "v1.18.20": true, "1.19.0": true, "1.18.19": false, "1.18.20-beta": false, "1.18.20.1": false, "1.18.20+meta": false, "2.0.0": false, "1.18": false, "1.18.020": false} {
		if got := stableFinalizationVersion(version); got != want {
			t.Fatalf("%q accepted=%v", version, got)
		}
	}
}
