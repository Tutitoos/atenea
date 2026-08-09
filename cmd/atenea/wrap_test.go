package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// wrapSettings writes a settings file declaring one server at addr.
func wrapSettings(t *testing.T, addr string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.toml")
	body := settings + "\n[metrics]\npath = \"" + filepath.Join(dir, "base.duckdb") + "\"\n" +
		"\n[[mcp_server]]\nid = \"declared\"\nurl = \"" + addr + "\"\ntimeout = \"1s\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestWrapWithoutAClientSaysWhatToPass(t *testing.T) {
	out, err := cli(t, "wrap")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "opencode") {
		t.Errorf("err = %v, want an example of what to pass; got: %s", err, out)
	}
}

// The refusal has to list what would have worked. A wrapper that only says
// "no" leaves the reader to find the answer in a help page they have already
// decided not to open.
func TestWrapNamesTheClientsItSupports(t *testing.T) {
	_, err := cli(t, "wrap", "emacs")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	// Read from the table rather than pinned to a literal. A client added
	// to the table whose name never reaches this message is a client
	// nobody discovers, and the list was a single name for long enough
	// that the literal looked like the contract.
	for name := range clients {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("err = %v, want %q named as supported", err, name)
		}
	}
}

// Ordering, pinned: the binary is resolved before anything is probed.
//
// The other order is the one that looks harmless and is not -- eleven
// handshakes, a full report, and only then "opencode: not found", which is
// the answer that made the whole report pointless. The probes are also the
// slow part, so getting this backwards is exactly as expensive as it looks.
func TestWrapChecksTheBinaryBeforeItProbesAnything(t *testing.T) {
	// A PATH with nothing on it: the client cannot be found, and the
	// declared server's address is one nothing is listening on.
	t.Setenv("PATH", t.TempDir())
	path := wrapSettings(t, "http://127.0.0.1:1/mcp")

	// Straight at cmdWrap with its own buffer. The report is written to
	// stderr in the real dispatch -- stdout belongs to the client wrap
	// execs -- so asking the CLI's stdout whether a report appeared would
	// pass no matter what this function did.
	var report bytes.Buffer
	err := cmdWrap(path, []string{"opencode"}, &report)
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found: %v", contract.KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("err = %v, want it to name PATH as the thing that failed", err)
	}
	if got := report.String(); strings.Contains(got, "declared") || strings.Contains(got, "refused") {
		t.Errorf("a report was printed before the binary was resolved:\n%s", got)
	}
}

// A settings file that declares a server nothing can reach is not an error:
// naming it is the entire feature. The load must not fail, and the block
// must survive into the config the command reads.
func TestADeclaredServerSurvivesIntoTheSettings(t *testing.T) {
	path := wrapSettings(t, "http://127.0.0.1:1/mcp")
	out, err := cli(t, "--config", path, "config", "path")
	if err != nil {
		t.Fatalf("a declared mcp_server made the settings unreadable: %v", err)
	}
	if strings.TrimSpace(out) != path {
		t.Errorf("path = %q, want %q", strings.TrimSpace(out), path)
	}
}
