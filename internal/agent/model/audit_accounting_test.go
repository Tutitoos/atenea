package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditFallbackSubtractsCumulativeTwice checks the regression scenario: audit fallback subtracts cumulative twice.
func TestAuditFallbackSubtractsCumulativeTwice(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "claude")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\necho '{\"type\":\"result\",\"is_error\":true,\"result\":\"overloaded\",\"total_cost_usd\":0.10}'\n"
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Binary: bin, Explore: "first", ExploreFallbacks: []string{"second", "third"}})
	if err != nil {
		t.Fatal(err)
	}
	req := baseRequest()
	req.BudgetUSD = 1
	_, _ = c.Turn(t.Context(), req)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "--max-budget-usd 0.8") {
		t.Fatalf("not reproduced: %s", raw)
	}
}
