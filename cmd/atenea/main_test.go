package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const settings = `
contract = "1.0.0"

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
effects = ["read"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

  [implementation.health]
  state = "alive"

[[implementation]]
id = "serena.search"
provider = "serena"
capability = "code.search"

  [implementation.constraints]
  requires_index = true

[[repository]]
id = "api"
path = "/srv/api"
languages = ["go"]
scale = "small"
`

func settingsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func exec(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, &out)
	return out.String(), err
}

func TestVersionPrintsBothVersions(t *testing.T) {
	out, err := exec(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "contract "+contract.Current.String()) {
		t.Fatalf("out = %q", out)
	}
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	out, err := exec(t)
	if err != nil {
		t.Fatalf("no args: %v", err)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("out = %q", out)
	}
}

func TestStatusShowsEveryProviderWithItsLight(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"code.search", "ripgrep", "serena.search", "AMBER", "repositories", "api"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// The trace is the deliverable, not a debug extra: a decision nobody can
// explain is a decision nobody can trust.
func TestSelectPrintsTheChoiceAndTheFunnel(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "select", "code.search", "--repo", "api")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for _, want := range []string{
		"chosen      ripgrep",
		"2 in -> 1 out: ripgrep",
		"dropped serena.search: needs an index from provider serena",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("select output is missing %q:\n%s", want, out)
		}
	}
}

// With exactly one repository registered, naming it is ceremony.
func TestSelectDefaultsToTheOnlyRepository(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "select", "code.search")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !strings.Contains(out, "repository  api") {
		t.Fatalf("out = %q", out)
	}
}

func TestCatalogShowsTheFourBlocks(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "catalog")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, want := range []string{"capability code.search 1.0.0", "constraints", "cost", "health"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog output is missing %q:\n%s", want, out)
		}
	}
}

func TestConfigInitWritesAUsableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if _, err := exec(t, "--config", path, "config", "init"); err != nil {
		t.Fatalf("config init: %v", err)
	}
	if _, err := exec(t, "--config", path, "config", "init"); err == nil {
		t.Fatal("a second init without --force should fail")
	}
	if _, err := exec(t, "--config", path, "config", "init", "--force"); err != nil {
		t.Fatalf("config init --force: %v", err)
	}
	out, err := exec(t, "--config", path, "status")
	if err != nil {
		t.Fatalf("status on the written file: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Fatalf("status did not read the written file:\n%s", out)
	}
}

func TestConfigPathReportsTheResolvedLocation(t *testing.T) {
	out, err := exec(t, "--config", "/tmp/explicit.toml", "config", "path")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if strings.TrimSpace(out) != "/tmp/explicit.toml" {
		t.Fatalf("out = %q", out)
	}
}

// A shell script has to be able to tell a broken settings file from a provider
// that is simply down, so the bins map onto distinct exit codes.
func TestErrorsMapOntoDistinctExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"unknown command", []string{"explode"}, 2},
		{"select without a capability", []string{"select"}, 2},
		{"unknown capability", []string{"select", "code.impact"}, 3},
		{"unknown config subcommand", []string{"config", "wat"}, 2},
	}
	path := settingsFile(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exec(t, append([]string{"--config", path}, tc.args...)...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := exitCode(err); got != tc.code {
				t.Fatalf("exit code = %d, want %d (err %v)", got, tc.code, err)
			}
		})
	}
}

func TestBrokenSettingsFailLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte("contract = \"1.0.0\"\nnonsense = true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := exec(t, "--config", path, "status")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}
