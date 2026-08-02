// Package config reads Atenea's single settings file.
//
// Atenea is a declarative engine: the catalog of capabilities, the
// implementations behind them, the repositories they run against and the user's
// selector rules all live in this file. Changing behavior means editing it,
// not the core.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/adapter/serena"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

//go:embed default.toml
var defaultSettings []byte

// BuiltIn is the Source of a config that came from the embedded defaults.
const BuiltIn = "built-in defaults"

// Config is the decoded, validated settings file.
type Config struct {
	// Source is the file it came from, or BuiltIn.
	Source string
	// Contract is the contract version the file targets.
	Contract        contract.Version
	Core            Core
	Orchestrator    Orchestrator
	Metrics         Metrics
	Security        Security
	Selector        selector.Config
	Capabilities    []contract.Capability
	Implementations []contract.Implementation
	Repositories    []contract.Repository
}

// Core holds the operational knobs.
type Core struct {
	// ShutdownGrace is how long a clean stop waits for in-flight work.
	ShutdownGrace time.Duration
}

// Metrics holds the measurement store and the rhythms that maintain it.
//
// The rhythms live here rather than in Go because the design keeps one clock
// for all of them: retuning a beat is meant to be a line in this file, not a
// rebuild, and having them side by side is what makes it obvious when two are
// set to collide.
type Metrics struct {
	// Path is the database file. Empty means metrics.DefaultPath.
	Path string
	// Enabled is false when the user has turned measuring off. A core that is
	// not measuring still works; it simply never learns what anything costs.
	Enabled bool
	// Flush is how often the buffered batch is pushed to disk on its own.
	// A phase closing and the process going down push it regardless: this is
	// the rhythm between those two, not the guarantee.
	Flush time.Duration
	// Compact is how often the retention ladder is walked.
	Compact time.Duration
	// BufferLimit caps how many measurements wait in memory when flushes are
	// failing. It is never zero once a file has been read: an omitted key
	// takes the store's own ceiling here, so the value downstream is always
	// the one the status screen shows.
	BufferLimit int
}

// Orchestrator holds the knobs of the agent that splits and hands out work.
type Orchestrator struct {
	// MaxParallel caps how many steps of one wave run at a time. Zero means no
	// ceiling. The real limit is the machine, and the machine is not the same
	// one everywhere, so it is declared rather than compiled in.
	MaxParallel int
	// CheckpointDir is where the paper copy of a run in flight is written. It
	// is empty when checkpointing is off.
	CheckpointDir string
	// Runners names the client adapters that are live, in the order they are
	// declared. It is a list and not a choice because omp and Claude Code are
	// both first-class: with one slot, whichever client lost would have every
	// implementation it serves permanently unreachable. An empty list leaves
	// the core able to plan and choose, with nothing to dispatch to.
	Runners    []string
	Local      LocalRunner
	OMP        OMPAdapter
	ClaudeCode ClaudeCodeAdapter
	Serena     SerenaAdapter
}

// LocalRunner configures the stand-in that runs when no client adapter is
// reachable on this machine.
type LocalRunner struct {
	// Implementations the stand-in answers for. Anything else is reported
	// unavailable, which is the bin that drives fallback.
	Implementations []string
	// SkipDirs are directory names never descended into.
	SkipDirs []string
}

// OMPAdapter configures the omp client adapter.
type OMPAdapter struct {
	// Binary is the omp executable. A bare name is looked up on PATH, which is
	// the normal case; a path covers an install somewhere unusual.
	Binary string
	// Implementations the adapter answers for.
	Implementations []string
	// MatchLimit caps how many matches one search asks omp for. omp always
	// caps, so the only choice is whether Atenea states the number or lets omp
	// pick one quietly.
	MatchLimit int
	// Timeout caps one omp invocation.
	Timeout time.Duration
}

// ClaudeCodeAdapter configures the Claude Code client adapter.
//
// It is off in the shipped defaults. Unlike every other far side here, this
// one costs money per invocation, and a fresh install that quietly started
// spending would be a bad surprise however good the answers were.
type ClaudeCodeAdapter struct {
	// Binary is the claude executable. A bare name is looked up on PATH.
	Binary string
	// Implementations the adapter answers for.
	Implementations []string
	// BudgetUSD caps what one invocation may spend.
	BudgetUSD float64
	// Timeout caps one invocation. A model turn is slower than a tool call by
	// nature, so this sits far above omp's ceiling.
	Timeout time.Duration
}

// SerenaAdapter configures the Serena adapter.
//
// Serena is the first far side that is not a CLI: it is an MCP server behind a
// local proxy. Nothing above this line changes because of that -- a capability
// does not care whether its provider is a command or a server, which is the
// point of having the seam at all.
type SerenaAdapter struct {
	// Endpoint is where the MCP server is listening.
	Endpoint string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. A language server indexing a cold repository is
	// slow long before it is stuck.
	Timeout time.Duration
}

// Security is the one place delicate files are declared.
type Security struct {
	// Sensitive holds the path patterns that carry secrets. Reading is free by
	// default; these are the exception, and while exploring they are skipped in
	// silence rather than turned into a question.
	Sensitive []string
}

// RunnerOMP, RunnerClaudeCode, RunnerSerena and RunnerLocal are the values
// orchestrator.runners accepts.
const (
	RunnerOMP        = "omp"
	RunnerClaudeCode = "claudecode"
	RunnerSerena     = "serena"
	RunnerLocal      = "local"
)

// DefaultPath returns where Atenea looks for its settings when nothing else
// says otherwise.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "atenea", "atenea.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "atenea", "atenea.toml")
	}
	return filepath.Join(home, ".config", "atenea", "atenea.toml")
}

// ResolvePath picks the settings file: an explicit path wins, then
// ATENEA_CONFIG, then the default location.
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if fromEnv := os.Getenv("ATENEA_CONFIG"); fromEnv != "" {
		return fromEnv
	}
	return DefaultPath()
}

// Load reads the settings file at path. A missing file at the default location
// is not an error: Atenea falls back to the built-in defaults so a fresh
// install boots without ceremony. A missing file that was asked for explicitly
// is an error, because staying quiet there would hide a typo.
func Load(explicit string) (Config, error) {
	path := ResolvePath(explicit)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parse(raw, path)
	case errors.Is(err, fs.ErrNotExist) && explicit == "":
		return Defaults()
	default:
		return Config{}, contract.Fail(contract.FailureNotFound,
			"settings file %s: %v", path, err)
	}
}

// Defaults returns the embedded settings.
func Defaults() (Config, error) {
	return parse(defaultSettings, BuiltIn)
}

// WriteDefault copies the built-in settings to path.
func WriteDefault(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return contract.Fail(contract.FailureInvalidInput,
			"settings file %s already exists; pass --force to overwrite", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, defaultSettings, 0o644); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"writing %s: %v", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// On-disk shape
// ---------------------------------------------------------------------------

type file struct {
	Contract        string           `toml:"contract"`
	Core            fileCore         `toml:"core"`
	Orchestrator    fileOrchestrator `toml:"orchestrator"`
	Metrics         fileMetrics      `toml:"metrics"`
	Security        fileSecurity     `toml:"security"`
	Selector        fileSelector     `toml:"selector"`
	Capabilities    []fileCapability `toml:"capability"`
	Implementations []fileImpl       `toml:"implementation"`
	Repositories    []fileRepository `toml:"repository"`
}

type fileCore struct {
	ShutdownGrace string `toml:"shutdown_grace"`
}

type fileMetrics struct {
	Path        string `toml:"path"`
	Enabled     *bool  `toml:"enabled"`
	Flush       string `toml:"flush"`
	Compact     string `toml:"compact"`
	BufferLimit *int   `toml:"buffer_limit"`
}

type fileOrchestrator struct {
	MaxParallel   *int   `toml:"max_parallel"`
	Checkpoints   *bool  `toml:"checkpoints"`
	CheckpointDir string `toml:"checkpoint_dir"`
	// Runners uses a pointer so an omitted list and an explicitly empty one
	// are different things: leaving the block out keeps the shipped adapter,
	// while writing an empty list is how a user says "dispatch nowhere".
	Runners    *[]string             `toml:"runners"`
	Local      fileLocalRunner       `toml:"local"`
	OMP        fileOMPAdapter        `toml:"omp"`
	ClaudeCode fileClaudeCodeAdapter `toml:"claudecode"`
	Serena     fileSerenaAdapter     `toml:"serena"`
}

type fileLocalRunner struct {
	Implementations *[]string `toml:"implementations"`
	SkipDirs        *[]string `toml:"skip_dirs"`
}

type fileOMPAdapter struct {
	Binary          string    `toml:"binary"`
	Implementations *[]string `toml:"implementations"`
	MatchLimit      *int      `toml:"match_limit"`
	Timeout         string    `toml:"timeout"`
}

type fileClaudeCodeAdapter struct {
	Binary          string    `toml:"binary"`
	Implementations *[]string `toml:"implementations"`
	BudgetUSD       *float64  `toml:"budget_usd"`
	Timeout         string    `toml:"timeout"`
}

type fileSerenaAdapter struct {
	Endpoint        string    `toml:"endpoint"`
	Implementations *[]string `toml:"implementations"`
	Timeout         string    `toml:"timeout"`
}

// fileSecurity uses a pointer so an omitted list and an explicitly empty one
// are different things. Leaving the block out must not quietly disarm the
// guard; emptying it on purpose is the user's call to make.
type fileSecurity struct {
	Sensitive *[]string `toml:"sensitive"`
}

type fileSelector struct {
	Rules []fileRule `toml:"rule"`
}

type fileRule struct {
	Capability string `toml:"capability"`
	Repository string `toml:"repository"`
	Prefer     string `toml:"prefer"`
}

type fileCapability struct {
	ID        string      `toml:"id"`
	Version   string      `toml:"version"`
	Summary   string      `toml:"summary"`
	Semantics string      `toml:"semantics"`
	Effects   []string    `toml:"effects"`
	Inputs    []fileField `toml:"input"`
	Outputs   []fileField `toml:"output"`
}

type fileField struct {
	Name     string      `toml:"name"`
	Type     string      `toml:"type"`
	Required bool        `toml:"required"`
	Summary  string      `toml:"summary"`
	Fields   []fileField `toml:"field"`
}

type fileImpl struct {
	ID          string          `toml:"id"`
	Provider    string          `toml:"provider"`
	Capability  string          `toml:"capability"`
	Constraints fileConstraints `toml:"constraints"`
	Cost        fileCost        `toml:"cost"`
	Health      fileHealth      `toml:"health"`
}

type fileConstraints struct {
	Languages     []string `toml:"languages"`
	RequiresIndex bool     `toml:"requires_index"`
	MinScale      string   `toml:"min_scale"`
	MaxScale      string   `toml:"max_scale"`
}

// fileCost only carries the estimate. Measurements are never declared by hand:
// they are earned by running, and a hand-written measurement would poison the
// baseline the selector is meant to learn from.
type fileCost struct {
	EstimatedDuration string `toml:"estimated_duration"`
	EstimatedTokens   int    `toml:"estimated_tokens"`
	ToolVersion       string `toml:"tool_version"`
}

type fileHealth struct {
	State  string  `toml:"state"`
	Score  float64 `toml:"score"`
	Reason string  `toml:"reason"`
}

type fileRepository struct {
	ID        string   `toml:"id"`
	Path      string   `toml:"path"`
	Languages []string `toml:"languages"`
	Scale     string   `toml:"scale"`
	IndexedBy []string `toml:"indexed_by"`
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

func parse(raw []byte, source string) (Config, error) {
	var decoded file
	meta, err := toml.Decode(string(raw), &decoded)
	if err != nil {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	// An unknown key is almost always a typo, and a typo that is silently
	// ignored is a setting that never takes effect.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: unknown key(s): %s", source, strings.Join(keys, ", "))
	}

	cfg := Config{Source: source}

	if decoded.Contract == "" {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: contract version is required", source)
	}
	cfg.Contract, err = contract.ParseVersion(decoded.Contract)
	if err != nil {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	if !contract.Current.Supports(cfg.Contract) {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: contract %s is not supported by this core (%s)",
			source, cfg.Contract, contract.Current)
	}

	if cfg.Core, err = decoded.Core.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Orchestrator, err = decoded.Orchestrator.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Metrics, err = decoded.Metrics.build(source); err != nil {
		return Config{}, err
	}
	cfg.Security = decoded.Security.build()
	for _, rule := range decoded.Selector.Rules {
		cfg.Selector.Rules = append(cfg.Selector.Rules, selector.Rule{
			Capability: rule.Capability,
			Repository: rule.Repository,
			Prefer:     rule.Prefer,
		})
	}
	for _, raw := range decoded.Capabilities {
		capability, err := raw.build(source)
		if err != nil {
			return Config{}, err
		}
		cfg.Capabilities = append(cfg.Capabilities, capability)
	}
	for _, raw := range decoded.Implementations {
		impl, err := raw.build(source)
		if err != nil {
			return Config{}, err
		}
		cfg.Implementations = append(cfg.Implementations, impl)
	}
	for _, raw := range decoded.Repositories {
		repo, err := raw.build(source)
		if err != nil {
			return Config{}, err
		}
		cfg.Repositories = append(cfg.Repositories, repo)
	}
	return cfg, nil
}

const defaultShutdownGrace = 10 * time.Second

// The orchestrator defaults. Four steps at a time is a ceiling that keeps a
// laptop responsive; conflict resolution put the real brake on total memory.
// The measurement base now records what each step's far side weighed, so the
// figures exist; deciding the ceiling from them is still a fixed number here.
const (
	defaultMaxParallel = 4
	maxMaxParallel     = 100
)

// defaultServedImplementations is what a runner answers for unless the
// settings say otherwise: the one provider in the shipped catalog that needs
// no index and no language support. Both the omp adapter and the stand-in
// start there, because it is the same shape of search either way.
var defaultServedImplementations = []string{"ripgrep"}

// defaultSkipDirs keeps the stand-in out of the places a real search tool
// skips by reading .gitignore, which this one does not do.
var defaultSkipDirs = []string{".git", "node_modules", "vendor", "dist", "build", ".venv", "target"}

// defaultSensitive is the list from the security design: the files that carry
// secrets inside them. Reading is otherwise free.
var defaultSensitive = []string{
	".env",
	".env.*",
	".npmrc",
	".netrc",
	"*.pem",
	"*.key",
	"*.p12",
	"id_rsa",
	"id_ed25519",
	"*credentials*.json",
	"*service-account*.json",
}

// defaultClaudeImplementations is what the Claude Code adapter answers for
// once it is switched on. It is a separate id from ripgrep's on purpose: the
// two are not the same answer at the same price, and the selector has to be
// able to tell them apart.
var defaultClaudeImplementations = []string{"claude.search"}

func (o fileOrchestrator) build(source string) (Orchestrator, error) {
	out := Orchestrator{
		MaxParallel:   defaultMaxParallel,
		Runners:       []string{RunnerOMP},
		CheckpointDir: checkpoint.DefaultDir(),
		Local:         LocalRunner{Implementations: defaultServedImplementations, SkipDirs: defaultSkipDirs},
		OMP: OMPAdapter{
			Binary:          omp.DefaultBinary,
			Implementations: defaultServedImplementations,
			MatchLimit:      omp.DefaultMatchLimit,
			Timeout:         omp.DefaultTimeout,
		},
		ClaudeCode: ClaudeCodeAdapter{
			Binary:          claudecode.DefaultBinary,
			Implementations: defaultClaudeImplementations,
			BudgetUSD:       claudecode.DefaultBudgetUSD,
			Timeout:         claudecode.DefaultTimeout,
		},
		Serena: SerenaAdapter{
			Endpoint:        serena.DefaultEndpoint,
			Implementations: serena.DefaultImplementations(),
			Timeout:         serena.DefaultTimeout,
		},
	}
	if o.MaxParallel != nil {
		if *o.MaxParallel < 0 || *o.MaxParallel > maxMaxParallel {
			return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.max_parallel must be between 0 and %d, got %d",
				source, maxMaxParallel, *o.MaxParallel)
		}
		out.MaxParallel = *o.MaxParallel
	}
	if o.Runners != nil {
		seen := make(map[string]struct{}, len(*o.Runners))
		list := make([]string, 0, len(*o.Runners))
		for _, name := range *o.Runners {
			switch name {
			case RunnerOMP, RunnerClaudeCode, RunnerSerena, RunnerLocal:
			default:
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.runners has %q, which is not one of %s, %s, %s, %s",
					source, name, RunnerOMP, RunnerClaudeCode, RunnerSerena, RunnerLocal)
			}
			// A name written twice is a mistake, not an instruction: it would
			// build the same adapter again and then collide with itself over
			// every implementation it serves.
			if _, dup := seen[name]; dup {
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.runners lists %q twice", source, name)
			}
			seen[name] = struct{}{}
			list = append(list, name)
		}
		out.Runners = list
	}
	if o.CheckpointDir != "" {
		out.CheckpointDir = o.CheckpointDir
	}
	if o.Checkpoints != nil && !*o.Checkpoints {
		out.CheckpointDir = ""
	}
	if o.Local.Implementations != nil {
		out.Local.Implementations = *o.Local.Implementations
	}
	if o.Local.SkipDirs != nil {
		out.Local.SkipDirs = *o.Local.SkipDirs
	}
	adapter, err := o.OMP.build(source, out.OMP)
	if err != nil {
		return Orchestrator{}, err
	}
	out.OMP = adapter
	claude, err := o.ClaudeCode.build(source, out.ClaudeCode)
	if err != nil {
		return Orchestrator{}, err
	}
	out.ClaudeCode = claude
	symbols, err := o.Serena.build(source, out.Serena)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Serena = symbols
	return out, nil
}

func (o fileOMPAdapter) build(source string, out OMPAdapter) (OMPAdapter, error) {
	if strings.TrimSpace(o.Binary) != "" {
		out.Binary = strings.TrimSpace(o.Binary)
	}
	if o.Implementations != nil {
		out.Implementations = *o.Implementations
	}
	if o.MatchLimit != nil {
		if *o.MatchLimit <= 0 {
			// Zero is the tempting way to write "no limit", and it is exactly
			// the value omp reads as "use a small default and call the answer
			// complete". Refusing it here is cheaper than explaining a
			// silently short search later.
			return OMPAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.omp.match_limit must be above 0, got %d",
				source, *o.MatchLimit)
		}
		out.MatchLimit = *o.MatchLimit
	}
	if o.Timeout != "" {
		timeout, err := time.ParseDuration(o.Timeout)
		if err != nil {
			return OMPAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.omp.timeout %q: %v", source, o.Timeout, err)
		}
		if timeout <= 0 {
			return OMPAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.omp.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	return out, nil
}

// The metric rhythms. Flushing every half minute keeps the window of work at
// risk from a hard kill small without turning the batch back into a write per
// call; compacting hourly matches the finest tier the ladder keeps, so a pass
// never has more than one closed hour to fold.
const (
	defaultMetricsFlush   = 30 * time.Second
	defaultMetricsCompact = time.Hour
)

func (m fileMetrics) build(source string) (Metrics, error) {
	out := Metrics{
		Path:    metrics.DefaultPath(),
		Enabled: true,
		Flush:   defaultMetricsFlush,
		Compact: defaultMetricsCompact,
		// Taken from the store rather than restated here: a settings file that
		// says nothing and a store opened with nothing have to agree, and two
		// numbers that must match are one number.
		BufferLimit: metrics.DefaultBufferLimit,
	}
	if strings.TrimSpace(m.Path) != "" {
		out.Path = strings.TrimSpace(m.Path)
	}
	if m.Enabled != nil {
		out.Enabled = *m.Enabled
	}
	for _, beat := range []struct {
		key   string
		raw   string
		field *time.Duration
	}{
		{"flush", m.Flush, &out.Flush},
		{"compact", m.Compact, &out.Compact},
	} {
		if beat.raw == "" {
			continue
		}
		every, err := time.ParseDuration(beat.raw)
		if err != nil {
			return Metrics{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: metrics.%s %q: %v", source, beat.key, beat.raw, err)
		}
		if every <= 0 {
			// Zero reads like "never", and a rhythm that never fires is a
			// maintenance task that silently stops happening. Turning the
			// store off is what `enabled = false` is for, and it says so.
			return Metrics{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: metrics.%s must be above 0, got %s; use enabled = false to stop measuring",
				source, beat.key, every)
		}
		*beat.field = every
	}
	if m.BufferLimit != nil {
		if *m.BufferLimit <= 0 {
			return Metrics{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: metrics.buffer_limit must be above 0, got %d",
				source, *m.BufferLimit)
		}
		out.BufferLimit = *m.BufferLimit
	}
	return out, nil
}

func (c fileClaudeCodeAdapter) build(source string, out ClaudeCodeAdapter) (ClaudeCodeAdapter, error) {
	if strings.TrimSpace(c.Binary) != "" {
		out.Binary = strings.TrimSpace(c.Binary)
	}
	if c.Implementations != nil {
		out.Implementations = *c.Implementations
	}
	if c.BudgetUSD != nil {
		if *c.BudgetUSD <= 0 {
			// Same trap as omp's match_limit, with money instead of matches:
			// zero reads as "no ceiling" and is the one value that must not
			// mean that. A model turn with nothing stopping it is a runaway.
			return ClaudeCodeAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.claudecode.budget_usd must be above 0, got %v",
				source, *c.BudgetUSD)
		}
		out.BudgetUSD = *c.BudgetUSD
	}
	if c.Timeout != "" {
		timeout, err := time.ParseDuration(c.Timeout)
		if err != nil {
			return ClaudeCodeAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.claudecode.timeout %q: %v", source, c.Timeout, err)
		}
		if timeout <= 0 {
			return ClaudeCodeAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.claudecode.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	return out, nil
}

func (s fileSerenaAdapter) build(source string, out SerenaAdapter) (SerenaAdapter, error) {
	if strings.TrimSpace(s.Endpoint) != "" {
		out.Endpoint = strings.TrimSpace(s.Endpoint)
	}
	if s.Implementations != nil {
		out.Implementations = *s.Implementations
	}
	if s.Timeout != "" {
		timeout, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return SerenaAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.serena.timeout %q: %v", source, s.Timeout, err)
		}
		if timeout <= 0 {
			return SerenaAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.serena.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	return out, nil
}

func (s fileSecurity) build() Security {
	if s.Sensitive == nil {
		return Security{Sensitive: defaultSensitive}
	}
	return Security{Sensitive: *s.Sensitive}
}

func (c fileCore) build(source string) (Core, error) {
	out := Core{ShutdownGrace: defaultShutdownGrace}
	if c.ShutdownGrace == "" {
		return out, nil
	}
	grace, err := time.ParseDuration(c.ShutdownGrace)
	if err != nil {
		return Core{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: core.shutdown_grace %q: %v", source, c.ShutdownGrace, err)
	}
	if grace <= 0 {
		return Core{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: core.shutdown_grace must be positive", source)
	}
	out.ShutdownGrace = grace
	return out, nil
}

func (c fileCapability) build(source string) (contract.Capability, error) {
	fail := func(format string, args ...any) (contract.Capability, error) {
		return contract.Capability{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: capability %s: %s", source, c.ID, fmt.Sprintf(format, args...))
	}
	version, err := contract.ParseVersion(c.Version)
	if err != nil {
		return fail("%v", err)
	}
	out := contract.Capability{
		ID:        c.ID,
		Version:   version,
		Summary:   c.Summary,
		Semantics: strings.TrimSpace(c.Semantics),
	}
	for _, name := range c.Effects {
		effect, err := contract.ParseEffect(name)
		if err != nil {
			return fail("%v", err)
		}
		out.Effects = append(out.Effects, effect)
	}
	if out.Inputs, err = buildFields(c.Inputs); err != nil {
		return fail("%v", err)
	}
	if out.Outputs, err = buildFields(c.Outputs); err != nil {
		return fail("%v", err)
	}
	if err := out.Validate(); err != nil {
		return contract.Capability{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	return out, nil
}

func buildFields(raw []fileField) ([]contract.Field, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]contract.Field, 0, len(raw))
	for _, f := range raw {
		kind, err := contract.ParseFieldType(f.Type)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		nested, err := buildFields(f.Fields)
		if err != nil {
			return nil, err
		}
		out = append(out, contract.Field{
			Name:     f.Name,
			Type:     kind,
			Required: f.Required,
			Summary:  f.Summary,
			Fields:   nested,
		})
	}
	return out, nil
}

func (i fileImpl) build(source string) (contract.Implementation, error) {
	fail := func(format string, args ...any) (contract.Implementation, error) {
		return contract.Implementation{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: implementation %s: %s", source, i.ID, fmt.Sprintf(format, args...))
	}
	minScale, err := contract.ParseScale(i.Constraints.MinScale)
	if err != nil {
		return fail("min_scale: %v", err)
	}
	maxScale, err := contract.ParseScale(i.Constraints.MaxScale)
	if err != nil {
		return fail("max_scale: %v", err)
	}
	var estimated contract.Sample
	if i.Cost.EstimatedDuration != "" {
		duration, err := time.ParseDuration(i.Cost.EstimatedDuration)
		if err != nil {
			return fail("cost.estimated_duration %q: %v", i.Cost.EstimatedDuration, err)
		}
		if duration < 0 {
			return fail("cost.estimated_duration must not be negative")
		}
		estimated.Duration = duration
	}
	if i.Cost.EstimatedTokens < 0 {
		return fail("cost.estimated_tokens must not be negative")
	}
	estimated.Tokens = i.Cost.EstimatedTokens
	state, err := contract.ParseHealthState(i.Health.State)
	if err != nil {
		return fail("health.state: %v", err)
	}

	languages := make([]string, 0, len(i.Constraints.Languages))
	for _, lang := range i.Constraints.Languages {
		languages = append(languages, strings.ToLower(strings.TrimSpace(lang)))
	}

	out := contract.Implementation{
		ID:         i.ID,
		Provider:   i.Provider,
		Capability: i.Capability,
		Constraints: contract.Constraints{
			Languages:     languages,
			RequiresIndex: i.Constraints.RequiresIndex,
			MinScale:      minScale,
			MaxScale:      maxScale,
		},
		Cost: contract.Cost{
			Estimated:   estimated,
			ToolVersion: i.Cost.ToolVersion,
		},
		Health: contract.Health{
			State:  state,
			Score:  i.Health.Score,
			Reason: i.Health.Reason,
		},
	}
	if err := out.Validate(); err != nil {
		return contract.Implementation{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	return out, nil
}

func (r fileRepository) build(source string) (contract.Repository, error) {
	scale, err := contract.ParseScale(r.Scale)
	if err != nil {
		return contract.Repository{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: repository %s: scale: %v", source, r.ID, err)
	}
	out := contract.NewRepository(r.ID, r.Path, r.Languages, scale, r.IndexedBy)
	if err := out.Validate(); err != nil {
		return contract.Repository{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	return out, nil
}
