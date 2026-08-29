package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
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

func supportedClientFlags(binary string, flags []string) []string {
	if len(flags) == 0 {
		return nil
	}
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		return nil
	}
	text := string(help)
	result := make([]string, 0, len(flags))
	for _, flag := range flags {
		if strings.Contains(text, flag) {
			result = append(result, flag)
		}
	}
	return result
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

func renderChatGPTMCP(self string, profile config.DesktopProfile) string {
	return fmt.Sprintf("# BEGIN ATENEA MANAGED MCP\n[mcp_servers.atenea]\ncommand = %q\nargs = [\"mcp\", \"--desktop-profile\", %q]\nenabled = true\nrequired = true\nstartup_timeout_sec = %d\ntool_timeout_sec = %d\ndefault_tools_approval_mode = \"prompt\"\n# END ATENEA MANAGED MCP\n", self, profile.Name, int(profile.StartupTimeout.Seconds()), int(profile.ToolTimeout.Seconds()))
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
	old := string(content)
	block := renderChatGPTMCP(self, profile)
	begin, end := "# BEGIN ATENEA MANAGED MCP", "# END ATENEA MANAGED MCP"
	if start := strings.Index(old, begin); start >= 0 {
		finish := strings.Index(old[start:], end)
		if finish < 0 {
			return errors.New("config.toml contiene un bloque Atenea incompleto")
		}
		finish += start + len(end)
		old = old[:start] + block + old[finish:]
	} else if strings.Contains(old, "[mcp_servers.atenea]") {
		if !replace {
			return errors.New("mcp_servers.atenea ya existe y no es gestionado por Atenea; usa --replace")
		}
		old += "\n" + block
	} else {
		if old != "" && !strings.HasSuffix(old, "\n") {
			old += "\n"
		}
		old += "\n" + block
	}
	if old == string(content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(content) > 0 {
		backup := fmt.Sprintf("%s.atenea-%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
		if err := os.WriteFile(backup, content, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(old), 0o600)
}

func removeChatGPTMCP() error {
	path, err := codexConfigPath()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	old := string(content)
	begin, end := "# BEGIN ATENEA MANAGED MCP", "# END ATENEA MANAGED MCP"
	start := strings.Index(old, begin)
	if start < 0 {
		return errors.New("no existe un bloque MCP gestionado por Atenea")
	}
	finish := strings.Index(old[start:], end)
	if finish < 0 {
		return errors.New("config.toml contiene un bloque Atenea incompleto")
	}
	finish += start + len(end)
	updated := strings.TrimRight(old[:start]+old[finish:], "\n") + "\n"
	backup := fmt.Sprintf("%s.atenea-%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(backup, content, 0o600); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func installClaudeMCP(self string, profile config.DesktopProfile) error {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return err
	}
	args := []string{"mcp", "add", "--scope", "user", "atenea", "--", self, "mcp", "--desktop-profile", profile.Name}
	if output, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("claude MCP install: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func removeClaudeMCP() error {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return err
	}
	if output, err := exec.Command(binary, "mcp", "remove", "atenea").CombinedOutput(); err != nil {
		return fmt.Errorf("claude MCP remove: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
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
		profileName, launch, replace := "", false, false
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
			default:
				return fmt.Errorf("opción desconocida: %s", args[i])
			}
		}
		profiles, err := config.DesktopProfilesFromFile(settingsPath)
		if err != nil {
			return err
		}
		cfg, err := config.Load(settingsPath)
		if err != nil {
			return err
		}
		if err := config.ValidateDesktopProfiles(profiles, cfg.MCPServers); err != nil {
			return err
		}
		profileClient := client
		if client == "codex" {
			profileClient = "chatgpt"
		}
		profile, err := config.ResolveDesktopProfile(profiles, profileName, profileClient)
		if err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		switch client {
		case "claude":
			if err := installClaudeMCP(self, profile); err != nil {
				return err
			}
		case "chatgpt", "codex":
			if err := installChatGPTMCP(self, profile, replace); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "Atenea instalada para %s con perfil %s\n", client, profile.Name)
		if launch {
			if runtime.GOOS != "darwin" {
				return errors.New("--launch para ChatGPT Desktop requiere macOS")
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
	profiles, err := config.DesktopProfilesFromFile(settingsPath)
	if err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(profiles, profileName, "chatgpt")
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
	profiles, err := config.DesktopProfilesFromFile(settingsPath)
	if err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(profiles, profileName, client)
	if err != nil {
		return err
	}
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	if err := config.ValidateDesktopProfiles(profiles, cfg.MCPServers); err != nil {
		return err
	}
	command := client
	if client == "chatgpt" {
		command = "open"
	}
	_, pathErr := exec.LookPath(command)
	mcpErr := mcpProbe(io.Discard)
	result := map[string]any{
		"client": client, "profile": profile.Name, "mcp_mode": profile.MCPMode,
		"fallback": profile.Fallback, "command": command, "available": pathErr == nil,
		"atenea_only":      profile.MCPMode == "atenea_only",
		"direct_mcp_count": len(config.FilterDesktopMCPServers(cfg.MCPServers, profile)),
		"mcp_ready":        mcpErr == nil,
	}
	if asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	status := "unavailable"
	if pathErr == nil {
		status = "available"
	}
	mcpStatus := "unavailable"
	if mcpErr == nil {
		mcpStatus = "ready"
	}
	fmt.Fprintf(out, "client=%s profile=%s command=%s status=%s mcp=%s mcp_mode=%s direct_mcp=%d fallback=%s\n", client, profile.Name, command, status, mcpStatus, profile.MCPMode, len(config.FilterDesktopMCPServers(cfg.MCPServers, profile)), profile.Fallback)
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
	profiles, err := config.DesktopProfilesFromFile(settingsPath)
	if err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(profiles, profileName, client)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "client=%s profile=%s mcp_mode=%s fallback=%s\n", client, profile.Name, profile.MCPMode, profile.Fallback)
	return nil
}
