package passthrough_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestAContractCannotDispatchToAReplacementProcess(t *testing.T) {
	for _, version := range []string{"0.20.10", "0.21.23"} {
		t.Run(version, func(t *testing.T) { testContractCannotDispatchToAReplacementProcess(t, version) })
	}
}

func testContractCannotDispatchToAReplacementProcess(t *testing.T, version string) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(root, "actions")
	env := map[string]string{"ATENEA_STDIO_HELPER": "1", "HELPER_SERVER_VERSION": version, "HELPER_CALL_LEDGER": ledger}
	b := passthrough.New(passthrough.Spec{ID: "guarded", Command: []string{self, "-test.run=TestHelperProcess", "-test.v=false"}, Env: env, WorkingDirectory: root, Allowed: []string{"search_code"}})
	t.Cleanup(b.Close)
	atomic := b.(passthrough.ContractBackend)
	catalog, err := atomic.ContractTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != version || catalog.WorkingDirectory != root || len(catalog.Tools) != 1 {
		t.Fatal(catalog)
	}
	b.Close()
	if _, err := atomic.CallContract(t.Context(), "search_code", nil, catalog); contract.CodeOf(err) != "compatibility_unverified" {
		t.Fatalf("retired contract dispatched: %v", err)
	}
	env["HELPER_SERVER_VERSION"] = "0.21.24"
	replacement, err := atomic.ContractTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Version != "0.21.24" {
		t.Fatal("replacement did not change identity")
	}
	if _, err := atomic.CallContract(t.Context(), "search_code", nil, catalog); contract.CodeOf(err) != "compatibility_unverified" {
		t.Fatalf("replacement received old contract action: %v", err)
	}
	if raw, err := os.ReadFile(ledger); err == nil && len(raw) > 0 {
		t.Fatalf("tools/call reached a process: %s", raw)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// A freshly observed contract remains usable; the rejection did not poison
	// the backend or silently send the previous action on a new child.
	if _, err := atomic.CallContract(t.Context(), "search_code", nil, replacement); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(ledger); err != nil || string(raw) != "tools/call\n" {
		t.Fatalf("fresh dispatch ledger=%q err=%v", raw, err)
	}
}
