package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These CLI validation tests need only the neutral capability catalog.
func askCatalog(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "config", "default.toml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATENEA_CONFIG", "")
	return path
}

// The payload is typed by the capability's own declaration, so a line number
// that is not a number is refused at the door rather than sent as a string for
// the far side to trip over.
func TestTheAskPayloadIsTypedByTheCapability(t *testing.T) {
	settingsPath := askCatalog(t)

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"not a number", []string{"--set", "file=a.go", "--set", "line=soon", "--set", "column=1"}, 2},
		{"unknown field", []string{"--set", "file=a.go", "--set", "color=blue"}, 2},
		{"not name=value", []string{"--set", "file"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--config", settingsPath, "ask", "symbol.definition", "--repo", "current"}, tc.args...)
			out, err := cli(t, args...)
			if err == nil {
				t.Fatalf("accepted a bad payload:\n%s", out)
			}
			if got := exitCode(err); got != tc.want {
				t.Errorf("exit code = %d, want %d (err %v)", got, tc.want, err)
			}
		})
	}
}

// A capability nobody declared is a not_found, not a crash and not a silent
// empty answer.
func TestAskingForACapabilityNobodyDeclaredIsNotFound(t *testing.T) {
	settingsPath := askCatalog(t)

	_, err := cli(t, "--config", settingsPath, "ask", "symbol.invented", "--repo", "current",
		"--set", "file=a.go")
	if err == nil {
		t.Fatal("an unknown capability was accepted")
	}
	if got := exitCode(err); got != 3 {
		t.Errorf("exit code = %d, want 3 (err %v)", got, err)
	}
}

func TestAskWorkspaceContextKeepsTwoExplicitRepositoriesInJSON(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	body := strings.ReplaceAll(settings, "code.search", "code.context")
	body = strings.Replace(body, `path = "/srv/api"`, fmt.Sprintf("path = %q", first), 1)
	body += fmt.Sprintf("\n[[repository]]\nid = \"web\"\npath = %q\nlanguages = [\"go\"]\nscale = \"small\"\n", second)
	body += "\n[orchestrator]\nrunners = []\n"
	settingsPath := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(payloadPath, []byte(`{"query":"TODO"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := cli(t, "--config", settingsPath, "ask", "workspace.context", "--repo", "api", "--repo", "web", "--payload", payloadPath, "--json")
	if err != nil {
		t.Fatalf("workspace.context CLI: %v\n%s", err, out)
	}
	var decoded struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("workspace.context output is not JSON: %v\n%s", err, out)
	}
	if len(decoded.Repositories) != 2 || decoded.Repositories[0].ID != "api" || decoded.Repositories[1].ID != "web" {
		t.Fatalf("workspace.context rows = %+v, want api then web", decoded.Repositories)
	}
}
