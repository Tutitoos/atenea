package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	var cfg map[string]any
	if err := json.Unmarshal([]byte(values["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["permission"].(map[string]any)["*"] != "deny" {
		t.Fatal(cfg)
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
