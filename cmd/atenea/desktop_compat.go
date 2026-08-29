package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/ipc"
)

type wrapOptions struct {
	Client     string
	Profile    string
	EmitConfig bool
	ClientArgs []string
}

func parseWrapOptions(args []string) (wrapOptions, error) {
	var options wrapOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			options.ClientArgs = append(options.ClientArgs, args[i+1:]...)
			return options, nil
		case "--client":
			if i+1 >= len(args) {
				return wrapOptions{}, errors.New("--client requires a value")
			}
			options.Client, i = args[i+1], i+1
		case "--profile":
			if i+1 >= len(args) {
				return wrapOptions{}, errors.New("--profile requires a value")
			}
			options.Profile, i = args[i+1], i+1
		case "--emit-config":
			options.EmitConfig = true
		default:
			if options.Client == "" && !strings.HasPrefix(args[i], "-") {
				options.Client = args[i]
				continue
			}
			options.ClientArgs = append(options.ClientArgs, args[i])
		}
	}
	return options, nil
}

func peelDesktopProfile(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, nil
	}
	remaining := make([]string, 0, len(args))
	profile := ""
	for i := 0; i < len(args); i++ {
		if args[i] != "--desktop-profile" {
			remaining = append(remaining, args[i])
			continue
		}
		if i+1 >= len(args) {
			return "", nil, errors.New("--desktop-profile requires a value")
		}
		profile, i = args[i+1], i+1
	}
	return profile, remaining, nil
}

var clientCapabilityCache = struct {
	sync.Mutex
	values map[string][]string
}{values: make(map[string][]string)}

func detectClientFlags(binary string, flags []string) ([]string, []string) {
	if len(flags) == 0 {
		return nil, nil
	}
	info, err := os.Stat(binary)
	if err != nil {
		return nil, append([]string(nil), flags...)
	}
	version := commandVersion(binary)
	key := fmt.Sprintf("%s|%s|%d", binary, version, info.ModTime().UnixNano())
	clientCapabilityCache.Lock()
	if cached, ok := clientCapabilityCache.values[key]; ok {
		clientCapabilityCache.Unlock()
		return append([]string(nil), cached...), omittedFlags(flags, cached)
	}
	clientCapabilityCache.Unlock()
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		return nil, append([]string(nil), flags...)
	}
	text := string(help)
	result := make([]string, 0, len(flags))
	for _, flag := range flags {
		if strings.Contains(text, flag) {
			result = append(result, flag)
		}
	}
	clientCapabilityCache.Lock()
	clientCapabilityCache.values[key] = append([]string(nil), result...)
	clientCapabilityCache.Unlock()
	return result, omittedFlags(flags, result)
}

func omittedFlags(all, supported []string) []string {
	result := make([]string, 0, len(all))
	for _, flag := range all {
		if !containsString(supported, flag) {
			result = append(result, flag)
		}
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func commandVersion(binary string) string {
	output, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
}

type desktopClientResolution struct {
	Client         string   `json:"client"`
	Path           string   `json:"path,omitempty"`
	Source         string   `json:"source"`
	Version        string   `json:"version,omitempty"`
	BinaryMTime    string   `json:"binary_mtime,omitempty"`
	SupportedFlags []string `json:"supported_flags,omitempty"`
	OmittedFlags   []string `json:"omitted_flags,omitempty"`
	AppInstalled   bool     `json:"app_installed"`
}

func resolveDesktopClient(client string, flags []string) desktopClientResolution {
	result := desktopClientResolution{Client: client}
	if client == "chatgpt" {
		app := "/Applications/ChatGPT.app"
		result.Source = "app"
		result.Path = app
		_, statErr := os.Stat(app)
		result.AppInstalled = statErr == nil
		result.Version = appVersion(app)
		return result
	}
	if client == "codex" {
		if path, err := exec.LookPath("codex"); err == nil {
			result.Path, result.Source = path, "path"
		} else if runtime.GOOS == "darwin" {
			path := "/Applications/ChatGPT.app/Contents/Resources/codex"
			if _, statErr := os.Stat(path); statErr == nil {
				result.Path, result.Source = path, "chatgpt_bundle"
			}
		}
	} else {
		path, err := exec.LookPath(client)
		if err == nil {
			result.Path, result.Source = path, "path"
		}
	}
	if result.Path == "" {
		return result
	}
	if info, err := os.Stat(result.Path); err == nil {
		result.BinaryMTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	result.Version = commandVersion(result.Path)
	result.SupportedFlags, result.OmittedFlags = detectClientFlags(result.Path, flags)
	return result
}

func appVersion(app string) string {
	data, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	text := string(data)
	key := "<key>CFBundleShortVersionString</key>"
	start := strings.Index(text, key)
	if start < 0 {
		return ""
	}
	value := text[start+len(key):]
	open, end := strings.Index(value, "<string>"), strings.Index(value, "</string>")
	if open < 0 || end < open {
		return ""
	}
	return strings.TrimSpace(value[open+len("<string>") : end])
}

func codexConfigPath() (string, error) {
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".codex")
	}
	return filepath.Join(root, "config.toml"), nil
}

func tomlQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return `"` + value + `"`
}

func renderChatGPTMCP(self string, profile config.DesktopProfile) string {
	return fmt.Sprintf("# BEGIN ATENEA MANAGED MCP\n[mcp_servers.atenea]\ncommand = %s\nargs = [\"mcp\", \"--desktop-profile\", %s]\nenabled = true\nrequired = true\nstartup_timeout_sec = %d\ntool_timeout_sec = %d\ndefault_tools_approval_mode = \"prompt\"\n# END ATENEA MANAGED MCP\n", tomlQuote(self), tomlQuote(profile.Name), int(profile.StartupTimeout.Seconds()), int(profile.ToolTimeout.Seconds()))
}

func managedBlockSpan(text string) (int, int, bool) {
	begin, end := "# BEGIN ATENEA MANAGED MCP", "# END ATENEA MANAGED MCP"
	if strings.Count(text, begin) > 1 {
		return 0, 0, false
	}
	start := strings.Index(text, begin)
	if start < 0 {
		return 0, 0, false
	}
	lineStart := strings.LastIndex(text[:start], "\n") + 1
	finish := strings.Index(text[start+len(begin):], end)
	if finish < 0 {
		return 0, 0, false
	}
	finish += start + len(begin) + len(end)
	if newline := strings.IndexByte(text[finish:], '\n'); newline >= 0 {
		finish += newline + 1
	}
	return lineStart, finish, true
}

func mcpTableSpan(text, header string) (int, int, bool) {
	offset := 0
	start := -1
	nestedPrefix := strings.TrimSuffix(header, "]") + "."
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if start < 0 {
			if trimmed == header {
				start = offset
			}
		} else if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, nestedPrefix) {
			return start, offset, true
		}
		offset += len(line)
	}
	if start >= 0 {
		return start, len(text), true
	}
	return 0, 0, false
}

func removeMCPTable(text string) (string, bool) {
	start, end, ok := mcpTableSpan(text, "[mcp_servers.atenea]")
	if !ok {
		return text, false
	}
	return text[:start] + text[end:], true
}

func validateTOML(text string) error {
	var document map[string]any
	if _, err := toml.Decode(text, &document); err != nil {
		return fmt.Errorf("config.toml inválido: %w", err)
	}
	return nil
}

func backupPath(path string) string {
	base := fmt.Sprintf("%s.atenea-%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	if _, err := os.Stat(base); err != nil {
		return base
	}
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("%s.%d", base, index)
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
	}
}

func atomicDesktopWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".atenea-config-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func writeDesktopConfig(path string, old, updated []byte, mode os.FileMode) error {
	if bytes.Equal(old, updated) {
		return nil
	}
	if len(old) > 0 {
		if err := os.WriteFile(backupPath(path), old, mode); err != nil {
			return err
		}
	}
	return atomicDesktopWrite(path, updated, mode)
}

func validateCodexMCPEntry(self string, profile config.DesktopProfile) error {
	resolved := resolveDesktopClient("codex", nil)
	if resolved.Path == "" {
		return nil
	}
	overrides := []string{
		"mcp_servers.atenea.command=" + tomlQuote(self),
		"mcp_servers.atenea.args=[\"mcp\",\"--desktop-profile\"," + tomlQuote(profile.Name) + "]",
	}
	args := make([]string, 0, len(overrides)*2+3)
	for _, override := range overrides {
		args = append(args, "-c", override)
	}
	args = append(args, "mcp", "get", "atenea")
	if _, err := exec.Command(resolved.Path, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("codex no pudo validar mcp_servers.atenea: %v", err)
	}
	return nil
}

func installChatGPTMCP(self string, profile config.DesktopProfile, replace bool) error {
	path, err := codexConfigPath()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	old := string(content)
	block := renderChatGPTMCP(self, profile)
	if start, finish, ok := managedBlockSpan(old); ok {
		old = old[:start] + block + old[finish:]
	} else if strings.Contains(old, "[mcp_servers.atenea]") {
		if !replace {
			return errors.New("mcp_servers.atenea ya existe y no es gestionado por Atenea; usa --replace")
		}
		updated, _ := removeMCPTable(old)
		old = strings.TrimRight(updated, "\n") + "\n\n" + block
	} else {
		if old != "" && !strings.HasSuffix(old, "\n") {
			old += "\n"
		}
		old += "\n" + block
	}
	if err := validateTOML(old); err != nil {
		return err
	}
	if err := validateCodexMCPEntry(self, profile); err != nil {
		return err
	}
	return writeDesktopConfig(path, content, []byte(old), mode)
}

func removeChatGPTMCP() error {
	path, err := codexConfigPath()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	start, finish, ok := managedBlockSpan(string(content))
	if !ok {
		return nil
	}
	updated := strings.TrimRight(string(content[:start])+string(content[finish:]), "\n") + "\n"
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	return writeDesktopConfig(path, content, []byte(updated), mode)
}

func claudeUserConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

func claudeMCPGet(binary string) (string, bool) {
	output, err := runClaudeMCPCommand(binary, "mcp", "get", "atenea")
	if err != nil {
		return string(output), false
	}
	return string(output), true
}

func installClaudeMCP(self string, profile config.DesktopProfile, replace bool) error {
	return installClaudeMCPWithProject(self, profile, replace, false)
}

func removeClaudeMCP() error {
	return removeManagedClaudeMCP()
}

func cmdDesktopMCP(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("desktop MCP requiere install o remove")
	}
	switch args[0] {
	case "install":
		if len(args) < 2 {
			return errors.New("desktop install requiere cliente")
		}
		client := args[1]
		profileName, launch, replace, replaceProject := "", false, false, false
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--profile":
				if i+1 >= len(args) {
					return errors.New("--profile requires a value")
				}
				profileName, i = args[i+1], i+1
			case "--launch":
				launch = true
			case "--replace":
				replace = true
			case "--replace-project":
				replaceProject = true
			default:
				return fmt.Errorf("opción desconocida: %s", args[i])
			}
		}
		if replaceProject && client != "claude" {
			return errors.New("--replace-project solo aplica a Claude")
		}
		if replaceProject && !replace {
			return errors.New("--replace-project requiere --replace")
		}
		cfg, err := config.Load(settingsPath)
		if err != nil {
			return err
		}
		profileClient := client
		if client == "codex" {
			profileClient = "chatgpt"
		}
		profile, err := config.ResolveDesktopProfile(cfg.DesktopProfiles, profileName, profileClient)
		if err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		switch client {
		case "claude":
			skillChanged, err := installClaudeSkill(replace)
			if err != nil {
				return err
			}
			if err := installClaudeMCPWithProject(self, profile, replace, replaceProject); err != nil {
				if skillChanged {
					_ = removeClaudeSkill()
				}
				return err
			}
		case "chatgpt", "codex":
			if err := installChatGPTMCP(self, profile, replace); err != nil {
				return err
			}
		}
		if client == "claude" {
			fmt.Fprintf(out, "Atenea instalada para %s con perfil %s y skill /atenea\n", client, profile.Name)
		} else {
			fmt.Fprintf(out, "Atenea instalada para %s con perfil %s\n", client, profile.Name)
		}
		if launch {
			if client != "chatgpt" {
				return errors.New("--launch solo está disponible para ChatGPT Desktop")
			}
			if runtime.GOOS != "darwin" {
				return errors.New("--launch para ChatGPT Desktop requiere macOS")
			}
			if !resolveDesktopClient("chatgpt", nil).AppInstalled {
				return errors.New("ChatGPT Desktop no está instalado en /Applications/ChatGPT.app")
			}
			return exec.Command("open", "-a", "ChatGPT").Run()
		}
		return nil
	case "remove":
		client := "chatgpt"
		if len(args) > 1 {
			client = args[1]
		}
		var err error
		switch client {
		case "claude":
			err = removeClaudeMCP()
			if err == nil {
				err = removeClaudeSkill()
			}
		case "chatgpt", "codex":
			err = removeChatGPTMCP()
		default:
			return fmt.Errorf("desktop remove no soporta %q", client)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "MCP de Atenea eliminado para %s\n", client)
		return nil
	default:
		return fmt.Errorf("subcomando desktop MCP desconocido: %s", args[0])
	}
}

func emitChatGPTConfig(settingsPath, profileName string, out io.Writer) error {
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(cfg.DesktopProfiles, profileName, "chatgpt")
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Fprint(out, renderChatGPTMCP(self, profile))
	return nil
}

type doctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

type doctorReport struct {
	Client        string                    `json:"client"`
	ClientVersion string                    `json:"client_version,omitempty"`
	ClientPath    string                    `json:"client_path,omitempty"`
	ClientSource  string                    `json:"client_source,omitempty"`
	Profile       string                    `json:"profile"`
	Overall       string                    `json:"overall"`
	Checks        []doctorCheck             `json:"checks"`
	Config        map[string]string         `json:"config"`
	Telemetry     core.CompatibilitySummary `json:"telemetry"`
}

func appendDoctorCheck(report *doctorReport, id, status, detail, remedy string) {
	report.Checks = append(report.Checks, doctorCheck{ID: id, Status: status, Detail: detail, Remedy: remedy})
	if status == "error" {
		report.Overall = "error"
	} else if status == "degraded" && report.Overall == "ok" {
		report.Overall = "degraded"
	}
}

func writeMCPRequest(conn io.Writer, id int, method string, params any) error {
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	return json.NewEncoder(conn).Encode(request)
}

func readMCPResponse(reader *bufio.Reader) (map[string]any, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var response map[string]any
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, err
	}
	if response["error"] != nil {
		return nil, fmt.Errorf("MCP response error: %v", response["error"])
	}
	return response, nil
}

func probeMCPForDoctor(profile, client, version string) (int, []doctorCheck) {
	conn, err := ipc.Dial(core.SocketPath())
	if err != nil {
		return 0, []doctorCheck{{ID: "mcp.socket", Status: "error", Detail: err.Error(), Remedy: "Start the Atenea service before connecting the desktop client."}}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	params := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": client, "version": version},
		"_meta":           map[string]any{"atenea": map[string]string{"profile": profile}},
	}
	if err := writeMCPRequest(conn, 1, "initialize", params); err != nil {
		return 0, []doctorCheck{{ID: "mcp.initialize", Status: "error", Detail: err.Error(), Remedy: "Check the Atenea MCP transport."}}
	}
	if _, err := readMCPResponse(reader); err != nil {
		return 0, []doctorCheck{{ID: "mcp.initialize", Status: "error", Detail: err.Error(), Remedy: "Check the selected desktop profile and MCP handshake."}}
	}
	checks := []doctorCheck{{ID: "mcp.initialize", Status: "ok", Detail: "initialize respondió correctamente"}}
	if err := writeMCPRequest(conn, 2, "tools/list", nil); err != nil {
		return 0, append(checks, doctorCheck{ID: "mcp.tools_list", Status: "error", Detail: err.Error(), Remedy: "Check the Atenea MCP socket."})
	}
	response, err := readMCPResponse(reader)
	if err != nil {
		return 0, append(checks, doctorCheck{ID: "mcp.tools_list", Status: "error", Detail: err.Error(), Remedy: "Check tools/list and the active profile."})
	}
	result, _ := response["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	invalid := 0
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		schema, _ := tool["inputSchema"].(map[string]any)
		if strings.TrimSpace(name) == "" || schema == nil || schema["type"] != "object" {
			invalid++
		}
	}
	if invalid > 0 {
		checks = append(checks, doctorCheck{ID: "mcp.schemas", Status: "error", Detail: fmt.Sprintf("%d tool schema(s) inválidos", invalid), Remedy: "Normalize inputSchema in the Atenea adapter."})
	} else {
		checks = append(checks, doctorCheck{ID: "mcp.schemas", Status: "ok", Detail: fmt.Sprintf("%d tool(s) anunciadas con schema válido", len(tools))})
	}
	checks = append(checks, doctorCheck{ID: "mcp.tools_list", Status: "ok", Detail: fmt.Sprintf("%d tool(s) disponibles", len(tools))})
	return len(tools), checks
}

func installedChatGPTState(profile config.DesktopProfile) string {
	path, err := codexConfigPath()
	if err != nil {
		return "missing"
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "managed_drift"
	}
	if _, _, ok := managedBlockSpan(string(content)); ok {
		if strings.Contains(string(content), `"--desktop-profile", "`+profile.Name+`"`) {
			return "managed_match"
		}
		return "managed_drift"
	}
	if _, _, ok := mcpTableSpan(string(content), "[mcp_servers.atenea]"); ok {
		return "unmanaged_collision"
	}
	return "missing"
}

func cmdDoctorCompat(settingsPath string, args []string, out io.Writer) error {
	client, profileName, asJSON := "", "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--client":
			if i+1 >= len(args) {
				return errors.New("--client requires a value")
			}
			client, i = args[i+1], i+1
		case "--profile":
			if i+1 >= len(args) {
				return errors.New("--profile requires a value")
			}
			profileName, i = args[i+1], i+1
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("opción desconocida: %s", args[i])
		}
	}
	if client == "" {
		return errors.New("doctor requiere --client claude|chatgpt|codex")
	}
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(cfg.DesktopProfiles, profileName, client)
	if err != nil {
		return err
	}
	resolved := resolveDesktopClient(client, profile.ClientFlags)
	report := doctorReport{
		Client: client, ClientVersion: resolved.Version, ClientPath: resolved.Path,
		ClientSource: resolved.Source, Profile: profile.Name, Overall: "ok",
		Config:    map[string]string{"state": installedChatGPTState(profile)},
		Telemetry: core.ReadCompatibilitySummaryFor(client, profile.Name),
	}
	if resolved.Path == "" && client != "chatgpt" {
		appendDoctorCheck(&report, "client.resolve", "error", "cliente no encontrado", "Instala el cliente o añade su binario a PATH.")
	} else if client == "chatgpt" && !resolved.AppInstalled {
		appendDoctorCheck(&report, "client.resolve", "error", "ChatGPT Desktop no está instalado", "Instala ChatGPT Desktop antes de usar desktop install --launch.")
	} else {
		appendDoctorCheck(&report, "client.resolve", "ok", fmt.Sprintf("%s (%s)", resolved.Path, resolved.Version), "")
	}
	if len(resolved.OmittedFlags) > 0 {
		appendDoctorCheck(&report, "client.flags", "degraded", "flags omitidas: "+strings.Join(resolved.OmittedFlags, ", "), "Actualiza el cliente o usa el flujo sin esa flag opcional.")
	} else {
		appendDoctorCheck(&report, "client.flags", "ok", "todas las flags requeridas están disponibles", "")
	}
	appendDoctorCheck(&report, "profile.policy", "ok", fmt.Sprintf("mode=%s direct_mcp=%d fallback=%s startup=%s tool=%s", profile.MCPMode, len(config.FilterDesktopMCPServers(cfg.MCPServers, profile)), profile.Fallback, profile.StartupTimeout, profile.ToolTimeout), "")
	if client == "chatgpt" || client == "codex" {
		if report.Config["state"] == "unmanaged_collision" {
			appendDoctorCheck(&report, "config.installed", "degraded", "mcp_servers.atenea existe fuera de Atenea", "Usa desktop install ... --replace para adoptarla explícitamente.")
		} else {
			appendDoctorCheck(&report, "config.installed", "ok", report.Config["state"], "")
		}
	} else {
		state, inspection := claudeMCPState(profile)
		report.Config["state"] = state
		report.Config["scopes"] = strings.Join(inspection.Scopes, ",")
		if state == "missing" || state == "managed_match" {
			appendDoctorCheck(&report, "config.installed", "ok", state, "")
		} else {
			remedy := "Usa desktop install claude --profile " + profile.Name + " --replace para reemplazarlo explícitamente."
			if claudeScopePresent(inspection, "project") {
				remedy = "Usa desktop install claude --profile " + profile.Name + " --replace --replace-project para adoptar también .mcp.json."
			}
			appendDoctorCheck(&report, "config.installed", "degraded", state, remedy)
		}
	}
	if resolved.Path != "" && (client != "chatgpt" || resolved.AppInstalled) {
		_, checks := probeMCPForDoctor(profile.Name, client, resolved.Version)
		for _, check := range checks {
			report.Checks = append(report.Checks, check)
			if check.Status == "error" {
				report.Overall = "error"
			} else if check.Status == "degraded" && report.Overall == "ok" {
				report.Overall = "degraded"
			}
		}
	} else {
		appendDoctorCheck(&report, "mcp.initialize", "skipped", "cliente no resuelto", "Instala el cliente antes de probar MCP.")
	}
	if asJSON {
		return json.NewEncoder(out).Encode(report)
	}
	fmt.Fprintf(out, "client=%s version=%s profile=%s overall=%s config=%s telemetry=available:%d denied:%d fallback:%d error:%d\n", report.Client, report.ClientVersion, report.Profile, report.Overall, report.Config["state"], report.Telemetry.Available, report.Telemetry.Denied, report.Telemetry.Fallback, report.Telemetry.Error)
	return nil
}

func cmdDesktopStatusCompat(settingsPath string, args []string, out io.Writer) error {
	client, profileName := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--client":
			if i+1 >= len(args) {
				return errors.New("--client requires a value")
			}
			client, i = args[i+1], i+1
		case "--profile":
			if i+1 >= len(args) {
				return errors.New("--profile requires a value")
			}
			profileName, i = args[i+1], i+1
		default:
			return fmt.Errorf("opción desconocida: %s", args[i])
		}
	}
	if client == "" {
		return errors.New("status desktop requiere --client")
	}
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(cfg.DesktopProfiles, profileName, client)
	if err != nil {
		return err
	}
	telemetry := core.ReadCompatibilitySummaryFor(client, profile.Name)
	fmt.Fprintf(out, "client=%s profile=%s mcp_mode=%s fallback=%s telemetry=available:%d denied:%d fallback:%d error:%d\n", client, profile.Name, profile.MCPMode, profile.Fallback, telemetry.Available, telemetry.Denied, telemetry.Fallback, telemetry.Error)
	return nil
}
