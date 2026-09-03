package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The status screen is where the sharp edge stays visible. A settings file that
// never named a client floor has one anyway -- a copy of the operator's -- and
// the copy moves whenever the original does. Printing two identical lists with
// nothing between them would show a decision that was never made, so the screen
// has to say which of the two it is looking at.

// floorSettings writes the fixture with an orchestrator policy in front of it.
// The table goes first because everything after it in the fixture is an array
// of tables, and a bare key landing inside one of those belongs to it.
func floorSettings(t *testing.T, policy string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.toml")
	const anchor = "contract = \"4.0.0\"\n"
	body := strings.Replace(settings, anchor, anchor+"\n[orchestrator]\n"+policy+"\n", 1)
	if body == settings {
		t.Fatal("the fixture no longer declares its contract on one line")
	}
	body += "\n[metrics]\npath = \"" + filepath.Join(dir, "base.duckdb") + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestTheScreenSaysWhenClientsInheritTheOperatorsFloor(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err := cli(t, "--config", floorSettings(t, "effects = [\"process\", \"write\"]"), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	line := lineWith(t, out, "clients")
	// Both halves matter. The list, because a screen that only said
	// "inherited" would make the reader go and find the other line; and the
	// note, because without it the reader has no way to tell a copy from a
	// choice, which is the only thing this line was added to answer.
	for _, want := range []string{"process", "write", "inherited"} {
		if !strings.Contains(line, want) {
			t.Errorf("clients line = %q, want it to mention %q", line, want)
		}
	}
}

func TestTheScreenDoesNotCallAWrittenFloorInherited(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := floorSettings(t, "effects = [\"process\", \"write\"]\nclient_effects = [\"process\"]")
	out, err := cli(t, "--config", path, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	line := lineWith(t, out, "clients")
	if strings.Contains(line, "inherited") {
		t.Errorf("clients line = %q: a floor the operator wrote is not inherited", line)
	}
	if strings.Contains(line, "write") {
		t.Errorf("clients line = %q: the operator's write is not the clients'", line)
	}
	if !strings.Contains(line, "process") {
		t.Errorf("clients line = %q, want it to name the floor that was written", line)
	}
	// The operator's own line is unchanged beside it, which is what makes the
	// two readable as two: a screen showing only the narrower one would look
	// like the console had been narrowed too.
	if standing := lineWith(t, out, "standing"); !strings.Contains(standing, "write") {
		t.Errorf("standing line = %q, want it to still name write", standing)
	}
}

// lineWith returns the one line of the screen that starts with a label, and
// fails loudly when there is none: a test that silently matched an empty
// string would pass for a screen that stopped printing the line at all.
func lineWith(t *testing.T, out, label string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label+" ") {
			return line
		}
	}
	t.Fatalf("no %q line on the screen:\n%s", label, out)
	return ""
}

// ---------------------------------------------------------------------------
// floor measure: generic --agent, cold-equivalent pricing
// ---------------------------------------------------------------------------

// measureSettings writes a settings file `floor measure` can run a real
// probe against: a repository that exists on disk (Client.Floor changes into
// it to spawn the CLI, and a chdir into a path that is not there fails the
// spawn outright, not the probe) and two declared agent types -- "plan",
// which calls a model with no tools so no Atenea service needs to be
// listening for the probe to run, and "reviewer", which calls no model at
// all. Both are copied from internal/config/default.toml's own shipped
// blocks, trimmed to what AgentType.Validate requires.
func measureSettings(t *testing.T, binary string) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := t.TempDir()
	const body = `
contract = "4.0.0"

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find text."
effects = ["read"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

[[repository]]
id = "api"
path = "REPO_PATH"
languages = ["go"]
scale = "small"

[model]
binary = "BINARY"
explore = "explore-model"
plan = "plan-model"

[[agent]]
name = "plan"
kind = "specialized"
summary = "Reads an exploration and returns a graph of agent steps as TOML"
command = "$atenea"
args = ["agent-exec", "plan"]
context = ["repository", "workspace"]
effects = ["read"]
max_duration = "10m"
max_tokens = 200000

  [[agent.result]]
  name = "plan"
  type = "string"

[[agent]]
name = "reviewer"
kind = "specialized"
summary = "Re-reads what another agent answered about and says whether the answer holds"
command = "$atenea"
args = ["agent-exec", "reviewer"]
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1
pool = "review"

  [[agent.result]]
  name = "checked"
  type = "int"
  required = true

[[agent]]
name = "reader"
kind = "specialized"
summary = "Reads files somebody already named and reports what they say"
command = "$atenea"
args = ["agent-exec", "reader"]
context = ["repository"]
effects = ["read"]
max_duration = "10m"
max_tokens = 200000

  [[agent.result]]
  name = "summary"
  type = "string"
`
	rendered := strings.NewReplacer("REPO_PATH", repoPath, "BINARY", binary).Replace(body)
	rendered += "\n[metrics]\npath = \"" + filepath.Join(dir, "base.duckdb") + "\"\n"
	path := filepath.Join(dir, "atenea.toml")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// floorFakeCLI stands in for the model CLI: it answers --version on its
// own, and otherwise answers exactly one single-shot turn with stdout,
// regardless of the rest of its argv -- the same shape
// internal/agent/model's own floorScript uses on Client.Floor's own tests.
func floorFakeCLI(t *testing.T, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo '2.1.232 (Claude Code)'; exit 0;; esac\n" +
		"cat <<'ENVELOPE'\n" + stdout + "\nENVELOPE\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake CLI: %v", err)
	}
	return binary
}

// floorColdReading is a clean cold probe: no tool call, and a real
// cache-write figure shaped after the 25,340-token floor
// internal/agent/model's own tests measure this feature against.
const floorColdReading = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":25340},
  "total_cost_usd":0.28,"num_turns":1}`

// floorWarmReading is the same prefix, read back at cache-read price
// instead of written fresh.
const floorWarmReading = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":25340,"cache_creation_input_tokens":0},
  "total_cost_usd":0.01,"num_turns":1}`

// A cold probe prices itself: USDPerToken is read straight back out of the
// receipt, and USD stored is exactly what the CLI billed.
func TestFloorMeasureColdReadingPricesItself(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, floorColdReading))
	out, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api", "--agent", "plan")
	if err != nil {
		t.Fatalf("floor measure: %v", err)
	}
	if strings.Contains(out, "warm") {
		t.Errorf("out = %q: a cold reading is not warm", out)
	}
	for _, want := range []string{"$0.28", "25,340 prefix tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want it to mention %q", out, want)
		}
	}
	store, err := floor.Open("")
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	got, ok, err := store.Get("api", "plan", "plan-model")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if !got.Cold {
		t.Errorf("Cold = %v, want true", got.Cold)
	}
	if got.PrefixTokens != 25340 {
		t.Errorf("PrefixTokens = %d, want 25340", got.PrefixTokens)
	}
	if want := 0.28 / 25340.0; got.USDPerToken < want-1e-9 || got.USDPerToken > want+1e-9 {
		t.Errorf("USDPerToken = %v, want %v", got.USDPerToken, want)
	}
}

// A warm reading with nothing ever measured cold on that model has no price
// to convert its tokens with, and is refused rather than guessed at.
func TestFloorMeasureWarmWithNoColdRowRefuses(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, floorWarmReading))
	_, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api", "--agent", "plan")
	if err == nil {
		t.Fatal("expected a refusal: nothing has ever been measured cold on plan-model")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
	msg := err.Error()
	for _, want := range []string{"warm", "never been measured cold", "wait", "hour"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
}

// A warm reading with a cold row on record for the same model prices its
// prefix tokens at that row's rate, and says the reading was priced from
// it, naming its date.
func TestFloorMeasureWarmWithAColdRowPricesFromIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, floorWarmReading))

	store, err := floor.Open("")
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	pricedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// Seeded against a different repository on purpose: PriceForModel reads
	// by model, across every repository and agent, because the price of a
	// token is a property of the model, not of one repository's surface.
	if err := store.Put(floor.Measurement{
		Repository:   "elsewhere",
		Agent:        "plan",
		Model:        "plan-model",
		USD:          0.28,
		USDPerToken:  0.28 / 25340.0,
		PrefixTokens: 25340,
		Cold:         true,
		MeasuredAt:   pricedAt,
	}); err != nil {
		t.Fatalf("seeding a cold row: %v", err)
	}

	out, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api", "--agent", "plan")
	if err != nil {
		t.Fatalf("floor measure: %v", err)
	}
	for _, want := range []string{"warm", "priced from", "2026-08-10", "$0.28"} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want it to mention %q", out, want)
		}
	}
	got, ok, err := store.Get("api", "plan", "plan-model")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Cold {
		t.Errorf("Cold = %v, want false: this probe read the prefix, it did not write it", got.Cold)
	}
	if got.PrefixTokens != 25340 {
		t.Errorf("PrefixTokens = %d, want 25340", got.PrefixTokens)
	}
	if want := 0.28; got.USD < want-1e-9 || got.USD > want+1e-9 {
		t.Errorf("USD = %v, want %v -- 25,340 prefix tokens at the seeded row's price", got.USD, want)
	}
}

// reviewer calls no model at all, so measuring it spends nothing and stores
// a zero floor rather than pricing a turn that agent type never runs. The
// fake CLI does not exist on disk at all here -- if floorMeasure ever tried
// to spend, the spawn itself would fail first, so a clean success proves
// nothing was attempted, not merely that nothing was billed.
func TestFloorMeasureReviewerSpendsNothing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, filepath.Join(t.TempDir(), "claude-does-not-exist"))
	out, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api", "--agent", "reviewer")
	if err != nil {
		t.Fatalf("floor measure --agent reviewer: %v", err)
	}
	if !strings.Contains(out, "calls no model") || !strings.Contains(out, "costs nothing") {
		t.Errorf("out = %q, want it to say reviewer calls no model and costs nothing", out)
	}
	store, err := floor.Open("")
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	got, ok, err := store.Get("api", "reviewer", "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("no row was stored for reviewer")
	}
	if got.USD != 0 || got.PrefixTokens != 0 || !got.Cold {
		t.Errorf("stored row = %+v, want USD 0, PrefixTokens 0, Cold true", got)
	}
}

// An agent name this settings file never declared is refused by name,
// listing what it does declare -- the same AgentTypeByName refusal a
// workflow step's own agent name gets.
func TestFloorMeasureUndeclaredAgentListsDeclaredOnes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, filepath.Join(t.TempDir(), "claude-does-not-exist"))
	_, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api", "--agent", "bogus")
	if err == nil {
		t.Fatal("expected a refusal: bogus is not declared")
	}
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", got, err)
	}
	msg := err.Error()
	for _, want := range []string{"bogus", "plan", "reviewer"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
}

// --agent carried a default of "plan" until 2026-08-16, which made the
// SHORTEST form of the only command here that spends unattended also the one
// that picks what to spend on. `atenea floor measure --repo taxiprime-backend`,
// typed to read this command's warning text and expecting this refusal, priced
// a cold plan turn for $0.3487.
//
// The fake CLI here ANSWERS. That is the point: the refusal has to come before
// the probe, so the assertion is that the store is still empty afterwards.
// Restore the default and this test fails on the row that appears, not on the
// message -- which is the failure the missing guard actually had.
func TestFloorMeasureWithNoAgentRefusesRatherThanPickingOne(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, floorColdReading))
	_, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api")
	if err == nil {
		t.Fatal("expected a refusal: --agent names what the turn is spent on")
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
	msg := err.Error()
	// The flag, the reason it has no default, and the settings file's own list
	// -- the same courtesy AgentTypeByName gives a mistyped name.
	for _, want := range []string{"--agent is required", "spends a turn", "plan", "reviewer"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
	store, err := floor.Open("")
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	for _, agent := range []string{"plan", "reader", "explore", "reviewer"} {
		if _, ok, err := store.Get("api", agent, "plan-model"); err != nil {
			t.Fatalf("Get: %v", err)
		} else if ok {
			t.Errorf("a row was written for %q: the refusal did not come before the probe", agent)
		}
	}
}

// COLD USD is what a cold turn is BILLED, which is the prefix and the block
// arriving with the first tool call -- the same span WARM USD prices. It showed
// the stored figure, the prefix's slice alone, so the two columns of one table
// had different scopes: measured 2026-08-16 by paying both, explore read 2.00x
// low and reader 9.84x low, while a row with no first-call probe agreed exactly.
// The error therefore appeared only on the rows measured by the better probe,
// which is the worst place for it -- a reader comparing two rows could not tell
// which quantity they had.
func TestTheFloorTableColdColumnIsTheWholeStartNotThePrefixsSlice(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := floor.Open("")
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	now := time.Now().UTC()
	// The two real rows, and a third with no first-call probe: that one has no
	// span to widen over and must print its stored figure unchanged.
	for _, row := range []floor.Measurement{
		{
			Repository: "taxiprime-backend", Agent: "explore", Model: "claude-opus-5",
			USD: 0.13600522528262304, USDPerToken: 5.291208577755332e-06,
			PrefixTokens: 25_704, FirstCallTokens: 25_778,
			InputTokens: 2, OutputTokens: 17, Cold: true, MeasuredAt: now,
		},
		{
			Repository: "taxiprime-backend", Agent: "reader", Model: "claude-opus-5",
			USD: 0.045499885632274126, USDPerToken: 1.0039692328392349e-05,
			PrefixTokens: 4_532, FirstCallTokens: 40_061,
			InputTokens: 2, OutputTokens: 17, Cold: true, MeasuredAt: now,
		},
		{
			Repository: "taxiprime-backend", Agent: "plan", Model: "claude-opus-5",
			USD: 0.3487, USDPerToken: 1.000315556958031e-05,
			PrefixTokens: 34_859, InputTokens: 2, OutputTokens: 4,
			Cold: true, MeasuredAt: now,
		},
	} {
		if err := store.Put(row); err != nil {
			t.Fatalf("seeding %s: %v", row.Agent, err)
		}
	}

	out, err := cli(t, "--config", floorSettings(t, ""), "floor")
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	// The rows start with the repository, so lineWith's line-start anchor does
	// not reach the agent column: matched on the agent surrounded by spaces,
	// which is unambiguous here because "reader" is not a substring of another
	// declared name in this fixture.
	rowFor := func(agent string) string {
		t.Helper()
		for line := range strings.SplitSeq(out, "\n") {
			if strings.Contains(line, " "+agent+" ") {
				return line
			}
		}
		t.Fatalf("no %q row on the screen:\n%s", agent, out)
		return ""
	}
	for _, want := range []struct{ agent, cold, wrong string }{
		{"explore", "$0.27", "$0.14"},
		{"reader", "$0.45", "$0.05"},
		{"plan", "$0.35", ""}, // no first-call probe: the stored figure IS the receipt
	} {
		line := rowFor(want.agent)
		if !strings.Contains(line, want.cold) {
			t.Errorf("%s row = %q, want COLD USD %s", want.agent, line, want.cold)
		}
		if want.wrong != "" && strings.Contains(line, want.wrong) {
			t.Errorf("%s row = %q still prints the prefix's slice %s", want.agent, line, want.wrong)
		}
	}
}

// firstCallReading is the event stream of a probe that made one tool call,
// with the two real figures measured 2026-08-15: a 5,647-token prefix and the
// 41,927-token block that arrives with the first tool result, billed $0.4935
// together.
//
// One JSON object per line, unlike the single-shot fixtures above: the CLI's
// stream-json output is read line by line, and a pretty-printed event is not
// an event.
const firstCallReading = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":2,"output_tokens":11,"cache_read_input_tokens":0,"cache_creation_input_tokens":5647}}}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":2,"output_tokens":11,"cache_read_input_tokens":0,"cache_creation_input_tokens":5647}}}
{"type":"user","subtype":"tool_result"}
{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":0,"cache_creation_input_tokens":41927}}}
{"type":"result","is_error":false,"subtype":"success","result":"ok","num_turns":3,"usage":{"input_tokens":5,"output_tokens":15,"cache_read_input_tokens":0,"cache_creation_input_tokens":47574},"total_cost_usd":0.4935}`

// The measurement that unblocked the floor, end to end through the command a
// person runs. Two properties, and the second is the one that was wrong when
// this path was first wired: the rate has to be derived from every token the
// receipt covers, not from the prefix alone. Divided by the prefix, the same
// receipt reported a $0.21 warm step and a $4.16 cold start -- 8.4x out, the
// ratio between the two messages.
func TestFloorMeasurePricesTheFirstToolCallAndDerivesTheRateFromBoth(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, firstCallReading))
	out, err := cli(t, "--config", settingsPath, "floor", "measure", "--repo", "api", "--agent", "reader")
	if err != nil {
		t.Fatalf("floor measure: %v", err)
	}
	for _, want := range []string{
		"5,647 prefix tokens",
		"its first tool call brings 41,927 more tokens",
		// The whole start, cold, is what the receipt itself said: this is the
		// arithmetic checking against the bill rather than against itself.
		"$0.49 on whichever run of the hour establishes the cache",
		"~$0.02 warm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want it to mention %q", out, want)
		}
	}

	store, err := floor.Open("")
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	got, ok, err := store.Get("api", "reader", "explore-model")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.PrefixTokens != 5647 || got.FirstCallTokens != 41927 {
		t.Errorf("stored prefix/first-call = %d/%d, want 5647/41927",
			got.PrefixTokens, got.FirstCallTokens)
	}
	if want := 0.4935 / 47574.0; got.USDPerToken < want-1e-9 || got.USDPerToken > want+1e-9 {
		t.Errorf("USDPerToken = %v, want %v -- the receipt over everything it paid for",
			got.USDPerToken, want)
	}
	// The stored USD stays the cold price of the PREFIX, which is what every
	// row before this probe existed carried and what floorList's own column
	// means.
	if want := 5647 * (0.4935 / 47574.0); got.USD < want-1e-9 || got.USD > want+1e-9 {
		t.Errorf("USD = %v, want %v", got.USD, want)
	}
	if w := got.WarmUSD(); w < 0.024 || w > 0.025 {
		t.Errorf("WarmUSD = %v, want ~$0.0247 -- the whole start read from cache", w)
	}
}

// This command spends by definition: it prices a turn by paying for one. It
// printed "about to spend real money" and then spent it in the same breath,
// with no --confirm, no prompt and no way to see the estimate without
// authorizing the bill -- while `task`, `ask`, `decide --run` and `agent`, all
// of which spend less predictably, have offered --confirm all along.
//
// The fake CLI here answers a full cold reading, so a --dry-run that leaked
// through would leave a stored row behind and this test would see it.
func TestFloorMeasureDryRunPricesTheProbeWithoutPayingForIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, floorColdReading))

	out, err := cli(t, "--config", settingsPath, "floor", "measure",
		"--repo", "api", "--agent", "plan", "--dry-run")
	if err != nil {
		t.Fatalf("floor measure --dry-run: %v", err)
	}
	if !strings.Contains(out, "about to spend real money") {
		t.Errorf("out = %q, want the estimate a reader is deciding on", out)
	}
	if !strings.Contains(out, "nothing was spent") {
		t.Errorf("out = %q, want it said plainly that no money left", out)
	}

	table, err := cli(t, "--config", settingsPath, "floor")
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	if !strings.Contains(table, "no floor has been measured yet") {
		t.Errorf("a --dry-run stored a row:\n%s", table)
	}
}

// --confirm is refused outright where nobody can answer it, rather than read
// as a yes. Stdin is replaced with a pipe here so the assertion is about the
// non-interactive path and not about whatever the suite was started with: a
// test that let confirmTTY reach a real terminal would sit waiting for a
// keystroke instead of failing.
func TestFloorMeasureConfirmRefusesWhereNobodyCanAnswer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsPath := measureSettings(t, floorFakeCLI(t, floorColdReading))

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = read
	t.Cleanup(func() {
		os.Stdin = stdin
		_ = read.Close()
		_ = write.Close()
	})

	_, err = cli(t, "--config", settingsPath, "floor", "measure",
		"--repo", "api", "--agent", "plan", "--confirm")
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	table, err := cli(t, "--config", settingsPath, "floor")
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	if !strings.Contains(table, "no floor has been measured yet") {
		t.Errorf("an unconfirmed measurement was paid for anyway:\n%s", table)
	}
}
