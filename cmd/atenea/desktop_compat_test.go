package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
)

func testDesktopProfile() config.DesktopProfile {
	return config.DesktopProfile{
		Name: "chatgpt", MCPMode: "atenea_only", Fallback: "diagnostic",
		StartupTimeout: 10 * time.Second, ToolTimeout: time.Minute,
	}
}

func TestDesktopConfigRenderingAndSemanticTableReplacement(t *testing.T) {
	block := renderChatGPTMCP("/tmp/atenea", testDesktopProfile())
	if !strings.Contains(block, `[mcp_servers.atenea]`) || !strings.Contains(block, `"--desktop-profile", "chatgpt"`) {
		t.Fatalf("unexpected rendered MCP block: %s", block)
	}
	old := `[mcp_servers.atenea]
command = "old"
[mcp_servers.atenea.env]
TOKEN = "must-preserve-only-outside"
[mcp_servers.other]
command = "other"
`
	updated, ok := removeMCPTable(old)
	if !ok || strings.Contains(updated, "mcp_servers.atenea") || !strings.Contains(updated, "mcp_servers.other") {
		t.Fatalf("semantic removal changed the wrong tables: %q", updated)
	}
	if err := validateTOML(updated + block); err != nil {
		t.Fatalf("rendered TOML is invalid: %v", err)
	}
	start, finish, ok := managedBlockSpan(block)
	if !ok || start != 0 || finish != len(block) {
		t.Fatalf("managed span = %d:%d ok=%v", start, finish, ok)
	}
}

func TestDesktopConfigWriteIsAtomicAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	old := []byte("old\n")
	updated := []byte("new\n")
	if err := writeDesktopConfig(path, nil, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDesktopConfig(path, old, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".atenea-") {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("backups = %d, want one for the changed write", backups)
	}
	if err := writeDesktopConfig(path, updated, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatalf("idempotent write created extra files: %#v", entries)
	}
}

func TestDesktopClientResolverDetectsFlagsAndCachesCapabilities(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-client")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo fake-1.2; else echo 'usage --strict-mcp-config'; fi\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	resolved := resolveDesktopClient("fake-client", []string{"--strict-mcp-config", "--missing-flag"})
	if resolved.Source != "path" || resolved.Version != "fake-1.2" {
		t.Fatalf("unexpected client resolution: %#v", resolved)
	}
	if len(resolved.SupportedFlags) != 1 || resolved.SupportedFlags[0] != "--strict-mcp-config" {
		t.Fatalf("supported flags = %#v", resolved.SupportedFlags)
	}
	if len(resolved.OmittedFlags) != 1 || resolved.OmittedFlags[0] != "--missing-flag" {
		t.Fatalf("omitted flags = %#v", resolved.OmittedFlags)
	}
	second := resolveDesktopClient("fake-client", []string{"--strict-mcp-config", "--missing-flag"})
	if second.Path != resolved.Path || second.BinaryMTime != resolved.BinaryMTime {
		t.Fatalf("cached resolution changed: %#v vs %#v", resolved, second)
	}
}

func TestInstalledChatGPTStateDistinguishesManagedAndCollision(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	profile := testDesktopProfile()
	if got := installedChatGPTState(profile); got != "missing" {
		t.Fatalf("missing state = %q", got)
	}
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(renderChatGPTMCP("/tmp/atenea", profile)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedChatGPTState(profile); got != "managed_match" {
		t.Fatalf("managed state = %q", got)
	}
	if err := os.WriteFile(path, []byte("[mcp_servers.atenea]\ncommand=\"other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedChatGPTState(profile); got != "unmanaged_collision" {
		t.Fatalf("collision state = %q", got)
	}
}

func TestDesktopParsersRejectMalformedInputAndHandleMissingClients(t *testing.T) {
	if _, err := parseWrapOptions([]string{"--client"}); err == nil {
		t.Fatal("missing wrap client value was accepted")
	}
	if _, _, err := peelDesktopProfile([]string{"--desktop-profile"}); err == nil {
		t.Fatal("missing MCP profile value was accepted")
	}
	if _, err := parseWrapOptions([]string{"claude", "--", "mcp", "list"}); err != nil {
		t.Fatal(err)
	}
	if got := commandVersion(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("missing client version = %q", got)
	}
	if resolved := resolveDesktopClient("missing-client", nil); resolved.Path != "" || resolved.Source != "" {
		t.Fatalf("missing client resolution = %#v", resolved)
	}
	if err := validateTOML("[broken"); err == nil {
		t.Fatal("invalid TOML was accepted")
	}
	if _, _, ok := managedBlockSpan("# BEGIN ATENEA MANAGED MCP\n"); ok {
		t.Fatal("incomplete managed block was accepted")
	}
	if _, _, ok := mcpTableSpan("[mcp_servers.other]\ncommand=\"x\"\n", "[mcp_servers.atenea]"); ok {
		t.Fatal("missing Atenea table was found")
	}
}

func TestMCPDoctorResponseParserRejectsBadResponses(t *testing.T) {
	if _, err := readMCPResponse(bufio.NewReader(strings.NewReader("not json\n"))); err == nil {
		t.Fatal("invalid response was accepted")
	}
	if _, err := readMCPResponse(bufio.NewReader(strings.NewReader(`{"jsonrpc":"2.0","error":{"code":-1}}` + "\n"))); err == nil {
		t.Fatal("MCP error response was accepted")
	}
	if _, err := readMCPResponse(bufio.NewReader(strings.NewReader(""))); err == nil {
		t.Fatal("empty response was accepted")
	}
}

func TestTOMLQuoteEscapesBasicStringCharacters(t *testing.T) {
	quoted := tomlQuote("a\\b\"c\nd")
	if quoted != `"a\\b\"c\nd"` {
		t.Fatalf("quoted = %q", quoted)
	}
}

func writeFakeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChatGPTInstallAdoptsCollisionsAndRemovesOnlyManagedBlock(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFakeExecutable(t, bin, "codex", "exit 0\n")
	path := filepath.Join(root, "config.toml")
	foreign := "[mcp_servers.other]\ncommand = \"other\"\n[mcp_servers.atenea]\ncommand = \"foreign\"\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := testDesktopProfile()
	if err := installChatGPTMCP("/tmp/atenea", profile, false); err == nil {
		t.Fatal("unmanaged collision was silently replaced")
	}
	if err := installChatGPTMCP("/tmp/atenea", profile, true); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "[mcp_servers.atenea]") != 1 || !strings.Contains(string(content), "mcp_servers.other") {
		t.Fatalf("adopted config lost tables or duplicated Atenea: %s", content)
	}
	entries, _ := os.ReadDir(root)
	backups := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".atenea-") {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("backup count = %d, want one", backups)
	}
	if err := installChatGPTMCP("/tmp/atenea", profile, false); err != nil {
		t.Fatal("idempotent install failed:", err)
	}
	if err := removeChatGPTMCP(); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if strings.Contains(string(content), "mcp_servers.atenea") || !strings.Contains(string(content), "mcp_servers.other") {
		t.Fatalf("remove changed the wrong table: %s", content)
	}
	if err := removeChatGPTMCP(); err != nil {
		t.Fatal("idempotent remove failed:", err)
	}
}

func TestClaudeInstallUsesUserScopeAndRollbackSafeFakeCLI(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	state := filepath.Join(home, "claude-state")
	t.Setenv("HOME", home)
	t.Setenv("FAKE_CLAUDE_STATE", state)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFakeExecutable(t, bin, "claude", `
if [ "$1" = "mcp" ] && [ "$2" = "get" ]; then
  if [ -f "$FAKE_CLAUDE_STATE" ]; then cat "$FAKE_CLAUDE_STATE"; exit 0; fi
  exit 1
fi
if [ "$1" = "mcp" ] && [ "$2" = "remove" ]; then rm -f "$FAKE_CLAUDE_STATE"; exit 0; fi
if [ "$1" = "mcp" ] && [ "$2" = "add" ]; then printf '%s\n' "$@" > "$FAKE_CLAUDE_STATE"; exit 0; fi
exit 1
`)
	profile := config.DesktopProfile{Name: "claude", Fallback: "diagnostic"}
	if err := installClaudeMCP("/tmp/atenea", profile, false); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeMCP("/tmp/atenea", profile, false); err != nil {
		t.Fatal("idempotent Claude install failed:", err)
	}
	if err := os.WriteFile(state, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeMCP("/tmp/atenea", profile, false); err == nil {
		t.Fatal("Claude conflict was silently replaced")
	}
	if err := installClaudeMCP("/tmp/atenea", profile, true); err != nil {
		t.Fatal(err)
	}
	if err := removeClaudeMCP(); err != nil {
		t.Fatal(err)
	}
	if err := removeClaudeMCP(); err != nil {
		t.Fatal("idempotent Claude remove failed:", err)
	}
}

func TestDoctorJSONReportsUnreachableMCPWithoutCallingTools(t *testing.T) {
	settingsPath, _ := isolated(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFakeExecutable(t, bin, "codex", "echo fake-codex; exit 0\n")
	t.Setenv("CODEX_HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdDoctorCompat(settingsPath, []string{"--client", "codex", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["client"] != "codex" || report["profile"] != "chatgpt" {
		t.Fatalf("unexpected doctor report: %#v", report)
	}
	if report["overall"] != "error" {
		t.Fatalf("unreachable MCP was not reported as error: %#v", report)
	}
}
