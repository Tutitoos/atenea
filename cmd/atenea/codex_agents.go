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
	if len(args) >= 1 && args[0] == "certify" {
		return cmdCodexCertify(args[1:], out)
	}
	if len(args) < 2 {
		return fmt.Errorf("codex requires: agents sync|check, plan-mode sync|check, or certify start|status|complete|cancel|check")
	}
	if args[0] == "plan-mode" {
		return cmdCodexPlanMode(args[1:], out)
	}
	if args[0] != "agents" {
		return fmt.Errorf("codex requires: agents sync|check, plan-mode sync|check, or certify start|status|complete|cancel|check")
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

func cmdCodexPlanMode(args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "sync" && args[0] != "check") {
		return fmt.Errorf("codex plan-mode requires sync or check")
	}
	action := args[0]
	flags := flag.NewFlagSet("codex plan-mode "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	global := flags.Bool("global", false, "use the global Codex skills directory")
	project := flags.String("project", "", "repository whose .agents/skills directory is used")
	prune := flags.Bool("prune", false, "remove obsolete verified ATENEA-managed skills")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("codex plan-mode %s received unexpected argument %q", action, flags.Arg(0))
	}
	if action == "check" && *prune {
		return fmt.Errorf("--prune belongs to codex plan-mode sync")
	}
	if action == "check" && !*global && strings.TrimSpace(*project) == "" {
		*global = true
	}
	path, err := codexSkillsTarget(*global, *project)
	if err != nil {
		return err
	}
	var report adaptercodex.SyncReport
	if action == "sync" {
		report, err = adaptercodex.SyncSkills(adaptercodex.SkillSyncOptions{Path: path, Prune: *prune})
	} else {
		report, err = adaptercodex.CheckSkills(path, nil)
	}
	if err != nil {
		return err
	}
	if err := printCodexSyncReport("Codex Plan mode", report, *jsonOutput, out); err != nil {
		return err
	}
	if action == "check" && !report.Matches {
		return fmt.Errorf("codex ATENEA Plan mode integration is out of date in %s", path)
	}
	return nil
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
	return printCodexSyncReport("Codex agents", report, *jsonOutput, out)
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
	if err := printCodexSyncReport("Codex agents", report, *jsonOutput, out); err != nil {
		return err
	}
	if !report.Matches {
		return fmt.Errorf("codex ATENEA profiles are out of date in %s", path)
	}
	return nil
}

func codexSkillsTarget(global bool, project string) (string, error) {
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
		return filepath.Join(codexHome, "skills"), nil
	}
	root, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".agents", "skills"), nil
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

func printCodexSyncReport(label string, report adaptercodex.SyncReport, jsonOutput bool, out io.Writer) error {
	if jsonOutput {
		data, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}
	if _, err := fmt.Fprintf(out, "%s: %s\ndigest: %s\nchanged: %t\nmatched: %t\n", label, report.Path, report.Digest, report.Changed, report.Matches); err != nil {
		return err
	}
	if len(report.Pruned) > 0 {
		if _, err := fmt.Fprintf(out, "pruned: %s\n", strings.Join(report.Pruned, ", ")); err != nil {
			return err
		}
	}
	if len(report.Skipped) > 0 {
		if _, err := fmt.Fprintf(out, "skipped: %s\n", strings.Join(report.Skipped, ", ")); err != nil {
			return err
		}
	}
	return nil
}
