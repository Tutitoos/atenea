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
