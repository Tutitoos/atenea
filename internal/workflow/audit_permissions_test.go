package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestAuditWorkflowEffectsWidenAtDispatch checks the regression scenario: audit workflow effects widen at dispatch.
func TestAuditWorkflowEffectsWidenAtDispatch(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "assignment.json")
	bin := filepath.Join(dir, "agent")
	body := "#!/bin/sh\ncat > '" + dest + "'\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'\n"
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, noCeiling(), declared("writer", bin, config.PoolAgent, contract.EffectRead, contract.EffectWrite))
	_, err := h.engine.Start(t.Context(), graphOf(step("read", "writer", nil, contract.EffectRead)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var assignment map[string]any
	if err := json.Unmarshal(raw, &assignment); err != nil {
		t.Fatal(err)
	}
	effects, ok := assignment["effects"].([]any)
	if !ok || len(effects) != 1 || effects[0] != "read" {
		t.Fatalf("dispatch must grant exactly read: %s", raw)
	}
}

// TestAuditDuplicateFileSelfConflict checks the regression scenario: audit duplicate file self conflict.
func TestAuditDuplicateFileSelfConflict(t *testing.T) {
	_, err := compile(t, graphOf(withFiles(step("edit", "writer", nil, contract.EffectRead, contract.EffectWrite), "a.go", "./a.go")))
	if err != nil {
		t.Fatal(err)
	}
}
