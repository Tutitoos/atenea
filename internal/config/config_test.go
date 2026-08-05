package config_test

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The embedded settings are what a fresh install boots on, so they have to be
// valid and they have to carry the P0 catalog.
func TestBuiltInDefaultsAreValid(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if cfg.Source != config.BuiltIn {
		t.Errorf("Source = %q", cfg.Source)
	}
	if cfg.Core.ShutdownGrace != 10*time.Second {
		t.Errorf("ShutdownGrace = %v", cfg.Core.ShutdownGrace)
	}
	// By name, not by count, and the same for the implementations below: a
	// bare number says nothing about which entry went missing when it changes.
	ids := make([]string, len(cfg.Capabilities))
	for i, capability := range cfg.Capabilities {
		ids[i] = capability.ID
	}
	slices.Sort(ids)
	wantIDs := []string{"code.impact", "code.search", "symbol.calls", "symbol.definition", "symbol.implementations", "symbol.references"}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("capabilities = %v, want %v", ids, wantIDs)
	}

	// The symbol capabilities and code.impact are read-only providers: none
	// of them may ship declaring an effect that lets a provider write. Only
	// code.search also spawns a process to answer -- every implementation
	// behind it, ripgrep or the local stand-in, is a binary, not a library.
	for _, capability := range cfg.Capabilities {
		want := []contract.Effect{contract.EffectRead}
		if capability.ID == "code.search" {
			want = append(want, contract.EffectProcess)
		}
		if !slices.Equal(capability.Effects, want) {
			t.Errorf("%s effects = %v, want %v", capability.ID, capability.Effects, want)
		}
	}

	capability := cfg.Capabilities[0]
	// The output shape from the design: a list of records, each with a path and
	// a line number.
	matches := capability.Outputs[0]
	if matches.Name != "matches" || matches.Type != contract.TypeRecordList {
		t.Fatalf("outputs[0] = %+v", matches)
	}
	if err := capability.ValidateOutput(map[string]any{
		"matches": []any{map[string]any{"path": "main.go", "line": 1, "column": 1}},
	}); err != nil {
		t.Errorf("the declared output shape rejects a valid payload: %v", err)
	}

	shipped := make([]string, len(cfg.Implementations))
	for i, impl := range cfg.Implementations {
		shipped[i] = impl.ID
	}
	slices.Sort(shipped)
	want := []string{
		"claude.search",
		"codebase-memory.calls",
		"codebase-memory.impact",
		"codebase-memory.search",
		"ripgrep",
		"serena.definition",
		"serena.implementations",
		"serena.references",
		"serena.search",
	}
	if !slices.Equal(shipped, want) {
		t.Fatalf("implementations = %v, want %v", shipped, want)
	}
	// code.impact walks a git diff against a baseline: the one implementation
	// behind it has nothing to measure against without a repository under
	// version control.
	for _, impl := range cfg.Implementations {
		if impl.ID == "codebase-memory.impact" && !impl.Constraints.RequiresVCS {
			t.Errorf("codebase-memory.impact ships with requires_vcs=false, want true")
		}
	}
	// Nothing has been probed on a cold start, and pretending otherwise would
	// let the funnel trust a provider that may not even be installed.
	for _, impl := range cfg.Implementations {
		if impl.Health.State != contract.HealthUnknown {
			t.Errorf("%s ships with health %s, want unknown", impl.ID, impl.Health.State)
		}
		if impl.Cost.Samples != 0 {
			t.Errorf("%s ships with %d measurements, want none", impl.ID, impl.Cost.Samples)
		}
		// An estimate that failed to parse would leave the cost block empty and
		// nobody would notice until the selector started ranking on it.
		if impl.Cost.Estimated.Duration <= 0 || impl.Cost.Estimated.Tokens <= 0 {
			t.Errorf("%s ships without a cost estimate: %+v", impl.ID, impl.Cost.Estimated)
		}
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

const minimal = `
contract = "1.0.0"

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
path = "/srv/api"
languages = ["go"]
scale = "small"
vcs = "present"
`

func TestLoadReadsAFile(t *testing.T) {
	path := write(t, minimal)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source != path {
		t.Errorf("Source = %q", cfg.Source)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].ID != "api" {
		t.Fatalf("repositories = %+v", cfg.Repositories)
	}
	if cfg.Repositories[0].VCS != contract.VCSPresent {
		t.Errorf("VCS = %v, want present", cfg.Repositories[0].VCS)
	}
}

// A repository can pin its own Serena so the adapter never retargets the
// default endpoint for it. Empty stays empty: that is the "fall back and
// retarget" path, not a missing value.
func TestLoadReadsSerenaEndpoint(t *testing.T) {
	body := `
contract = "1.0.0"

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
path = "/srv/api"
languages = ["go"]

[[repository]]
id = "web"
path = "/srv/web"
languages = ["typescript"]
serena_endpoint = "http://127.0.0.1:9121/mcp"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(cfg.Repositories))
	}
	var web *contract.Repository
	for i := range cfg.Repositories {
		if cfg.Repositories[i].ID == "web" {
			web = &cfg.Repositories[i]
		}
		if cfg.Repositories[i].ID == "api" && cfg.Repositories[i].SerenaEndpoint != "" {
			t.Errorf("api serena_endpoint = %q, want empty", cfg.Repositories[i].SerenaEndpoint)
		}
	}
	if web == nil {
		t.Fatal("web repository missing")
	}
	if web.SerenaEndpoint != "http://127.0.0.1:9121/mcp" {
		t.Errorf("web serena_endpoint = %q", web.SerenaEndpoint)
	}
}

func TestBrokenSerenaEndpointIsRefused(t *testing.T) {
	bad := strings.Replace(minimal, "vcs = \"present\"\n", "vcs = \"present\"\nserena_endpoint = \"localhost:9121\"\n", 1)
	_, err := config.Load(write(t, bad))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err=%v)", got, err)
	}
}

// A typo that is silently ignored is a setting the user believes is in force
// and is not.
func TestUnknownKeysAreRefused(t *testing.T) {
	path := write(t, minimal+"\n[core]\nshutdwn_grace = \"5s\"\n")
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("err = %v", err)
	}
}

func TestContractVersionIsEnforced(t *testing.T) {
	cases := map[string]string{
		"missing":     strings.Replace(minimal, `contract = "1.0.0"`, "", 1),
		"unparseable": strings.Replace(minimal, `contract = "1.0.0"`, `contract = "one"`, 1),
		"too new":     strings.Replace(minimal, `contract = "1.0.0"`, `contract = "9.0.0"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(write(t, body)); err == nil {
				t.Fatal("expected an error")
			} else if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("kind = %v", contract.KindOf(err))
			}
		})
	}
}

func TestBrokenCatalogueEntriesAreRefused(t *testing.T) {
	cases := map[string]string{
		"unknown effect":   strings.Replace(minimal, `effects = ["read"]`, `effects = ["device"]`, 1),
		"unknown type":     strings.Replace(minimal, `type = "string"`, `type = "float"`, 1),
		"unknown scale":    strings.Replace(minimal, `scale = "small"`, `scale = "huge"`, 1),
		"unknown vcs":      strings.Replace(minimal, `vcs = "present"`, `vcs = "sideways"`, 1),
		"bad duration":     minimal + "\n[implementation.cost]\nestimated_duration = \"soon\"\n",
		"negative tokens":  minimal + "\n[implementation.cost]\nestimated_tokens = -1\n",
		"unknown health":   minimal + "\n[implementation.health]\nstate = \"sick\"\n",
		"bad grace period": minimal + "\n[core]\nshutdown_grace = \"-5s\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(write(t, body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// A fresh install must boot. A file the user explicitly named and that is not
// there must not be papered over.
func TestMissingFileFallsBackOnlyWhenImplicit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATENEA_CONFIG", "")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("implicit load: %v", err)
	}
	if cfg.Source != config.BuiltIn {
		t.Errorf("Source = %q, want the built-in defaults", cfg.Source)
	}

	missing := filepath.Join(t.TempDir(), "nope.toml")
	if _, err := config.Load(missing); contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("explicit load: kind = %v, want not_found", contract.KindOf(err))
	}
}

func TestResolvePathPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ATENEA_CONFIG", "/from/env.toml")

	if got := config.ResolvePath("/explicit.toml"); got != "/explicit.toml" {
		t.Errorf("explicit path lost: %q", got)
	}
	if got := config.ResolvePath(""); got != "/from/env.toml" {
		t.Errorf("env ignored: %q", got)
	}
	t.Setenv("ATENEA_CONFIG", "")
	if want := filepath.Join(dir, "atenea", "atenea.toml"); config.ResolvePath("") != want {
		t.Errorf("xdg path = %q, want %q", config.ResolvePath(""), want)
	}
}

func TestWriteDefaultRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "atenea.toml")
	if err := config.WriteDefault(path, false); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	if err := config.WriteDefault(path, false); err == nil {
		t.Fatal("overwriting without --force should fail")
	}
	if err := config.WriteDefault(path, true); err != nil {
		t.Fatalf("WriteDefault --force: %v", err)
	}
	written, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The point is that what was written reads back as what shipped, not that
	// either of them has a particular size.
	shipped, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if len(written.Implementations) != len(shipped.Implementations) {
		t.Fatalf("implementations = %d, want the %d that ship",
			len(written.Implementations), len(shipped.Implementations))
	}
}

// ---------------------------------------------------------------------------
// The orchestrator and security blocks
// ---------------------------------------------------------------------------

// A file that says nothing about the agent still has to boot into something
// usable: a fresh install has no reason to know these knobs exist.
func TestTheOrchestratorHasWorkingDefaults(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Orchestrator.Runners; len(got) != 1 || got[0] != config.RunnerOMP {
		t.Errorf("runners = %v, want just the adapter that ships", got)
	}
	// Claude Code is a first-class client, but it is the only far side that
	// costs money per call, so a fresh install must not have it attached.
	if slices.Contains(cfg.Orchestrator.Runners, config.RunnerClaudeCode) {
		t.Error("a fresh install would start spending without being asked")
	}
	if cfg.Orchestrator.BudgetUSD <= 0 {
		t.Error("a ceiling of zero would let a commission run away")
	}
	if cfg.Orchestrator.ClaudeCode.Timeout <= cfg.Orchestrator.OMP.Timeout {
		t.Error("a model turn given a tool's patience will be cut off mid-thought")
	}
	if cfg.Orchestrator.OMP.Binary == "" {
		t.Error("the adapter has no command to run")
	}
	if len(cfg.Orchestrator.OMP.Implementations) == 0 {
		t.Error("the adapter serves nothing, so nothing could ever be dispatched")
	}
	if cfg.Orchestrator.OMP.MatchLimit <= 0 {
		t.Error("a ceiling of zero is the one omp reads as a small default")
	}
	if cfg.Orchestrator.OMP.Timeout <= 0 {
		t.Error("without a timeout a stuck omp would never fall back")
	}
	if cfg.Orchestrator.MaxParallel <= 0 {
		t.Errorf("max_parallel = %d, want a real ceiling by default", cfg.Orchestrator.MaxParallel)
	}
	if cfg.Orchestrator.CheckpointDir == "" {
		t.Error("a fresh install writes its receipts somewhere")
	}
	if len(cfg.Orchestrator.Local.Implementations) == 0 {
		t.Error("the stand-in serves nothing, so nothing could ever be dispatched")
	}
	if len(cfg.Orchestrator.Local.SkipDirs) == 0 {
		t.Error("a walk with no skip list would descend into .git")
	}
	// Not declaring a secret is not the same as declaring there are none.
	if len(cfg.Security.Sensitive) == 0 {
		t.Error("a file that says nothing about secrets must still protect them")
	}
}

func TestTheOrchestratorBlockIsRead(t *testing.T) {
	body := minimal + `
[orchestrator]
max_parallel = 2
runners = []
checkpoint_dir = "/tmp/receipts"

  [orchestrator.local]
  implementations = ["ripgrep", "serena.search"]
  skip_dirs = ["vendor"]

[security]
sensitive = ["*.pem"]
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Orchestrator.MaxParallel != 2 || len(cfg.Orchestrator.Runners) != 0 {
		t.Errorf("orchestrator = %+v", cfg.Orchestrator)
	}
	if cfg.Orchestrator.CheckpointDir != "/tmp/receipts" {
		t.Errorf("checkpoint_dir = %q", cfg.Orchestrator.CheckpointDir)
	}
	if len(cfg.Orchestrator.Local.Implementations) != 2 {
		t.Errorf("implementations = %v", cfg.Orchestrator.Local.Implementations)
	}
	if len(cfg.Orchestrator.Local.SkipDirs) != 1 {
		t.Errorf("skip_dirs = %v", cfg.Orchestrator.Local.SkipDirs)
	}
	// A declared list REPLACES the shipped one. Merging would make it
	// impossible to ever narrow the guard, and silently widen what the user
	// thought they had pinned down.
	if len(cfg.Security.Sensitive) != 1 || cfg.Security.Sensitive[0] != "*.pem" {
		t.Errorf("sensitive = %v, want exactly what the file declared", cfg.Security.Sensitive)
	}
}

// An empty list is a deliberate statement -- "nothing here is secret" -- and
// has to survive as one rather than being mistaken for an omission.
func TestAnEmptySensitiveListDisarmsTheGuardOnPurpose(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[security]\nsensitive = []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Security.Sensitive) != 0 {
		t.Errorf("sensitive = %v, want the empty list the user asked for", cfg.Security.Sensitive)
	}
}

func TestAnUnknownRunnerIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\nrunners = [\"magic\"]\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// A name written twice would build the same adapter again and then collide
// with itself over every implementation it serves.
func TestARunnerListedTwiceIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\nrunners = [\"omp\", \"omp\"]\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// The singular spelling is gone. Accepting it silently would leave a settings
// file that reads as if it configured something and does not.
func TestTheOldSingularRunnerKeyIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\nrunner = \"omp\"\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

func TestTheClaudeCodeBlockIsRead(t *testing.T) {
	body := minimal + `
[orchestrator]
runners = ["claudecode"]

  [orchestrator.claudecode]
  binary = "/opt/bin/claude"
  implementations = ["claude.search"]
  timeout = "2m"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	claude := cfg.Orchestrator.ClaudeCode
	if claude.Binary != "/opt/bin/claude" || claude.Timeout != 2*time.Minute {
		t.Errorf("claudecode = %+v", claude)
	}
}

// The ceiling is the commission's, so it is read from the commission's block.
func TestTheCommissionCeilingIsRead(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[orchestrator]\nbudget_usd = 1.5\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Orchestrator.BudgetUSD != 1.5 {
		t.Errorf("budget_usd = %v, want 1.5", cfg.Orchestrator.BudgetUSD)
	}
}

// The ceiling used to live on the adapter, where it capped one call and a
// four-step run spent it four times. A settings file still carrying it must
// not load quietly with the key ignored: that would be the old number sitting
// in the file looking effective while the real one came from somewhere else.
func TestTheOldAdapterCeilingIsRefused(t *testing.T) {
	body := minimal + "\n[orchestrator]\n  [orchestrator.claudecode]\n  budget_usd = 0.25\n"
	_, err := config.Load(write(t, body))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
	if !strings.Contains(err.Error(), "budget_usd") {
		t.Errorf("the error does not name the key that moved: %v", err)
	}
}

// Zero reads as "no ceiling" everywhere else in this file, and it is the one
// value a spending cap must not accept. A grant that reaches zero while a
// commission runs is a different thing and perfectly ordinary; this is
// somebody typing it, which is always a mistake.
func TestAZeroBudgetIsRefused(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		_, err := config.Load(write(t, minimal+"\n[orchestrator]\nbudget_usd = "+value+"\n"))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Fatalf("budget_usd = %s: kind = %v, want invalid_input", value, got)
		}
	}
}

// The standing grant is read the same way the budget ceiling is: from the
// orchestrator's own block, added to every commission and question this
// core dispatches from here on.
func TestStandingEffectsAreRead(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[orchestrator]\neffects = [\"process\", \"write\"]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []contract.Effect{contract.EffectProcess, contract.EffectWrite}
	if !slices.Equal(cfg.Orchestrator.StandingEffects, want) {
		t.Errorf("effects = %v, want %v", cfg.Orchestrator.StandingEffects, want)
	}
}

// Omitting the block is the common case and must not read as a typo further
// down: no standing grant beyond the read every commission already has for
// free.
func TestNoStandingEffectsIsTheZeroValue(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Orchestrator.StandingEffects) != 0 {
		t.Errorf("effects = %v, want none", cfg.Orchestrator.StandingEffects)
	}
}

func TestAnUnknownStandingEffectIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\neffects = [\"ghost\"]\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
	if !strings.Contains(err.Error(), "orchestrator.effects") {
		t.Errorf("the error does not name the key that rejected it: %v", err)
	}
}

func TestANegativeCeilingIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\nmax_parallel = -1\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// Zero is not a typo for one: it is how an operator lifts the ceiling on a
// machine that can take it.
func TestAZeroCeilingLiftsTheLimit(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[orchestrator]\nmax_parallel = 0\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Orchestrator.MaxParallel != 0 {
		t.Errorf("max_parallel = %d, want the uncapped 0", cfg.Orchestrator.MaxParallel)
	}
}

func TestTheOMPBlockIsRead(t *testing.T) {
	body := minimal + `
[orchestrator]

  [orchestrator.omp]
  binary = "/opt/omp/bin/omp"
  implementations = ["ripgrep", "serena.search"]
  match_limit = 25
  timeout = "90s"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	adapter := cfg.Orchestrator.OMP
	if adapter.Binary != "/opt/omp/bin/omp" {
		t.Errorf("binary = %q", adapter.Binary)
	}
	if len(adapter.Implementations) != 2 {
		t.Errorf("implementations = %v", adapter.Implementations)
	}
	if adapter.MatchLimit != 25 {
		t.Errorf("match_limit = %d", adapter.MatchLimit)
	}
	if adapter.Timeout != 90*time.Second {
		t.Errorf("timeout = %s", adapter.Timeout)
	}
}

// Zero reads like "no limit" and is the one value omp treats as "use a small
// default and call the short answer complete", so it cannot be accepted here.
func TestAnUnusableMatchCeilingIsRefused(t *testing.T) {
	for _, limit := range []string{"0", "-1"} {
		_, err := config.Load(write(t, minimal+"\n[orchestrator.omp]\nmatch_limit = "+limit+"\n"))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("match_limit = %s -> %v, want invalid_input", limit, got)
		}
	}
}

func TestAnUnusableAdapterTimeoutIsRefused(t *testing.T) {
	for _, timeout := range []string{"never", "0s", "-5s"} {
		_, err := config.Load(write(t, minimal+"\n[orchestrator.omp]\ntimeout = \""+timeout+"\"\n"))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("timeout = %q -> %v, want invalid_input", timeout, got)
		}
	}
}

func TestAnUnknownKeyInTheNewBlocksIsRefused(t *testing.T) {
	// The misspellings below are the subject of the test, not an accident.
	for _, body := range []string{
		"\n[orchestrator]\nmax_paralel = 2\n",            //nolint:misspell // deliberate typo
		"\n[orchestrator.local]\nimplementaitons = []\n", //nolint:misspell // deliberate typo
		"\n[orchestrator.omp]\nbinry = \"omp\"\n",
		"\n[orchestrator.omp]\nmatch_limt = 5\n",
		"\n[security]\nsensitiv = []\n",
	} {
		_, err := config.Load(write(t, minimal+body))
		if err == nil || !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("%q was accepted: %v", strings.TrimSpace(body), err)
		}
	}
}

// ---------------------------------------------------------------------------
// The measurement base
// ---------------------------------------------------------------------------

// A fresh install measures. The baseline is what the funnel ranks on, so a
// file that says nothing about metrics must still produce a working store.
func TestTheMetricsBaseHasWorkingDefaults(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.Metrics
	if !m.Enabled {
		t.Error("a fresh install is not measuring anything")
	}
	if m.Path == "" {
		t.Error("the store has nowhere to live")
	}
	if m.Flush <= 0 || m.Compact <= 0 {
		t.Errorf("the rhythms are not running: flush=%v compact=%v", m.Flush, m.Compact)
	}
	if m.BufferLimit <= 0 {
		t.Errorf("buffer_limit = %d, want a real ceiling", m.BufferLimit)
	}
}

func TestTheMetricsBlockIsRead(t *testing.T) {
	body := minimal + `
[metrics]
path = "/tmp/atenea-test/base.duckdb"
enabled = true
flush = "10s"
compact = "2h"
buffer_limit = 25
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.Metrics
	if m.Path != "/tmp/atenea-test/base.duckdb" {
		t.Errorf("path = %q", m.Path)
	}
	if m.Flush != 10*time.Second {
		t.Errorf("flush = %v, want 10s", m.Flush)
	}
	if m.Compact != 2*time.Hour {
		t.Errorf("compact = %v, want 2h", m.Compact)
	}
	if m.BufferLimit != 25 {
		t.Errorf("buffer_limit = %d, want 25", m.BufferLimit)
	}
}

// Switching measuring off is a real choice and has to survive as one: the core
// still runs, it simply learns nothing.
func TestMeasuringCanBeSwitchedOff(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[metrics]\nenabled = false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.Enabled {
		t.Fatal("enabled = false was ignored")
	}
}

// A rhythm of zero is not "off", it is a beat that never lands or one that
// never stops. Off is spelled `enabled = false`, in one place.
func TestAnUnusableRhythmIsRefused(t *testing.T) {
	for _, body := range []string{
		"\n[metrics]\nflush = \"0s\"\n",
		"\n[metrics]\nflush = \"-1s\"\n",
		"\n[metrics]\nflush = \"soon\"\n",
		"\n[metrics]\ncompact = \"0s\"\n",
		"\n[metrics]\ncompact = \"-1h\"\n",
		"\n[metrics]\nbuffer_limit = 0\n",
		"\n[metrics]\nbuffer_limit = -5\n",
	} {
		_, err := config.Load(write(t, minimal+body))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("%q gave %v, want invalid_input", strings.TrimSpace(body), got)
		}
	}
}

func TestAnUnknownMetricsKeyIsRefused(t *testing.T) {
	//nolint:misspell // the misspelling is the subject of the test
	_, err := config.Load(write(t, minimal+"\n[metrics]\nbuffer_limt = 10\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("a typo in the metrics block was accepted: %v", err)
	}
}

// Six hours and five copies are the design's numbers, not a guess. A default
// that drifted to a day would leave a whole working day unprotected, and one
// that kept a single copy would mean a corrupted base copied over the only
// good snapshot destroys the history it was meant to save.
func TestTheShippedBackupRhythmIsSixHoursAndFiveCopies(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if !cfg.Backup.Enabled {
		t.Error("a fresh install would keep no copies at all")
	}
	if cfg.Backup.Every != 6*time.Hour {
		t.Errorf("every = %v, want 6h", cfg.Backup.Every)
	}
	if cfg.Backup.Keep != 5 {
		t.Errorf("keep = %d, want 5", cfg.Backup.Keep)
	}
}

// Keeping zero copies is not "copying is off", it is a rotation that deletes
// the snapshot it just took. The two are different intents and the file has a
// separate word for the other one, so the nonsense must be refused rather than
// quietly read as the sane one next to it.
func TestARotationThatKeepsNothingIsRefused(t *testing.T) {
	for _, keep := range []string{"0", "-1"} {
		body := minimal + "\n[backup]\nkeep = " + keep + "\n"
		_, err := config.Load(write(t, body))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("keep = %s was answered with %v, want invalid_input", keep, got)
		}
		if err != nil && !strings.Contains(err.Error(), "enabled = false") {
			t.Errorf("the refusal does not point at the way to turn copying off: %v", err)
		}
	}
}

// Turning copying off is a sentence the operator can write, and it must not
// drag the rest of the block down with it: a disabled block with a rhythm
// still written in it is somebody who switched copies off for an afternoon.
func TestCopyingCanBeTurnedOffWithoutErasingTheBlock(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[backup]\nenabled = false\nevery = \"2h\"\nkeep = 9\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backup.Enabled {
		t.Error("copying stayed on after being switched off")
	}
	if cfg.Backup.Every != 2*time.Hour || cfg.Backup.Keep != 9 {
		t.Errorf("the block was erased: every = %v, keep = %d", cfg.Backup.Every, cfg.Backup.Keep)
	}
}

// Every endpoint the shipped settings reach over the network names an address,
// never a hostname. A proxy binds an address; a name is a question the machine
// answers, and it may answer it differently tomorrow -- `localhost` resolving
// to ::1 first is the default nearly everywhere, and a proxy listening only on
// 127.0.0.1 would start refusing connections with nothing on this side having
// changed. `localhost` is friendlier, which is exactly why somebody will try
// to tidy this into one. See docs/content/diagnosing-providers.md.
func TestTheShippedEndpointsNameAnAddressAndNeverAName(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	endpoint := cfg.Orchestrator.Serena.Endpoint
	host, err := hostOf(endpoint)
	if err != nil {
		t.Fatalf("serena endpoint %q: %v", endpoint, err)
	}
	if net.ParseIP(host) == nil {
		t.Fatalf("serena endpoint %q reaches the proxy by the name %q; pin the address it binds", endpoint, host)
	}
}

func hostOf(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return parsed.Hostname(), nil
}

// ---------------------------------------------------------------------------
// Serena as a managed process
// ---------------------------------------------------------------------------

// A file that says nothing about orchestrator.serena.process must leave
// Serena exactly as unmanaged as it has always been: reached over Endpoint,
// started by whatever started it.
func TestSerenaProcessIsNilByDefault(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Orchestrator.Serena.Process != nil {
		t.Errorf("Process = %+v, want nil when the file never mentions it", cfg.Orchestrator.Serena.Process)
	}
}

func TestTheSerenaProcessBlockIsRead(t *testing.T) {
	body := minimal + `
[orchestrator.serena.process]
command = "serena"
args = ["start-mcp-server", "--transport", "streamable-http", "--port", "{{port}}"]
env = ["SERENA_LOG_LEVEL=INFO"]
lifecycle = "on_demand"
port = 9121
restart_limit = 5
restart_delay = "2s"
stable_after = "45s"
ready_timeout = "20s"
idle_timeout = "10m"
stop_grace = "15s"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Orchestrator.Serena.Process
	if got == nil {
		t.Fatal("Process = nil, want the table the file declared")
	}
	want := config.ManagedProcess{
		Command:      "serena",
		Args:         []string{"start-mcp-server", "--transport", "streamable-http", "--port", "{{port}}"},
		Env:          []string{"SERENA_LOG_LEVEL=INFO"},
		Lifecycle:    supervisor.OnDemand,
		Port:         9121,
		RestartLimit: 5,
		RestartDelay: 2 * time.Second,
		StableAfter:  45 * time.Second,
		ReadyTimeout: 20 * time.Second,
		IdleTimeout:  10 * time.Minute,
		StopGrace:    15 * time.Second,
	}
	if got.Command != want.Command ||
		!slices.Equal(got.Args, want.Args) ||
		!slices.Equal(got.Env, want.Env) ||
		got.Lifecycle != want.Lifecycle ||
		got.Port != want.Port ||
		got.RestartLimit != want.RestartLimit ||
		got.RestartDelay != want.RestartDelay ||
		got.StableAfter != want.StableAfter ||
		got.ReadyTimeout != want.ReadyTimeout ||
		got.IdleTimeout != want.IdleTimeout ||
		got.StopGrace != want.StopGrace {
		t.Errorf("Process = %+v, want %+v", got, want)
	}
}

// The recovery knobs are the supervisor package's to default, not config's:
// a Spec built from a Process that never mentioned them must arrive at
// supervisor.Spec.withDefaults still zero, so there is exactly one place
// those numbers are decided rather than two that could drift apart.
func TestSerenaProcessOptionalTimingsStayZeroWhenOmitted(t *testing.T) {
	body := minimal + "\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"persistent\"\n"
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Orchestrator.Serena.Process
	if got == nil {
		t.Fatal("Process = nil, want the table the file declared")
	}
	if got.Port != 0 || got.RestartLimit != 0 || got.RestartDelay != 0 ||
		got.StableAfter != 0 || got.ReadyTimeout != 0 || got.IdleTimeout != 0 || got.StopGrace != 0 {
		t.Errorf("Process = %+v, want every unset knob left at zero for the supervisor to default", got)
	}
}

// A process table present without a command is an operator who opted in and
// then left out the one thing that makes the opt-in mean anything -- not the
// same as never having written the table at all.
func TestSerenaProcessRequiresACommand(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator.serena.process]\nlifecycle = \"on_demand\"\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("KindOf = %v, want invalid_input", got)
	}
	if !strings.Contains(err.Error(), "process.command") {
		t.Errorf("error %q does not name the missing field", err.Error())
	}
}

// Persistent and on_demand are two genuinely different behaviors -- always
// warm, or stopped when idle -- and there is no default to guess between
// them: a Process with Command set and Lifecycle empty or unrecognized must
// be refused rather than silently picking one.
func TestSerenaProcessLifecycleMustBeValid(t *testing.T) {
	for _, body := range []string{
		"\n[orchestrator.serena.process]\ncommand = \"serena\"\n",
		"\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"\"\n",
		"\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"always\"\n",
		"\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"Persistent\"\n",
	} {
		_, err := config.Load(write(t, minimal+body))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("%q gave %v, want invalid_input", strings.TrimSpace(body), got)
		}
	}
}

func TestSerenaProcessNumbersAreValidated(t *testing.T) {
	const header = "\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"on_demand\"\n"
	for _, body := range []string{
		header + "port = -1\n",
		header + "port = 70000\n",
		header + "restart_limit = -1\n",
		header + "restart_delay = \"soon\"\n",
		header + "restart_delay = \"0s\"\n",
		header + "restart_delay = \"-1s\"\n",
		header + "stable_after = \"0s\"\n",
		header + "ready_timeout = \"0s\"\n",
		header + "idle_timeout = \"0s\"\n",
		header + "stop_grace = \"0s\"\n",
	} {
		_, err := config.Load(write(t, minimal+body))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("%q gave %v, want invalid_input", strings.TrimSpace(body), got)
		}
	}
}

// A zero port is not an omission here -- it is the explicit "ask the OS for
// a free one" that most of these should use -- so it must be accepted even
// though every other number in this block treats zero as unset.
func TestSerenaProcessPortZeroIsAccepted(t *testing.T) {
	body := minimal + "\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"on_demand\"\nport = 0\n"
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Orchestrator.Serena.Process.Port != 0 {
		t.Errorf("Port = %d, want 0", cfg.Orchestrator.Serena.Process.Port)
	}
}
