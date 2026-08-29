package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func testClaudeExecutable(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testClaudeJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func writeTestClaudeJSON(t *testing.T, path string, document map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testClaudeEntry(t *testing.T, command, profile string) map[string]any {
	t.Helper()
	return map[string]any{
		"command": command,
		"args":    []string{"mcp", "--desktop-profile", profile},
	}
}

func setTestClaudeEntry(t *testing.T, root, scope string, entry map[string]any) {
	t.Helper()
	switch scope {
	case "user":
		path := claudeUserConfigPath()
		document := testClaudeJSON(t, path)
		document["mcpServers"] = map[string]any{"atenea": entry}
		writeTestClaudeJSON(t, path, document)
	case "local":
		path := claudeUserConfigPath()
		document := testClaudeJSON(t, path)
		projects, _ := document["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
		}
		projects[root] = map[string]any{"mcpServers": map[string]any{"atenea": entry}}
		document["projects"] = projects
		writeTestClaudeJSON(t, path, document)
	case "project":
		path := filepath.Join(root, ".mcp.json")
		document := testClaudeJSON(t, path)
		document["mcpServers"] = map[string]any{"atenea": entry}
		writeTestClaudeJSON(t, path, document)
	default:
		t.Fatalf("unknown test Claude scope %q", scope)
	}
}

func removeTestClaudeEntry(t *testing.T, root, scope string) {
	t.Helper()
	switch scope {
	case "user":
		path := claudeUserConfigPath()
		document := testClaudeJSON(t, path)
		if servers, ok := document["mcpServers"].(map[string]any); ok {
			delete(servers, "atenea")
		}
		writeTestClaudeJSON(t, path, document)
	case "local":
		path := claudeUserConfigPath()
		document := testClaudeJSON(t, path)
		if projects, ok := document["projects"].(map[string]any); ok {
			if project, ok := projects[root].(map[string]any); ok {
				if servers, ok := project["mcpServers"].(map[string]any); ok {
					delete(servers, "atenea")
				}
			}
		}
		writeTestClaudeJSON(t, path, document)
	case "project":
		path := filepath.Join(root, ".mcp.json")
		document := testClaudeJSON(t, path)
		if servers, ok := document["mcpServers"].(map[string]any); ok {
			delete(servers, "atenea")
		}
		writeTestClaudeJSON(t, path, document)
	}
}

func testClaudeRunner(t *testing.T, root string, failAdd bool) *[]string {
	t.Helper()
	var calls []string
	previous := runClaudeMCPCommand
	runClaudeMCPCommand = func(binary string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) < 2 || args[0] != "mcp" {
			return nil, errors.New("unexpected Claude command")
		}
		switch args[1] {
		case "get":
			entries, err := claudeMCPEntries(root)
			if err != nil {
				return nil, err
			}
			if len(entries) == 0 {
				return nil, errors.New("not found")
			}
			return []byte("configured"), nil
		case "remove":
			if len(args) != 5 || args[2] != "--scope" {
				return nil, errors.New("invalid remove arguments")
			}
			removeTestClaudeEntry(t, root, args[3])
			return nil, nil
		case "add":
			if failAdd {
				return nil, errors.New("fake add failure")
			}
			if len(args) != 10 || args[2] != "--scope" || args[3] != "user" || args[4] != "atenea" || args[5] != "--" {
				return nil, errors.New("invalid add arguments")
			}
			setTestClaudeEntry(t, root, "user", testClaudeEntry(t, args[6], args[9]))
			return nil, nil
		default:
			return nil, errors.New("unexpected Claude MCP subcommand")
		}
	}
	t.Cleanup(func() { runClaudeMCPCommand = previous })
	return &calls
}

func TestClaudeScopeInspectionClassifiesAllScopes(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(root)
	testClaudeExecutable(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	profile := config.DesktopProfile{Name: "claude", Fallback: "diagnostic"}
	testClaudeRunner(t, root, false)

	tests := []struct {
		name  string
		scope string
		entry map[string]any
		want  string
	}{
		{name: "user match", scope: "user", entry: testClaudeEntry(t, self, "claude"), want: "managed_match"},
		{name: "local match", scope: "local", entry: testClaudeEntry(t, self, "claude"), want: "scope_mismatch"},
		{name: "project match", scope: "project", entry: testClaudeEntry(t, self, "claude"), want: "scope_mismatch"},
		{name: "user drift", scope: "user", entry: testClaudeEntry(t, self, "shared"), want: "managed_drift"},
		{name: "foreign", scope: "user", entry: map[string]any{"command": "foreign", "args": []string{}}, want: "unmanaged_collision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, scope := range []string{"local", "project", "user"} {
				removeTestClaudeEntry(t, root, scope)
			}
			setTestClaudeEntry(t, root, test.scope, test.entry)
			inspection, err := inspectClaudeMCPFor("claude", self, profile)
			if err != nil {
				t.Fatal(err)
			}
			if got := claudeMCPStateFromInspection(inspection); got != test.want {
				t.Fatalf("state = %q, want %q; inspection=%#v", got, test.want, inspection)
			}
		})
	}
}

func TestClaudeReplaceRehomesLocalAndLeavesProjectProtected(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(root)
	testClaudeExecutable(t)
	self := "/tmp/atenea-candidate"
	profile := config.DesktopProfile{Name: "claude", Fallback: "diagnostic"}
	calls := testClaudeRunner(t, root, false)
	setTestClaudeEntry(t, root, "local", testClaudeEntry(t, self, "claude"))
	if err := installClaudeMCPWithProject(self, profile, true, false); err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectClaudeMCPFor("claude", self, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Matches || len(inspection.Entries) != 1 || !claudeScopePresent(inspection, "user") {
		t.Fatalf("unexpected rehomed inspection: %#v", inspection)
	}
	if len(*calls) != 6 || !strings.Contains((*calls)[0], "mcp get") || !strings.Contains((*calls)[1], "remove --scope local") || !strings.Contains((*calls)[2], "mcp get") || !strings.Contains((*calls)[3], "mcp add") || !strings.Contains((*calls)[4], "mcp get") {
		t.Fatalf("unexpected Claude command sequence: %#v", *calls)
	}
	for _, call := range *calls {
		if strings.Contains(call, " list") {
			t.Fatalf("Claude list command was invoked: %q", call)
		}
	}

	for _, scope := range []string{"local", "project", "user"} {
		removeTestClaudeEntry(t, root, scope)
	}
	setTestClaudeEntry(t, root, "project", testClaudeEntry(t, self, "claude"))
	original, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := installClaudeMCPWithProject(self, profile, true, false); err == nil || !strings.Contains(err.Error(), "replace-project") {
		t.Fatalf("project collision error = %v", err)
	}
	unchanged, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if string(unchanged) != string(original) {
		t.Fatal("project config changed without --replace-project")
	}
	if err := installClaudeMCPWithProject(self, profile, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeReplaceRollsBackAllSnapshotsWhenAddFails(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(root)
	testClaudeExecutable(t)
	self := "/tmp/atenea-candidate"
	profile := config.DesktopProfile{Name: "claude", Fallback: "diagnostic"}
	testClaudeRunner(t, root, true)
	setTestClaudeEntry(t, root, "local", testClaudeEntry(t, self, "claude"))
	setTestClaudeEntry(t, root, "project", map[string]any{"command": "foreign", "args": []string{}})
	userPath := claudeUserConfigPath()
	projectPath := filepath.Join(root, ".mcp.json")
	originalUser, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	originalProject, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := installClaudeMCPWithProject(self, profile, true, true); err == nil {
		t.Fatal("failed add was accepted")
	}
	currentUser, _ := os.ReadFile(userPath)
	currentProject, _ := os.ReadFile(projectPath)
	if string(currentUser) != string(originalUser) || string(currentProject) != string(originalProject) {
		t.Fatal("Claude snapshots were not restored byte-for-byte")
	}
}

func TestClaudeRemoveOnlyManagedUserEntry(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(root)
	testClaudeExecutable(t)
	self := "/tmp/atenea-candidate"
	testClaudeRunner(t, root, false)
	setTestClaudeEntry(t, root, "local", map[string]any{"command": "foreign", "args": []string{}})
	setTestClaudeEntry(t, root, "user", testClaudeEntry(t, self, "claude"))
	if err := removeManagedClaudeMCP(); err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectClaudeMCPFor("claude", self, config.DesktopProfile{Name: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if claudeScopePresent(inspection, "user") == true || !claudeScopePresent(inspection, "local") {
		t.Fatalf("remove affected wrong scope: %#v", inspection)
	}
	if err := removeManagedClaudeMCP(); err != nil {
		t.Fatal("idempotent remove failed:", err)
	}

	for _, scope := range []string{"local", "project", "user"} {
		removeTestClaudeEntry(t, root, scope)
	}
	setTestClaudeEntry(t, root, "user", map[string]any{"command": "foreign", "args": []string{}})
	if err := removeManagedClaudeMCP(); err == nil {
		t.Fatal("foreign user entry was removed")
	}
}
