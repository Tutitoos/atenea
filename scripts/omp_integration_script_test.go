package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOMPIntegrationRejectsAnUnexpectedVersionBeforeGo prevents unpinned local gates.
func TestOMPIntegrationRejectsAnUnexpectedVersionBeforeGo(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "go-ran")
	for name, body := range map[string]string{"omp": "#!/bin/sh\necho omp/99.0.0\n", "go": "#!/bin/sh\ntouch '" + marker + "'\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "bash", "omp-integration-check.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "expected omp/18.0.11, found omp/99.0.0") {
		t.Fatalf("unexpected version accepted: %s %v", out, err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Go test ran after version refusal")
	}
}
