package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
)

// runClaudeMCPCommand is a narrow seam for the Claude CLI. Production uses
// exec.Command; tests replace it to exercise scope and rollback behavior
// without invoking a real client.
var runClaudeMCPCommand = func(binary string, args ...string) ([]byte, error) {
	return exec.Command(binary, args...).CombinedOutput()
}

type claudeMCPEntry struct {
	Scope       string
	Path        string
	Raw         map[string]any
	Managed     bool
	Matches     bool
	Fingerprint string
}

type claudeMCPInspection struct {
	Found          bool
	Unknown        bool
	EffectiveScope string
	Scopes         []string
	Entries        map[string]claudeMCPEntry
	Managed        bool
	Matches        bool
	AllMatches     bool
	ScopeMismatch  bool
	Fingerprint    string
}

type claudeFileSnapshot struct {
	Path   string
	Exists bool
	Data   []byte
	Mode   os.FileMode
}

var claudeScopeOrder = []string{"local", "project", "user"}

func claudeProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return ""
	}
	cwd = filepath.Clean(cwd)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if info, statErr := os.Stat(filepath.Join(dir, ".mcp.json")); statErr == nil && !info.IsDir() {
			return dir
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return cwd
}

func claudeProjectPathAliases(root string) []string {
	if root == "" {
		return nil
	}
	paths := make([]string, 0, 2)
	appendPath := func(path string) {
		path = filepath.Clean(path)
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	appendPath(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		appendPath(resolved)
	}
	return paths
}

func readClaudeJSONDocument(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, true, fmt.Errorf("parse Claude MCP config %s: %w", path, err)
	}
	return document, true, nil
}

func claudeMCPServers(document map[string]any, path string) (map[string]any, bool, error) {
	raw, ok := document["mcpServers"]
	if !ok {
		return nil, false, nil
	}
	servers, ok := raw.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("claude MCP config %s has invalid mcpServers", path)
	}
	return servers, true, nil
}

func claudeMCPEntryFromServers(scope, path string, servers map[string]any) (claudeMCPEntry, bool, error) {
	raw, ok := servers["atenea"]
	if !ok {
		return claudeMCPEntry{}, false, nil
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		return claudeMCPEntry{}, true, fmt.Errorf("claude MCP entry atenea in %s is invalid", path)
	}
	return claudeMCPEntry{Scope: scope, Path: path, Raw: entry}, true, nil
}

func claudeMCPEntries(root string) (map[string]claudeMCPEntry, error) {
	entries := make(map[string]claudeMCPEntry)
	userPath := claudeUserConfigPath()
	if userPath != "" {
		document, exists, err := readClaudeJSONDocument(userPath)
		if err != nil {
			return nil, err
		}
		if exists {
			servers, hasServers, err := claudeMCPServers(document, userPath)
			if err != nil {
				return nil, err
			}
			if hasServers {
				entry, found, err := claudeMCPEntryFromServers("user", userPath, servers)
				if err != nil {
					return nil, err
				}
				if found {
					entries["user"] = entry
				}
			}

			projectsRaw, hasProjects := document["projects"]
			if hasProjects {
				projects, ok := projectsRaw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("claude MCP config %s has invalid projects", userPath)
				}
				for _, projectPath := range claudeProjectPathAliases(root) {
					projectRaw, ok := projects[projectPath]
					if !ok {
						continue
					}
					project, ok := projectRaw.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("claude project entry %s is invalid", projectPath)
					}
					servers, hasServers, err := claudeMCPServers(project, userPath)
					if err != nil {
						return nil, err
					}
					if !hasServers {
						continue
					}
					entry, found, err := claudeMCPEntryFromServers("local", userPath, servers)
					if err != nil {
						return nil, err
					}
					if found {
						entries["local"] = entry
						break
					}
				}
			}
		}
	}

	if root != "" {
		projectPath := filepath.Join(root, ".mcp.json")
		document, exists, err := readClaudeJSONDocument(projectPath)
		if err != nil {
			return nil, err
		}
		if exists {
			servers, hasServers, err := claudeMCPServers(document, projectPath)
			if err != nil {
				return nil, err
			}
			if hasServers {
				entry, found, err := claudeMCPEntryFromServers("project", projectPath, servers)
				if err != nil {
					return nil, err
				}
				if found {
					entries["project"] = entry
				}
			}
		}
	}
	return entries, nil
}

func claudeMCPFingerprint(raw map[string]any) (string, error) {
	contract := make(map[string]any, len(raw))
	for key, value := range raw {
		contract[key] = value
	}
	if transport, ok := contract["type"].(string); !ok || transport == "" || strings.EqualFold(transport, "stdio") {
		delete(contract, "type")
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func claudeExpectedMCP(self string, profile config.DesktopProfile) map[string]any {
	return map[string]any{
		"command": self,
		"args":    []string{"mcp", "--desktop-profile", profile.Name},
	}
}

func claudeMCPArgs(raw map[string]any) []string {
	values, ok := raw["args"].([]any)
	if !ok {
		return nil
	}
	args := make([]string, 0, len(values))
	for _, value := range values {
		if arg, ok := value.(string); ok {
			args = append(args, arg)
		}
	}
	return args
}

func claudeMCPLooksManaged(raw map[string]any, self string) bool {
	command, _ := raw["command"].(string)
	base := strings.ToLower(filepath.Base(command))
	commandMatches := self != "" && command == self
	if !commandMatches {
		commandMatches = base == "atenea" || strings.Contains(base, "atenea")
	}
	if !commandMatches {
		return false
	}
	args := claudeMCPArgs(raw)
	hasMCP := false
	hasProfile := false
	for i, arg := range args {
		if arg == "mcp" {
			hasMCP = true
		}
		if arg == "--desktop-profile" && i+1 < len(args) && args[i+1] != "" {
			hasProfile = true
		}
	}
	return hasMCP && hasProfile
}

func inspectClaudeMCPFor(binary, self string, profile config.DesktopProfile) (claudeMCPInspection, error) {
	root := claudeProjectRoot()
	entries, err := claudeMCPEntries(root)
	if err != nil {
		return claudeMCPInspection{}, err
	}
	_, clientReportsEntry := claudeMCPGet(binary)
	inspection := claudeMCPInspection{
		Entries:     entries,
		Fingerprint: "",
	}
	expected, err := claudeMCPFingerprint(claudeExpectedMCP(self, profile))
	if err != nil {
		return claudeMCPInspection{}, err
	}
	inspection.Fingerprint = expected
	if len(entries) == 0 {
		inspection.Found = clientReportsEntry
		inspection.Unknown = clientReportsEntry
		if clientReportsEntry {
			inspection.EffectiveScope = "unknown"
		}
		return inspection, nil
	}

	inspection.Found = true
	inspection.Managed = true
	inspection.AllMatches = true
	for _, scope := range claudeScopeOrder {
		entry, ok := entries[scope]
		if !ok {
			continue
		}
		if inspection.EffectiveScope == "" {
			inspection.EffectiveScope = scope
		}
		entry.Managed = claudeMCPLooksManaged(entry.Raw, self)
		entryFingerprint, fingerprintErr := claudeMCPFingerprint(entry.Raw)
		if fingerprintErr != nil {
			return claudeMCPInspection{}, fingerprintErr
		}
		entry.Fingerprint = entryFingerprint
		entry.Matches = entryFingerprint == expected
		entries[scope] = entry
		inspection.Scopes = append(inspection.Scopes, scope)
		inspection.Managed = inspection.Managed && entry.Managed
		inspection.AllMatches = inspection.AllMatches && entry.Matches
		if entry.Matches && scope != "user" {
			inspection.ScopeMismatch = true
		}
	}
	inspection.Entries = entries
	inspection.Matches = len(entries) == 1 && entries["user"].Matches
	if len(entries) > 1 {
		inspection.ScopeMismatch = inspection.AllMatches
	}
	return inspection, nil
}

func inspectClaudeMCP(profile config.DesktopProfile) (claudeMCPInspection, error) {
	binary, err := osExecutableClaude()
	if err != nil {
		return claudeMCPInspection{}, err
	}
	self, _ := os.Executable()
	return inspectClaudeMCPFor(binary, self, profile)
}

func osExecutableClaude() (string, error) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	return binary, nil
}

func claudeSnapshotFiles(root string) ([]claudeFileSnapshot, error) {
	paths := []string{claudeUserConfigPath()}
	if root != "" {
		paths = append(paths, filepath.Join(root, ".mcp.json"))
	}
	snapshots := make([]claudeFileSnapshot, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		snapshot := claudeFileSnapshot{Path: path, Mode: 0o600}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			snapshots = append(snapshots, snapshot)
			continue
		}
		if err != nil {
			return nil, err
		}
		snapshot.Exists = true
		snapshot.Mode = info.Mode().Perm()
		snapshot.Data, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func claudeBackupSnapshots(snapshots []claudeFileSnapshot, scopes []string) error {
	paths := make(map[string]struct{})
	for _, scope := range scopes {
		switch scope {
		case "local", "user":
			paths[claudeUserConfigPath()] = struct{}{}
		case "project":
			for _, snapshot := range snapshots {
				if strings.HasSuffix(snapshot.Path, string(filepath.Separator)+".mcp.json") {
					paths[snapshot.Path] = struct{}{}
				}
			}
		}
	}
	for _, snapshot := range snapshots {
		if !snapshot.Exists {
			continue
		}
		if _, ok := paths[snapshot.Path]; !ok {
			continue
		}
		if err := os.WriteFile(backupPath(snapshot.Path), snapshot.Data, snapshot.Mode); err != nil {
			return err
		}
	}
	return nil
}

func restoreClaudeSnapshots(snapshots []claudeFileSnapshot) error {
	for _, snapshot := range snapshots {
		if !snapshot.Exists {
			if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := atomicDesktopWrite(snapshot.Path, snapshot.Data, snapshot.Mode); err != nil {
			return err
		}
	}
	return nil
}

func claudeScopePresent(inspection claudeMCPInspection, scope string) bool {
	_, ok := inspection.Entries[scope]
	return ok
}

func claudeMCPRemoveAtScope(binary, scope string) error {
	if _, err := runClaudeMCPCommand(binary, "mcp", "remove", "--scope", scope, "atenea"); err != nil {
		return fmt.Errorf("claude MCP remove (%s): %v", scope, err)
	}
	return nil
}

func claudeMCPAddAtUser(binary, self string, profile config.DesktopProfile) error {
	args := []string{"mcp", "add", "--scope", "user", "atenea", "--", self, "mcp", "--desktop-profile", profile.Name}
	if _, err := runClaudeMCPCommand(binary, args...); err != nil {
		return fmt.Errorf("claude MCP install: %v", err)
	}
	return nil
}

func installClaudeMCPWithProject(self string, profile config.DesktopProfile, replace, replaceProject bool) error {
	binary, err := osExecutableClaude()
	if err != nil {
		return err
	}
	inspection, err := inspectClaudeMCPFor(binary, self, profile)
	if err != nil {
		return err
	}
	if inspection.Matches {
		return nil
	}
	if inspection.Unknown {
		return errors.New("el MCP atenea de Claude no pertenece a un scope gestionable")
	}
	if claudeScopePresent(inspection, "project") && !replaceProject {
		return errors.New("el MCP atenea esta en scope project; usa --replace --replace-project para adoptarlo")
	}
	if inspection.Found && !replace {
		return errors.New("el MCP atenea de Claude ya existe y difiere; usa --replace")
	}

	root := claudeProjectRoot()
	snapshots, err := claudeSnapshotFiles(root)
	if err != nil {
		return err
	}
	removeScopes := make([]string, 0, len(claudeScopeOrder))
	for _, scope := range claudeScopeOrder {
		if !claudeScopePresent(inspection, scope) {
			continue
		}
		if scope == "project" && !replaceProject {
			return errors.New("el MCP atenea esta en scope project; usa --replace --replace-project para adoptarlo")
		}
		removeScopes = append(removeScopes, scope)
	}
	if err := claudeBackupSnapshots(snapshots, append(removeScopes, "user")); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if restoreErr := restoreClaudeSnapshots(snapshots); restoreErr != nil {
			return fmt.Errorf("%v; rollback Claude tambien fallo: %v", cause, restoreErr)
		}
		return cause
	}
	for _, scope := range removeScopes {
		if err := claudeMCPRemoveAtScope(binary, scope); err != nil {
			return rollback(err)
		}
	}
	postRemoval, err := inspectClaudeMCPFor(binary, self, profile)
	if err != nil {
		return rollback(err)
	}
	if postRemoval.Found {
		return rollback(errors.New("claude conserva una entrada atenea despues de remove"))
	}
	if err := claudeMCPAddAtUser(binary, self, profile); err != nil {
		return rollback(err)
	}
	installed, err := inspectClaudeMCPFor(binary, self, profile)
	if err != nil {
		return rollback(err)
	}
	if !installed.Matches || installed.Unknown || len(installed.Entries) != 1 || !claudeScopePresent(installed, "user") {
		return rollback(errors.New("claude no dejo exactamente una entrada atenea en scope user"))
	}
	return nil
}

func removeManagedClaudeMCP() error {
	binary, err := osExecutableClaude()
	if err != nil {
		return err
	}
	self, _ := os.Executable()
	inspection, err := inspectClaudeMCPFor(binary, self, config.DesktopProfile{})
	if err != nil {
		return err
	}
	if !inspection.Found {
		return nil
	}
	if inspection.Unknown {
		return errors.New("el MCP atenea de Claude no pertenece a un scope gestionable")
	}
	entry, exists := inspection.Entries["user"]
	if !exists {
		return nil
	}
	if !entry.Managed {
		return errors.New("el MCP atenea de Claude en user no esta gestionado por Atenea")
	}
	snapshots, err := claudeSnapshotFiles(claudeProjectRoot())
	if err != nil {
		return err
	}
	if err := claudeBackupSnapshots(snapshots, []string{"user"}); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if restoreErr := restoreClaudeSnapshots(snapshots); restoreErr != nil {
			return fmt.Errorf("%v; rollback Claude tambien fallo: %v", cause, restoreErr)
		}
		return cause
	}
	if err := claudeMCPRemoveAtScope(binary, "user"); err != nil {
		return rollback(err)
	}
	postRemoval, err := inspectClaudeMCPFor(binary, self, config.DesktopProfile{})
	if err != nil {
		return rollback(err)
	}
	if claudeScopePresent(postRemoval, "user") {
		return rollback(errors.New("claude conserva la entrada atenea user despues de remove"))
	}
	return nil
}

func claudeMCPState(profile config.DesktopProfile) (string, claudeMCPInspection) {
	inspection, err := inspectClaudeMCP(profile)
	if err != nil {
		return "managed_drift", claudeMCPInspection{}
	}
	return claudeMCPStateFromInspection(inspection), inspection
}

func claudeMCPStateFromInspection(inspection claudeMCPInspection) string {
	if !inspection.Found {
		return "missing"
	}
	if inspection.Unknown || !inspection.Managed {
		return "unmanaged_collision"
	}
	if inspection.Matches {
		return "managed_match"
	}
	if inspection.AllMatches || inspection.ScopeMismatch {
		return "scope_mismatch"
	}
	return "managed_drift"
}
