package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
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
	if len(cfg.Capabilities) != 1 || cfg.Capabilities[0].ID != "code.search" {
		t.Fatalf("capabilities = %+v, want the single P0 capability", cfg.Capabilities)
	}

	capability := cfg.Capabilities[0]
	if len(capability.Effects) != 1 || capability.Effects[0] != contract.EffectRead {
		t.Errorf("effects = %v, want read", capability.Effects)
	}
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

	if len(cfg.Implementations) != 3 {
		t.Fatalf("implementations = %d, want 3", len(cfg.Implementations))
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
	if len(written.Implementations) != 3 {
		t.Fatalf("implementations = %d", len(written.Implementations))
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
	if cfg.Orchestrator.Runner != config.RunnerOMP {
		t.Errorf("runner = %q, want the adapter that ships", cfg.Orchestrator.Runner)
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
runner = "none"
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
	if cfg.Orchestrator.MaxParallel != 2 || cfg.Orchestrator.Runner != config.RunnerNone {
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
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\nrunner = \"magic\"\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
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
