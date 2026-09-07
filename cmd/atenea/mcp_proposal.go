package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/mcpactivation"
	"github.com/Tutitoos/atenea/internal/mcpprobe"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func cmdMCPProposal(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "mcp needs propose, proposals, or activate")
	}
	store := mcpactivation.New(filepath.Join(platform.StateDir(), "mcp-proposals.json"))
	switch args[0] {
	case "propose":
		return proposeMCP(store, args[1:], out)
	case "proposals":
		if len(args) != 1 {
			return contract.Fail(contract.FailureInvalidInput, "mcp proposals takes no arguments")
		}
		rows, err := store.List()
		if err != nil {
			return err
		}
		return printMCPProposals(out, rows)
	case "activate":
		return activateMCP(store, settingsPath, args[1:], out)
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown mcp action %q", args[0])
	}
}

func proposeMCP(store *mcpactivation.Store, args []string, out io.Writer) error {
	var id, endpoint, protocol, expose string
	var command, tools, effects multiFlag
	var jsonOut bool
	flags := flag.NewFlagSet("mcp propose", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&id, "id", "", "stable integration id")
	flags.StringVar(&endpoint, "url", "", "MCP HTTP endpoint")
	flags.Var(&command, "command", "stdio command part; repeat in order")
	flags.StringVar(&protocol, "protocol", "auto", "legacy, auto, or modern-pin")
	flags.StringVar(&expose, "expose", "off", "off or raw")
	flags.Var(&tools, "tool", "allowed raw tool; repeat")
	flags.Var(&effects, "effect", "authorized raw effect; repeat")
	flags.BoolVar(&jsonOut, "json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput, "unexpected mcp propose arguments")
	}
	if (endpoint == "") == (len(command) == 0) {
		return contract.Fail(contract.FailureInvalidInput, "mcp propose needs exactly one --url or one or more --command")
	}
	for _, raw := range effects {
		if _, err := contract.ParseEffect(raw); err != nil {
			return err
		}
	}
	mode := mcpprobe.ProtocolMode(protocol)
	result := mcpprobe.Probe(context.Background(), mcpprobe.Server{ID: id, URL: endpoint, Command: command, ProtocolMode: mode, Timeout: 10 * time.Second})
	if !result.OK {
		return contract.Fail(contract.FailureUnavailable, "MCP %q did not pass discovery: %v", id, result.Err)
	}
	p, err := store.Put(mcpactivation.Proposal{ID: id, URL: endpoint, Command: command, ProtocolMode: protocol, RequestedProtocolVersion: result.RequestedProtocolVersion, ObservedProtocolVersion: result.ObservedProtocolVersion, ServerName: result.Name, ServerVersion: result.Version, Capabilities: result.Capabilities, Expose: expose, Tools: tools, Effects: effects})
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(p)
	}
	fmt.Fprintf(out, "proposal %s saved (%s %s, requested %s, observed %s)\n", p.ID, p.ServerName, p.ServerVersion, p.RequestedProtocolVersion, p.ObservedProtocolVersion)
	fmt.Fprintf(out, "capabilities declared: %s; functional validation: not tested\n", listOrNone(p.Capabilities))
	fmt.Fprintf(out, "activation: expose=%s tools=%s effects=%s\n", p.Expose, listOrNone(p.Tools), listOrNone(p.Effects))
	fmt.Fprintln(out, "Descriptions were not used to grant capabilities or permissions.")
	fmt.Fprintf(out, "Review, then run `atenea mcp activate %s --authorize` to change settings.\n", p.ID)
	return nil
}

func activateMCP(store *mcpactivation.Store, settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return contract.Fail(contract.FailureInvalidInput, "mcp activate needs one proposal id before its flags")
	}
	id, flagArgs := args[0], args[1:]
	var authorize bool
	flags := flag.NewFlagSet("mcp activate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&authorize, "authorize", false, "authorize this exact evidence-backed proposal")
	if err := flags.Parse(flagArgs); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput, "unexpected mcp activate arguments")
	}
	p, err := store.Get(id)
	if err != nil {
		return err
	}
	if !authorize {
		return contract.Fail(contract.FailurePermissionDenied, "activation changes MCP settings and permissions; review proposal %s and pass --authorize", p.ID)
	}
	path := settingsPath
	if path == "" {
		path = config.DefaultPath()
	}
	p, err = store.Activate(p.ID, path, p.EvidenceDigest, func(bound mcpactivation.Proposal) error {
		return appendMCPSettings(bound.SettingsPath, bound)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "activated %s in %s from evidence %s\n", p.ID, path, p.EvidenceDigest[:12])
	fmt.Fprintln(out, "Restart or reload the Atenea service before the integration becomes connected.")
	return nil
}

func appendMCPSettings(path string, p mcpactivation.Proposal) error {
	if cfg, err := config.Load(path); err == nil {
		for _, existing := range cfg.MCPServers {
			if existing.ID == p.ID {
				if sameMCPDeclaration(existing, p) {
					return nil
				}
				return contract.Fail(contract.FailureInvalidInput, "mcp_server %q already exists with different settings; existing integrations are never replaced", p.ID)
			}
		}
	} else if _, statErr := os.Stat(path); statErr == nil {
		return err
	}
	var prior []byte
	prior, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	block := renderMCPBlock(p)
	if len(prior) > 0 && prior[len(prior)-1] != '\n' {
		prior = append(prior, '\n')
	}
	next := append(append(append([]byte(nil), prior...), '\n'), block...)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".atenea-mcp-*.toml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(next)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err := config.Load(name); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "proposed MCP settings are invalid: %v", err)
	}
	return os.Rename(name, path)
}

func sameMCPDeclaration(existing config.MCPServer, p mcpactivation.Proposal) bool {
	if existing.URL != p.URL || strings.Join(existing.Command, "\x00") != strings.Join(p.Command, "\x00") || existing.ProtocolMode != p.ProtocolMode || string(existing.Expose) != p.Expose || strings.Join(existing.Tools, "\x00") != strings.Join(p.Tools, "\x00") {
		return false
	}
	effects := make([]string, len(existing.Effects))
	for i, effect := range existing.Effects {
		effects[i] = effect.String()
	}
	return strings.Join(effects, "\x00") == strings.Join(p.Effects, "\x00")
}

func renderMCPBlock(p mcpactivation.Proposal) []byte {
	var b strings.Builder
	b.WriteString("[[mcp_server]]\n")
	fmt.Fprintf(&b, "id = %s\n", strconv.Quote(p.ID))
	if p.URL != "" {
		fmt.Fprintf(&b, "url = %s\n", strconv.Quote(p.URL))
	} else {
		fmt.Fprintf(&b, "command = %s\n", tomlStrings(p.Command))
	}
	fmt.Fprintf(&b, "protocol_mode = %s\nexpose = %s\n", strconv.Quote(p.ProtocolMode), strconv.Quote(p.Expose))
	if p.Expose == "raw" {
		fmt.Fprintf(&b, "tools = %s\neffects = %s\n", tomlStrings(p.Tools), tomlStrings(p.Effects))
	}
	return []byte(b.String())
}

func tomlStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
func listOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func printMCPProposals(out io.Writer, rows []mcpactivation.Proposal) error {
	if len(rows) == 0 {
		fmt.Fprintln(out, "no MCP activation proposals")
		return nil
	}
	for _, p := range rows {
		fmt.Fprintf(out, "%-10s %-20s protocol=%s capabilities=%s permissions=%s evidence=%s\n", p.Status, p.ID, p.ObservedProtocolVersion, listOrNone(p.Capabilities), listOrNone(p.Effects), p.EvidenceDigest[:12])
	}
	return nil
}
