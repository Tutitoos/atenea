package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdFloor lists what has been measured, or measures one more pair.
//
// With no subcommand it lists: that is the operator's own reason to run it
// unprompted, the way "atenea metrics" and "atenea traces" already read
// without one. "measure" is the one subcommand, because it is the one act
// this command can perform that spends money -- everything else about a
// floor is either reading the cache or refusing to read a broken one.
func cmdFloor(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return floorList(settingsPath, out)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "measure":
		return floorMeasure(settingsPath, rest, out)
	default:
		return contract.Fail(contract.FailureInvalidInput,
			"unknown floor subcommand %q: measure, or no subcommand to list", sub)
	}
}

// floorList prints every stored measurement.
//
// It never spends anything: reading floors.json and asking the configured
// CLI its own --version are both free. Only "measure" pays for a number.
func floorList(settingsPath string, out io.Writer) error {
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	store, err := floor.Open("")
	if err != nil {
		return err
	}
	rows, err := store.List()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no floor has been measured yet; run atenea floor measure --repo ID")
		return nil
	}

	binary := strings.TrimSpace(cfg.Model.Binary)
	if binary == "" {
		binary = model.DefaultBinary
	}
	// Cheap: --version is what toolversion always probes with, and asking it
	// costs nothing beyond starting the CLI once. This is the "running CLI"
	// every row's own CLIVersion is compared against below.
	running := toolversion.New(binary, "--version").Version(context.Background())

	fmt.Fprintf(out, "%-20s %-16s %10s %14s %-18s %s\n",
		"REPOSITORY", "MODEL", "USD", "CACHE-WRITE", "CLI VERSION", "AGE")
	now := time.Now()
	stale := 0
	for _, m := range rows {
		version := m.CLIVersion
		if version == "" {
			version = "(unknown)"
		}
		mark := ""
		if running != "" && m.CLIVersion != "" && m.CLIVersion != running {
			mark = " stale"
			stale++
		}
		fmt.Fprintf(out, "%-20s %-16s %10s %14s %-18s measured %s%s\n",
			m.Repository, m.Model, formatUSD(m.USD), groupedInt(m.CacheWriteTokens),
			version, formatAge(now.Sub(m.MeasuredAt)), mark)
	}
	if stale > 0 {
		fmt.Fprintf(out, "\n%d row(s) marked stale: the running CLI answers %q now, and the "+
			"system prompt and tool schemas ship WITH the CLI, so a different CLI is a "+
			"different floor even against the same repository and model. Re-run "+
			"atenea floor measure to replace a stale row.\n", stale, running)
	}
	return nil
}

// floorMeasure spends one real turn to price starting another one, and
// stores what it found.
func floorMeasure(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("floor measure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repoFlag := flags.String("repo", "", "repository id or path to measure (required)")
	modelFlag := flags.String("model", "plan", "which configured model to measure: explore or plan")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"floor measure takes no positional arguments, only --repo and --model: got %q", flags.Arg(0))
	}
	if strings.TrimSpace(*repoFlag) == "" {
		return contract.Fail(contract.FailureInvalidInput, "floor measure: --repo is required")
	}
	role, err := floorRole(*modelFlag)
	if err != nil {
		return err
	}

	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	// The same lookup "atenea select", "atenea detect" and "atenea ask" use:
	// an exact registered id, or -- because registry.Repository falls back
	// to it -- an absolute path under one of them. Building only the
	// repository half of the catalog here, rather than a full core.Core,
	// is deliberate: nothing about this command dispatches a capability or
	// needs a provider, and a heavier core.New would start machinery
	// (managed processes among them) this command never asks for.
	catalog := registry.New()
	for _, repo := range cfg.Repositories {
		if err := catalog.AddRepository(repo); err != nil {
			return err
		}
	}
	repo, err := catalog.Repository(*repoFlag)
	if err != nil {
		return err
	}

	client, err := model.New(model.Options(cfg.Model))
	if err != nil {
		return err
	}

	store, err := floor.Open("")
	if err != nil {
		return err
	}
	// modelName is resolved the same way Client.Floor resolves it
	// internally (Options.Explore / Options.Plan, by Role) -- read here,
	// ahead of spending anything, only so the warning below can name the
	// pair and quote what it cost last time. Client.Floor's own returned
	// Measurement.Model is still what gets stored; this is not a second
	// source of truth, only an early read of the same one.
	modelName := strings.TrimSpace(cfg.Model.Explore)
	if role == model.RolePlan {
		modelName = strings.TrimSpace(cfg.Model.Plan)
	}
	if modelName == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"floor measure: no model is configured for role %q", role)
	}
	previous, hadPrevious, err := store.Get(repo.ID, modelName)
	if err != nil {
		return err
	}

	// This is the one line every caller of this command has to see before
	// anything is spent: CALLING Client.Floor below spends real money, one
	// turn, priced at roughly the floor itself -- see Client.Floor's own
	// doc for why nothing calls it implicitly.
	if hadPrevious {
		fmt.Fprintf(out, "about to spend real money: one turn on %s with %s -- last measured at ~%s\n",
			repo.ID, modelName, formatUSD(previous.USD))
	} else {
		fmt.Fprintf(out, "about to spend real money: one turn on %s with %s -- no previous "+
			"measurement, the amount is unknown\n", repo.ID, modelName)
	}

	tools, builtins, err := floorTurnShape(role)
	if err != nil {
		return err
	}
	measured, err := client.Floor(context.Background(), model.FloorRequest{
		Role:     role,
		Dir:      repo.Path,
		Tools:    tools,
		Builtins: builtins,
	})
	if err != nil {
		return err
	}

	measuredAt := time.Now().UTC()
	if err := store.Put(floor.Measurement{
		Repository:       repo.ID,
		Model:            measured.Model,
		USD:              measured.USD,
		CacheWriteTokens: measured.CacheWriteTokens,
		InputTokens:      measured.InputTokens,
		OutputTokens:     measured.OutputTokens,
		CLIVersion:       measured.CLIVersion,
		MeasuredAt:       measuredAt,
	}); err != nil {
		return err
	}

	fmt.Fprintf(out, "starting a turn on %s with %s costs ~%s (%s tokens of cache write: "+
		"system prompt and tool definitions, before any file is read)\n",
		repo.ID, measured.Model, formatUSD(measured.USD), groupedInt(measured.CacheWriteTokens))
	return nil
}

// floorRole reads --model as a Role name. It is deliberately not a free
// string: model.Role is a closed set of two (see model.Role's own doc), and
// a Client can only ever be asked to price one of them.
func floorRole(name string) (model.Role, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "explore":
		return model.RoleExplore, nil
	case "plan", "":
		return model.RolePlan, nil
	default:
		return "", contract.Fail(contract.FailureInvalidInput,
			"floor measure: --model must be explore or plan, got %q", name)
	}
}

// floorTurnShape returns the --mcp-config and builtin tools a real step of
// role would carry, because Client.Floor prices exactly the shape it is
// handed -- see Client.Floor's own doc: "the same --mcp-config a real step
// would carry". Explore reads the repository through Atenea's own tools and
// the CLI's Read and Glob (see internal/agent/planner's explore, which this
// mirrors); plan reasons over an exploration already in its prompt and
// calls no tool at all, so it carries neither.
func floorTurnShape(role model.Role) (tools string, builtins []string, err error) {
	switch role {
	case model.RoleExplore:
		tools, err = model.AteneaTools()
		if err != nil {
			return "", nil, err
		}
		return tools, []string{"Read", "Glob"}, nil
	case model.RolePlan:
		return "", nil, nil
	default:
		return "", nil, contract.Fail(contract.FailureInvalidInput,
			"floor measure: role %q is not explore or plan", role)
	}
}

// formatUSD renders a dollar figure the way every other money line in this
// CLI does (see workflow.go's stepCost and main.go's charged/overspent
// lines): two places, because a figure a person has to act on in the next
// sentence is not helped by a third and fourth digit of a number the CLI
// itself only estimates.
func formatUSD(usd float64) string {
	return fmt.Sprintf("$%.2f", usd)
}

// formatAge renders how long ago a measurement was taken, at the grain the
// operator actually has to weigh it at: hours while it is fresh enough that
// a hurried skim distinguishes "this morning" from "last week", days once it
// is not. Never a bare duration string like "3h4m12s" -- nobody deciding
// whether to re-measure needs the minutes.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 0 {
			return "less than an hour ago"
		}
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// groupedInt writes an integer with thousands separators, the same way
// internal/workflow's own grouped() renders a token count in a refusal: as a
// quantity a reader weighs at a glance rather than a serial number. Kept as
// a second small copy rather than exporting workflow's -- this package does
// not otherwise import internal/workflow, and reaching into it for ten lines
// of digit grouping is not a dependency worth adding.
func groupedInt(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	if len(digits) <= 3 {
		return sign + digits
	}
	var b strings.Builder
	b.Grow(len(sign) + len(digits) + (len(digits)-1)/3)
	b.WriteString(sign)
	head := len(digits) % 3
	if head == 0 {
		head = 3
	}
	b.WriteString(digits[:head])
	for i := head; i < len(digits); i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}
