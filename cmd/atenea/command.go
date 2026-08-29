package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func cmdCommand(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "command needs a name; use `atenea command help`")
	}
	if strings.HasPrefix(args[0], "/atenea") {
		words, err := parseSlashCommand(strings.Join(args, " "))
		if err != nil {
			return err
		}
		args = words
	}
	name := args[0]
	flags := flag.NewFlagSet("command", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOut := flags.Bool("json", false, "print JSON")
	textOut := flags.Bool("text", false, "print plain text")
	markdownOut := flags.Bool("markdown", false, "print Markdown")
	capability := flags.String("capability", "", "filter metrics by capability")
	implementation := flags.String("implementation", "", "filter metrics by implementation")
	repository := flags.String("repository", "", "filter metrics by repository")
	flags.StringVar(repository, "repo", "", "alias for --repository")
	id := flags.String("id", "", "filter traces by execution id")
	typeName := flags.String("type", "", "filter traces by type")
	verdict := flags.String("verdict", "", "filter traces by verdict")
	open := flags.Bool("open", false, "show only open traces")
	since := flags.String("since", "", "trace window, for example 24h")
	limit := flags.Int("limit", 0, "maximum trace rows")
	all := flags.Bool("all", false, "include all incidents")
	client := flags.String("client", "", "client for doctor")
	profile := flags.String("profile", "", "desktop profile for doctor")
	if err := flags.Parse(args[1:]); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput, "command takes flags only, got %q", flags.Arg(0))
	}
	formats := 0
	if *jsonOut {
		formats++
	}
	if *textOut {
		formats++
	}
	if *markdownOut {
		formats++
	}
	if formats > 1 {
		return contract.Fail(contract.FailureInvalidInput, "choose only one output format")
	}
	format := "markdown"
	if *jsonOut {
		format = "json"
	}
	if *textOut {
		format = "text"
	}

	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	atenea, err := core.New(cfg, core.Command)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	response, err := atenea.Command(context.Background(), core.CommandRequest{
		Name: name, Format: format, Capability: *capability,
		Implementation: *implementation, Repository: *repository,
		ID: *id, Type: *typeName, Verdict: *verdict, Open: *open,
		Since: *since, Limit: *limit, All: *all, Client: *client, Profile: *profile,
	})
	if err != nil {
		return err
	}
	if format == "json" {
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	_, err = io.WriteString(out, response.Markdown)
	return err
}

// parseSlashCommand accepts quoted words but never invokes a shell.
func parseSlashCommand(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line != "/atenea" && !strings.HasPrefix(line, "/atenea ") {
		return nil, contract.Fail(contract.FailureInvalidInput, "command must start with /atenea")
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "/atenea"))
	if line == "" {
		return []string{"help"}, nil
	}
	var words []string
	var word strings.Builder
	quoted := rune(0)
	for _, r := range line {
		switch {
		case quoted != 0 && r == quoted:
			quoted = 0
		case quoted == 0 && (r == '\'' || r == '"'):
			quoted = r
		case quoted == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
		default:
			word.WriteRune(r)
		}
	}
	if quoted != 0 {
		return nil, contract.Fail(contract.FailureInvalidInput, "unterminated quote")
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words, nil
}
