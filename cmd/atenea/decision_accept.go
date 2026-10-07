package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdAcceptDecisionPlan records the operator's explicit acceptance of a scoped
// implementation objective. It neither executes the plan nor grants effects.
func cmdAcceptDecisionPlan(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "accept-plan requires an id and --decision-context JSON")
	}
	id := args[0]
	flags := flag.NewFlagSet("accept-plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var raw string
	flags.StringVar(&raw, "decision-context", "", "reviewed semantic context: version, repository, active_objective, scope_files, constraints")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	intent, err := parseDecisionContext(raw)
	if err != nil {
		return err
	}
	if intent == nil || flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput, "accept-plan requires --decision-context and no extra arguments")
	}
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	for _, repository := range atenea.Settings().Repositories {
		if repository.ID != intent.Repository {
			continue
		}
		ref, err := (decision.AcceptedPlanStore{}).Accept(context.Background(), id, repository.Path, *intent)
		if err != nil {
			return contract.Fail(contract.FailureInvalidInput, "accept-plan: %v", err)
		}
		return json.NewEncoder(out).Encode(ref)
	}
	return contract.Fail(contract.FailureInvalidInput, "accept-plan requires a declared repository")
}
