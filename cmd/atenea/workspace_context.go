package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/workspacecontext"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type workspaceRepositories []string

func (r *workspaceRepositories) String() string { return strings.Join(*r, ",") }
func (r *workspaceRepositories) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("repository id cannot be empty")
	}
	*r = append(*r, value)
	return nil
}

// cmdWorkspaceContext is the multi-repository CLI surface. It resolves each
// requested ID to its configured physical root before dispatch, while the
// coordinator still performs the complete duplicate/alias/authorization
// preflight before any child starts.
func cmdWorkspaceContext(settingsPath string, args []string, out io.Writer) error {
	var repositories workspaceRepositories
	var payloadFile string
	var budget float64
	var maxParallel int
	var jsonOut bool
	flags := flag.NewFlagSet("workspace.context", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&repositories, "repo", "registered repository ID; repeat for each workspace target")
	flags.StringVar(&payloadFile, "payload", "", "read shared code.context payload from a JSON file")
	flags.Float64Var(&budget, "budget", 0, "shared workspace budget in usd")
	flags.IntVar(&maxParallel, "max-parallel", 0, "lower the default child concurrency")
	flags.BoolVar(&jsonOut, "json", false, "print the complete workspace result as JSON")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput, "unexpected argument %q", flags.Arg(0))
	}
	if len(repositories) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "workspace.context requires at least one --repo; repeat it for each repository")
	}
	if payloadFile == "" {
		return contract.Fail(contract.FailureInvalidInput, "workspace.context requires --payload with the code.context JSON payload")
	}
	raw, err := os.ReadFile(payloadFile)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		if err == nil {
			err = fmt.Errorf("payload must be a JSON object")
		}
		return contract.Fail(contract.FailureInvalidInput, "workspace.context payload: %v", err)
	}
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	targets := make([]workspacecontext.Target, 0, len(repositories))
	for _, id := range repositories {
		repo, err := atenea.Registry().Repository(id)
		if err != nil {
			return err
		}
		// Core resolves the configured physical root server-side. The CLI only
		// carries the stable repository ID across the boundary.
		targets = append(targets, workspacecontext.Target{ID: repo.ID})
	}
	request := workspacecontext.Request{
		Targets: targets, Payload: payload, BudgetUSD: budget, MaxParallel: maxParallel,
		Permission: contract.Permission{Task: workspacecontext.Capability, Effects: []contract.Effect{contract.EffectRead}},
	}
	noticeOut := out
	if jsonOut {
		// Keep stdout machine-readable while still exposing the pre-dispatch
		// activity to an interactive CLI caller.
		noticeOut = os.Stderr
	}
	request.BeforeDispatch = func(_ context.Context, target workspacecontext.Target) error {
		id := contract.RedactRaw(strings.TrimSpace(target.ID))
		_, err := fmt.Fprintln(noticeOut, "> **ATENEA · code.context** — busco contexto en el repositorio "+id+" para responder la consulta del espacio de trabajo.")
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	session, err := atenea.Open(core.SessionOptions{Client: "atenea-cli", Origin: core.SessionOrigin{Surface: "cli", Transport: "local"}})
	if err != nil {
		return err
	}
	defer session.Close()
	result, err := atenea.WorkspaceContext(ctx, session, request)
	if err != nil {
		return err
	}
	if jsonOut {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	fmt.Fprintf(out, "workspace.context: %d repositorio(s)%s\n", len(result.Repositories), func() string {
		if result.Partial {
			return " · parcial"
		}
		return ""
	}())
	for _, row := range result.Repositories {
		state := "ok"
		if row.Error != "" {
			state = "error: " + row.Error
		}
		fmt.Fprintf(out, "- %s: %s", row.ID, state)
		if row.Provider != "" || row.Implementation != "" {
			fmt.Fprintf(out, " · %s/%s", row.Provider, row.Implementation)
		}
		if row.NextCursor != "" {
			fmt.Fprintf(out, " · next_cursor=%s", row.NextCursor)
		}
		fmt.Fprintln(out)
	}
	return nil
}
