package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
)

func cmdCodex(args []string, out io.Writer) error {
	if len(args) < 2 || args[0] != "agents" {
		return fmt.Errorf("codex requires: agents sync --global|--project PATH, or agents check")
	}
	switch args[1] {
	case "sync":
		return cmdCodexAgentsSync(args[2:], out)
	case "check":
		return cmdCodexAgentsCheck(args[2:], out)
	default:
		return fmt.Errorf("unknown codex agents command %q", args[1])
	}
}

func cmdCodexAgentsSync(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex agents sync", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	global := flags.Bool("global", false, "synchronize the global Codex agents directory")
	project := flags.String("project", "", "repository whose .codex/agents directory is synchronized")
	prune := flags.Bool("prune", false, "remove obsolete Atenea-managed profiles")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("codex agents sync received unexpected argument %q", flags.Arg(0))
	}
	path, err := codexAgentsTarget(*global, *project)
	if err != nil {
		return err
	}
	report, err := adaptercodex.SyncAgentProfiles(adaptercodex.SyncOptions{Path: path, Prune: *prune})
	if err != nil {
		return err
	}
	return printCodexAgentReport(report, *jsonOutput, out)
}

func cmdCodexAgentsCheck(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex agents check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	global := flags.Bool("global", false, "check the global Codex agents directory")
	project := flags.String("project", "", "repository whose .codex/agents directory is checked")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("codex agents check received unexpected argument %q", flags.Arg(0))
	}
	if !*global && *project == "" {
		*global = true
	}
	path, err := codexAgentsTarget(*global, *project)
	if err != nil {
		return err
	}
	report, err := adaptercodex.CheckAgentProfiles(path, adaptercodex.CanonicalAgentProfiles())
	if err != nil {
		return err
	}
	if err := printCodexAgentReport(report, *jsonOutput, out); err != nil {
		return err
	}
	if !report.Matches {
		return fmt.Errorf("codex ATENEA profiles are out of date in %s", path)
	}
	return nil
}

func codexAgentsTarget(global bool, project string) (string, error) {
	if global == (strings.TrimSpace(project) != "") {
		return "", fmt.Errorf("choose exactly one of --global or --project PATH")
	}
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return filepath.Join(codexHome, "agents"), nil
	}
	root, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".codex", "agents"), nil
}

func printCodexAgentReport(report adaptercodex.SyncReport, jsonOutput bool, out io.Writer) error {
	if jsonOutput {
		data, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}
	_, err := fmt.Fprintf(out, "Codex agents: %s\ndigest: %s\nchanged: %t\nmatched: %t\n", report.Path, report.Digest, report.Changed, report.Matches)
	if len(report.Pruned) > 0 {
		_, _ = fmt.Fprintf(out, "pruned: %s\n", strings.Join(report.Pruned, ", "))
	}
	if len(report.Skipped) > 0 {
		_, _ = fmt.Fprintf(out, "skipped: %s\n", strings.Join(report.Skipped, ", "))
	}
	return err
}
