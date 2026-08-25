package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

// A settings file is a file a person edits, and a person's TOML is arbitrary
// bytes long before it is a valid document.
//
// Load is also the first thing that runs on every command and on every service
// start, so a panic here is not a bad error message: it is a binary that
// cannot start and cannot say why. The contract this pins is narrow and total
// -- Load answers, with a configuration or with a refusal, whatever it is
// handed.
func FuzzLoadNeverPanics(f *testing.F) {
	f.Add("contract = \"3.2.0\"\n")
	f.Add("contract = \"3.2.0\"\n[core]\nshutdown_grace = \"10s\"\n")
	f.Add("[[repository]]\nid = \"x\"\npath = \"/tmp\"\n")
	f.Add("[[capability]]\nid = \"code.search\"\nversion = \"1.0.0\"\n")
	f.Add("[[agent]]\nname = \"x\"\nruns = \"y\"\n")
	f.Add("")
	f.Add("= = =")
	f.Add("contract = 3\n")

	f.Fuzz(func(t *testing.T, body string) {
		// A directory per invocation, not one for the whole target: fuzzing
		// runs ten workers in parallel and a shared path means ten of them
		// writing and reading the same file. Measured before this: throughput
		// went to zero after six seconds.
		path := filepath.Join(t.TempDir(), "atenea.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Skipf("write: %v", err)
		}
		// The return value is not the point and is deliberately unexamined:
		// almost every input here is a refusal, and which refusal is a
		// question the ordinary tests answer. What this asserts is that there
		// is an answer at all.
		_, _ = config.Load(path)
	})
}
