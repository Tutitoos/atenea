package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/internal/agent/planner"
	"github.com/Tutitoos/atenea/internal/allowance"
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
	// every row's own CLIVersion is compared against below -- trimmed with
	// the same model.VersionToken a stored row was trimmed with when it was
	// written. Measured 2026-08-14: a row written seconds earlier still
	// listed "stale" because the stored side was trimmed ("2.1.232") and
	// this side was compared raw ("2.1.232 (Claude Code)") -- two different
	// strings for the same version. Trimming only one side of a comparison
	// is the same bug as trimming neither.
	running := model.VersionToken(toolversion.New(binary, "--version").Version(context.Background()))

	fmt.Fprintf(out, "%-20s %-9s %-16s %9s %9s %13s %13s %10s %-11s %s\n",
		"REPOSITORY", "AGENT", "MODEL", "WARM USD", "COLD USD", "PREFIX TOK",
		"1ST CALL TOK", "RESCUABLE", "CLI VERSION", "AGE")
	now := time.Now()
	stale := 0
	legacy := 0
	for _, m := range rows {
		if m.Agent == "" {
			// Put before Agent was part of the key (see Measurement's own
			// doc): it does not describe any agent that exists today, so
			// it is shown as needing re-measurement rather than printed
			// as if it still applied to whichever agent asks next.
			fmt.Fprintf(out, "%-20s %-9s needs re-measurement -- predates --agent; run "+
				"atenea floor measure --repo %s --agent NAME\n", m.Repository, "(none)", m.Repository)
			legacy++
			continue
		}
		version := m.CLIVersion
		if version == "" {
			version = "(unknown)"
		}
		mark := ""
		if running != "" && m.CLIVersion != "" && m.CLIVersion != running {
			mark = " stale"
			stale++
		}
		modelName := m.Model
		if modelName == "" {
			// filereader, reviewer and plan-check call no model at all (see
			// floorMeasureNoModel): their row prices a check that never ran
			// one, and a blank column would read as data lost rather than
			// as the fact it is.
			modelName = "(no model)"
		}
		// A row can carry a real USD and no PrefixTokens: it was Put
		// before the field existed, when a floor was stored as the cache
		// write of whichever probe measured it. Printing the zero would
		// put "0" in a token column beside a real dollar figure, which is
		// the same misreading the zero-floor rows were guarded against --
		// there, zero is the measurement; here, it is its absence.
		tokens := groupedInt(m.PrefixTokens)
		if m.PrefixTokens == 0 && m.USD > 0 {
			tokens = "(not recorded)"
		}
		// The smallest share that buys any reading at all -- see
		// allowance.MinShareUSD -- ceilinged to cents the same way a
		// refusal's own "needs" column is, so a person who types this
		// number is admitted.
		//
		// From the WARM weight, because that is what a step pays and what
		// `workflow create` refuses against, while COLD USD beside it is the
		// one-time cost of establishing the cache. Both now cover the SAME
		// span -- prefix plus the block arriving with the first tool call --
		// so the pair is a cache-state split and nothing else. Reading either
		// as the other is the mistake measured out on 2026-08-15.
		//
		// WarmStartWeight falls back to CacheWriteTokens the same way
		// Prefix does, so a legacy row missing PrefixTokens still prices
		// here. A dash, not the longer "(not recorded)" above, because a
		// mechanical row -- no model, so no first assistant event ever
		// fires -- is not a historical gap in what got recorded; the
		// column does not apply to it at all.
		rescuable := "-"
		if w := m.WarmStartWeight(); w > 0 {
			rescuable = formatUSD(math.Ceil(allowance.MinShareUSD(w)*100) / 100)
		}
		// WARM USD is what a step pays and COLD USD what establishing the
		// cache costs once -- see floor.Measurement.WarmUSD and ColdStartUSD.
		// Both are costs, so both round the way every other money line in this
		// CLI does; only RESCUABLE above ceilings, because it is a figure a
		// person types back as a share and rounding it down would print a
		// number that refuses them. A dash rather than "$0.00" where no probe
		// has priced the first tool call: the warm figure is unknown for that
		// row, and `workflow create` says so by falling back to the cold one
		// in its refusal.
		//
		// COLD USD is ColdStartUSD and not the stored USD, which is only the
		// PREFIX's slice of the receipt a first-call probe paid. Printing the
		// slice here understated a cold turn by 2.00x on explore and 9.84x on
		// reader (measured 2026-08-16 by paying both), and it did so beside a
		// WARM column that already covered the whole start -- so two columns
		// of one table had different spans and a reader comparing rows could
		// not tell which they had. Worse, a row with no first-call probe
		// agreed exactly, which put the error only on the rows measured by
		// the better instrument.
		//
		// Falls back to the stored figure when there is nothing to scale by:
		// a legacy row whose prefix is unknown has a real dollar amount and
		// no span to widen it over, and printing "$0.00" for it would lose
		// the measurement -- the same reason the token column above says
		// "(not recorded)" rather than "0".
		warm, firstCall := "-", "-"
		if w := m.WarmUSD(); w > 0 {
			warm = formatUSD(w)
		}
		if m.FirstCallTokens > 0 {
			firstCall = groupedInt(m.FirstCallTokens)
		}
		cold := m.ColdStartUSD()
		if cold == 0 {
			cold = m.USD
		}
		fmt.Fprintf(out, "%-20s %-9s %-16s %9s %9s %13s %13s %10s %-11s measured %s%s\n",
			m.Repository, m.Agent, modelName, warm, formatUSD(cold), tokens, firstCall,
			rescuable, version, formatAge(now.Sub(m.MeasuredAt)), mark)
	}
	if stale > 0 {
		fmt.Fprintf(out, "\n%d row(s) marked stale: the running CLI answers %q now, and the "+
			"system prompt and tool schemas ship WITH the CLI, so a different CLI is a "+
			"different floor even against the same repository, agent and model. Re-run "+
			"atenea floor measure to replace a stale row.\n", stale, running)
	}
	if legacy > 0 {
		fmt.Fprintf(out, "\n%d row(s) need re-measurement: they were measured before --agent was "+
			"part of the key and are not read as any agent's floor.\n", legacy)
	}
	return nil
}

// floorMeasure spends one real turn to price starting another one, and
// stores what it found -- unless the agent type calls no model at all, in
// which case nothing is spent; see floorMeasureNoModel.
func floorMeasure(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("floor measure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repoFlag := flags.String("repo", "", "repository id or path to measure (required)")
	agentFlag := flags.String("agent", "", "which declared agent type's tool surface to measure (required)")
	dryRun := flags.Bool("dry-run", false, "print what the probe would cost and spend nothing")
	confirm := flags.Bool("confirm", false, "ask at the terminal before spending the turn")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"floor measure takes no positional arguments, only --repo, --agent, --dry-run and --confirm: got %q",
			flags.Arg(0))
	}
	if strings.TrimSpace(*repoFlag) == "" {
		return contract.Fail(contract.FailureInvalidInput, "floor measure: --repo is required")
	}

	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	// --agent has no default, because the only thing a default could do here
	// is spend money on a type nobody named. It carried "plan" until
	// 2026-08-16, when `atenea floor measure --repo taxiprime-backend` --
	// typed to read this command's own warning text, expecting exactly the
	// refusal below -- silently priced a cold `plan` turn for $0.3487.
	//
	// This is not a spending policy being tightened; it is the removal of a
	// default that spends. Every other guard in this command refuses before
	// the money (--repo, an undeclared name, a path off the catalog), and a
	// missing agent type was the one hole in that set. The list is the
	// settings file's own answer, for the same reason AgentTypeByName gives
	// it: a caller one keystroke from the name should not be sent to a file
	// to find it.
	if strings.TrimSpace(*agentFlag) == "" {
		declared := make([]string, 0, len(cfg.Agents))
		for _, agent := range cfg.Agents {
			declared = append(declared, agent.Spec.Name)
		}
		if len(declared) == 0 {
			return contract.Fail(contract.FailureInvalidInput,
				"floor measure: --agent is required, and this settings file declares no agent type")
		}
		return contract.Fail(contract.FailureInvalidInput,
			"floor measure: --agent is required -- it spends a turn, so there is no default: declared are %s",
			strings.Join(declared, ", "))
	}

	// AgentTypeByName is the same lookup a workflow step gets when it names
	// its agent, refused the same way and for the same reason: a name
	// nobody declared is one keystroke from a typo, and "declared are ..."
	// is the settings file's own answer rather than a second list this
	// file would have to keep in sync with it.
	agentType, err := cfg.AgentTypeByName(strings.TrimSpace(*agentFlag))
	if err != nil {
		return err
	}
	agent := agentType.Spec.Name

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

	store, err := floor.Open("")
	if err != nil {
		return err
	}

	surface, callsModel := planner.SurfaceOf(agentKind(agentType))
	if !callsModel {
		return floorMeasureNoModel(store, repo.ID, agent, out)
	}

	// modelName is resolved the same way Client.Floor resolves it
	// internally (Options.Explore / Options.Plan, by Role) -- read here,
	// ahead of spending anything, only so the warning below can name the
	// triple and quote what it cost last time. Client.Floor's own returned
	// FloorMeasurement.Model is still what gets stored; this is not a
	// second source of truth, only an early read of the same one.
	modelName := strings.TrimSpace(cfg.Model.Explore)
	if surface.Role == model.RolePlan {
		modelName = strings.TrimSpace(cfg.Model.Plan)
	}
	if modelName == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"floor measure: no model is configured for role %q", surface.Role)
	}
	previous, hadPrevious, err := store.Get(repo.ID, agent, modelName)
	if err != nil {
		return err
	}

	// This is the one line every caller of this command has to see before
	// anything is spent: CALLING Client.Floor below spends real money, one
	// turn, priced at roughly the floor itself -- see Client.Floor's own
	// doc for why nothing calls it implicitly.
	//
	// It quotes the RECEIPT the probe will be billed, never the stored USD.
	// Those are different numbers by construction: a stored floor is the
	// prefix's slice of a receipt that also covered the block arriving with
	// the first tool call (see coldEquivalentUSD), so it is smaller than the
	// turn by exactly prefix/(prefix+first call). Measured 2026-08-16 by
	// paying it: this line said "~$0.14" and "~$0.05" for the two types on
	// taxiprime-backend, and the two turns cost ~$0.27 and ~$0.45 -- 1.9x and
	// 9.8x what it warned. An operator budgeting two probes off this line, as
	// one did, authorizes $0.19 and spends $0.72. The scaled figure is the one
	// the line below already prints AFTER the money is gone; a warning that
	// under-states the bill is worse than no warning, because it is acted on.
	if hadPrevious {
		fmt.Fprintf(out, "about to spend real money: one turn on %s as %s with %s -- "+
			"about %s, which is what the last probe of it was billed (it stores %s, "+
			"the prefix's share of that receipt)\n",
			repo.ID, agent, modelName, formatUSD(previous.ColdStartUSD()), formatUSD(previous.USD))
	} else {
		fmt.Fprintf(out, "about to spend real money: one turn on %s as %s with %s -- no previous "+
			"measurement, the amount is unknown\n", repo.ID, agent, modelName)
	}

	// The two ways out, and they come after the line above rather than before
	// it: the estimate is what an operator needs in order to answer, and it is
	// only known once the stored row has been read.
	//
	// --dry-run is the whole command except the turn -- it resolves the
	// repository, the agent type and the model, and refuses everything this
	// command refuses -- so what it prints is what the real run would price,
	// not a guess written beside it.
	//
	// --confirm is the same boundary `task`, `ask`, `decide --run` and `agent`
	// already offer, on the one command that spends by definition. It stays
	// opt-in for the same reason it is opt-in there: a prompt nobody asked for
	// blocks a script forever, and confirmTTY refuses outright when stdin is
	// not a terminal.
	if *dryRun {
		fmt.Fprintf(out, "--dry-run: nothing was spent and no row was stored. "+
			"Re-run without it, or with --confirm, to pay for the measurement.\n")
		return nil
	}
	if *confirm {
		estimate := 0.0
		if hadPrevious {
			estimate = previous.ColdStartUSD()
		}
		action := fmt.Sprintf("floor measure --repo %s --agent %s: one paid turn on %s",
			repo.ID, agent, modelName)
		if err := confirmTTY(out, action, estimate, []contract.Effect{contract.EffectExternal}); err != nil {
			return err
		}
	}

	client, err := model.New(model.Options(cfg.Model))
	if err != nil {
		return err
	}
	// Capabilities is a bool, not the config string, precisely so that a
	// surface can be inspected above without dialing the service; the
	// --mcp-config itself is only built for the one surface that actually
	// carries it, right before spending the turn it prices.
	var tools string
	if surface.Capabilities {
		tools, err = model.AteneaTools()
		if err != nil {
			return err
		}
	}
	// A surface with tools gets the probe that makes one tool call, because
	// that is what a real step does and where most of the money is: measured
	// 2026-08-15, the prefix is ~5,650 tokens and the block arriving with the
	// first tool call ~41,930. A surface with none cannot be asked to call
	// anything, and its prefix IS everything it pays -- so it gets the older
	// probe, and the row it writes carries no first-call figure because there
	// is none to carry.
	probe := client.FirstCall
	if len(surface.Builtins) == 0 && !surface.Capabilities {
		probe = client.Floor
	}
	measured, err := probe(context.Background(), model.FloorRequest{
		Role:     surface.Role,
		Dir:      repo.Path,
		Tools:    tools,
		Builtins: surface.Builtins,
	})
	if err != nil {
		return err
	}

	usd, usdPerToken, cold, pricedAt, err := coldEquivalentUSD(store, measured)
	if err != nil {
		return err
	}

	measuredAt := time.Now().UTC()
	stored := floor.Measurement{
		Repository:       repo.ID,
		Agent:            agent,
		Model:            measured.Model,
		USD:              usd,
		USDPerToken:      usdPerToken,
		PrefixTokens:     measured.PrefixTokens,
		FirstCallTokens:  measured.FirstCallTokens,
		Cold:             cold,
		CacheWriteTokens: measured.CacheWriteTokens,
		InputTokens:      measured.InputTokens,
		OutputTokens:     measured.OutputTokens,
		CLIVersion:       measured.CLIVersion,
		MeasuredAt:       measuredAt,
	}
	if err := store.Put(stored); err != nil {
		return err
	}

	note := ""
	if !cold {
		// The turn that ran just now only ever saw the cache-read price --
		// see coldEquivalentUSD -- so the figure printed above is not what
		// this probe was billed, and saying so is the difference between a
		// reader trusting a number and one who can tell you why it can be
		// trusted.
		note = fmt.Sprintf(" -- this reading came back warm, priced from the %s cold measurement taken on %s",
			measured.Model, pricedAt.Format("2006-01-02"))
	}
	fmt.Fprintf(out, "starting a turn on %s as %s with %s costs ~%s cold (%s prefix tokens: system "+
		"prompt and tool definitions, before any file is read)%s\n",
		repo.ID, agent, measured.Model, formatUSD(usd), groupedInt(measured.PrefixTokens), note)
	if measured.FirstCallTokens > 0 {
		// The number the admission rule actually charges, and the one the
		// prefix figure above was standing in for until this probe existed.
		fmt.Fprintf(out, "its first tool call brings %s more tokens, so a step starts at ~%s warm "+
			"and ~%s on whichever run of the hour establishes the cache\n",
			groupedInt(measured.FirstCallTokens), formatUSD(stored.WarmUSD()),
			formatUSD(stored.ColdStartUSD()))
	}
	return nil
}

// agentKind reads the built-in name a declared agent type dispatches to:
// args[1] of "agent-exec <kind>", the same key planner.SurfaceOf reads by
// (see its own doc). A repository's own type that borrows a shipped command
// with `runs` carries the same Args, so this resolves it to the shipped
// type's surface without this file needing to know local overlays exist; a
// type whose command is not $atenea, or that declares no args, answers ""
// and SurfaceOf reads that as "calls no model" rather than as a match.
func agentKind(a config.AgentType) string {
	if len(a.Args) < 2 || a.Args[0] != "agent-exec" {
		return ""
	}
	return a.Args[1]
}

// floorMeasureNoModel stores a zero floor for an agent type SurfaceOf says
// calls no model at all -- filereader, reviewer and plan-check are
// deterministic Go on the far side of the spawn (see cmd/atenea/agent.go's
// dispatch and planner.SurfaceOf's own doc), so there is no turn to price,
// and calling Client.Floor for one of them would price a turn that agent
// type never runs.
//
// The row is written anyway, at zero, rather than left absent: floorList's
// only way to tell "checked, costs nothing" from "never measured" is a row
// on disk, and the legacy-agent handling above already treats an absent
// row as needing attention rather than as a quiet zero.
func floorMeasureNoModel(store *floor.Store, repository, agent string, out io.Writer) error {
	if err := store.Put(floor.Measurement{
		Repository: repository,
		Agent:      agent,
		Cold:       true,
		MeasuredAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s as %s calls no model, so starting it costs nothing\n", repository, agent)
	return nil
}

// coldEquivalentUSD turns one Floor probe into the cold-equivalent price a
// stored floor is defined as (see internal/floor.Measurement's own doc):
// prefix tokens times a price per token, because PrefixTokens is the one
// quantity that does not move with cache state -- see
// model.FloorMeasurement.PrefixTokens for the same 26,603-both-times
// reading this is built on.
//
// A cold probe prices itself: the receipt IS the price of the tokens it just
// wrote, so USDPerToken is read straight back out of what was paid. The
// denominator is every token the receipt covers -- prefix AND the block that
// arrived with the first tool call, when the probe made one -- because
// dividing a two-message receipt by one message's tokens inflates the rate by
// exactly the ratio between them. Measured 2026-08-15, that ratio is 8.4x, and
// the wrong denominator turned a $0.02 warm step into $0.21 and a $0.49 cold
// start into $4.16 before this line said so. The rate this now derives,
// $1.04e-5 per token, agrees to 2% with the one an earlier prefix-only probe
// measured independently ($1.01e-5), which is what says the arithmetic is
// right rather than merely self-consistent.
//
// A warm probe has no receipt to divide -- the CLI billed only the
// cache-read price of tokens somebody else already wrote -- so it borrows
// USDPerToken from the most recent turn on the same model that WAS measured
// cold, wherever that happened: price is a property of the model, not of
// one repository's surface (see Store.PriceForModel). No cold row for that
// model anywhere is refused rather than guessed at: waiting roughly an hour
// with no probe ages the warm entry out, and the next reading is cold
// again.
func coldEquivalentUSD(store *floor.Store, measured model.FloorMeasurement) (usd, usdPerToken float64, cold bool, pricedAt time.Time, err error) {
	// What the receipt paid for, which is the prefix alone only when the
	// probe made no tool call.
	paidFor := measured.PrefixTokens + measured.FirstCallTokens
	if measured.Cold {
		if paidFor == 0 {
			return 0, 0, false, time.Time{}, contract.Fail(contract.FailureInvalidInput,
				"floor probe: claude code wrote and read zero prefix tokens -- there is nothing to price")
		}
		rate := measured.USD / float64(paidFor)
		return float64(measured.PrefixTokens) * rate, rate, true, time.Time{}, nil
	}
	price, measuredAt, ok := store.PriceForModel(measured.Model)
	if !ok {
		return 0, 0, false, time.Time{}, contract.Fail(contract.FailureUnavailable,
			"floor measure: this reading came back warm (%s of its %s prefix tokens were already "+
				"cached) and %s has never been measured cold, so there is no price to convert its "+
				"tokens with -- wait for the cache entry to age out (roughly an hour with no probe of "+
				"this repository, agent and model; every probe refreshes it) and measure again",
			groupedInt(measured.CacheReadTokens), groupedInt(measured.PrefixTokens), measured.Model)
	}
	return float64(measured.PrefixTokens) * price, price, false, measuredAt, nil
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
