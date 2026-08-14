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
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
	"github.com/Tutitoos/atenea/internal/adapter/codebasememory"
	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/adapter/serena"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/mcpprobe"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

//go:embed default.toml
var defaultSettings []byte

// BuiltIn is the Source of a config that came from the embedded defaults.
const BuiltIn = "built-in defaults"

// Config is the decoded, validated settings file.
type Config struct {
	// Source is the file it came from, or BuiltIn. When a repository's own
	// overlay applied, it names both, joined by " + ".
	Source string
	// Local records the repository overlay that applied, or nil when none
	// did. Nothing reads it to decide behavior -- the overlay is already
	// merged into the fields above by the time it is set -- it is here so a
	// reader can be told which half of the effective settings is whose.
	Local *Local
	// Contract is the contract version the file targets.
	Contract     contract.Version
	Core         Core
	Orchestrator Orchestrator
	// Model fixes which model backs each of the two model-backed built-in
	// agents, explore and plan, by role.
	Model Model
	// Workflow is how a graph of agent steps is scheduled.
	Workflow        Workflow
	Metrics         Metrics
	Backup          Backup
	Security        Security
	Selector        selector.Config
	Capabilities    []contract.Capability
	Implementations []contract.Implementation
	Repositories    []contract.Repository
	// Agents are the declared agent types, resolved by name at dispatch.
	Agents []AgentType
	// Missing names implementations the shipped catalog declares and this
	// settings file does not.
	//
	// Settings replace the catalog wholesale rather than patching it, which
	// is documented and deliberate -- but it means a file written before a
	// release never gains what that release shipped, and until now nothing
	// said so. Measured on a real machine: a settings file predating v0.6.0
	// still registered one implementation of symbol.overview after the binary
	// began shipping two, so the funnel ran with a single candidate where it
	// should have had a fallback, and a dead provider had nothing to fall back
	// to.
	//
	// It is advisory, never an error: the file is the user's, and a catalog
	// trimmed on purpose is a legitimate thing to have.
	Missing []string
	// MCPServers are the MCP endpoints Atenea will vouch for on a client's
	// behalf, used by `atenea wrap`. They are not part of the catalog and
	// nothing dispatches against them: Atenea's own providers are reached
	// through adapters, not through this list.
	MCPServers []MCPServer
}

// MCPServer is one MCP endpoint a client should be pointed at instead of
// spawning its own copy.
//
// The point of declaring it here rather than in each client's own config is
// that there is then one declaration to keep true instead of one per client,
// and something that can check it before a client is told it exists.
type MCPServer struct {
	// ID is the name the client will see. It replaces any server the client
	// already declares under the same name, which is how a per-session
	// spawn becomes a pointer at a shared one.
	ID string
	// URL is set for an HTTP endpoint, Command for a stdio one. Exactly one
	// of the two, which is what picks the transport.
	URL     string
	Command []string
	Env     map[string]string
	// Timeout bounds the readiness check. Zero takes the probe's default.
	Timeout time.Duration
	// Expose says whether the backend's own tools may be reached through
	// Atenea, and is the field that separates a pointer from a passthrough.
	//
	// ExposeOff, the default and today's whole behavior, means the entry is
	// a pointer: `atenea wrap` tells a client where the shared server lives
	// and then steps out of the path. ExposeRaw means Atenea holds the
	// connection and re-offers the tools verbatim under `raw.<id>.<tool>`.
	//
	// A raw backend must also say what it may offer and what that costs:
	// Tools is an allow list with no default, and Effects is what every one
	// of them is authorized to cause. Both are required of a raw block and
	// neither is read on an off one -- see the refusals in build.
	Expose Expose
	// Tools are the backend's own tool names this machine allows through,
	// already trimmed and deduplicated. Empty on an off block; never empty
	// on a raw one.
	Tools []string
	// Effects is the whole backend's authorization, and ToolEffects narrows
	// it for the tools that differ. A tool absent from ToolEffects causes
	// Effects; there is no third answer, because an undeclared effect is
	// the thing this pair exists to make impossible.
	Effects     []contract.Effect
	ToolEffects map[string][]contract.Effect
}

// EffectsOf reports what one of this backend's tools is authorized to cause.
//
// A tool with its own declaration uses it; every other tool causes what the
// server declared. There is deliberately no third answer: a tool that reached
// this call is already inside the allow list, and an allow list entry with no
// effects cannot exist, so "undeclared" is not a state this can be asked
// about.
func (m MCPServer) EffectsOf(tool string) []contract.Effect {
	if narrowed, ok := m.ToolEffects[tool]; ok {
		return narrowed
	}
	return m.Effects
}

// Probe describes this declaration the way internal/mcpprobe wants it, and is
// the one place that conversion lives.
//
// It sits here rather than in either caller because there are two of them now
// and they must not disagree. `atenea wrap` and `atenea detect` build one to
// actually probe; the status screen builds one only to answer "how would this
// be reached, and where" -- Server.Transport and Server.Where already own
// those two sentences, and a second copy of the URL-or-command rule is how a
// screen ends up naming a transport the prober would not have used.
func (m MCPServer) Probe() mcpprobe.Server {
	return mcpprobe.Server{
		ID:      m.ID,
		URL:     m.URL,
		Command: m.Command,
		Env:     m.Env,
		Timeout: m.Timeout,
	}
}

// Expose is how a declared backend may be reached.
//
// It is a closed set rather than a bool because the third state is already
// foreseeable -- a curated subset of a backend's tools -- and a bool would
// have to be replaced rather than extended when it arrives.
type Expose string

const (
	// ExposeOff is a pointer: the client is told where the server is and
	// talks to it directly. Atenea is not in the path.
	ExposeOff Expose = "off"
	// ExposeRaw is a passthrough: the tools are re-offered verbatim under
	// the reserved raw. namespace, with no funnel and no capability.
	ExposeRaw Expose = "raw"
)

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

// Backup holds what protects the history and how often.
//
// The rhythm lives beside the metrics rhythms on purpose: they all run in the
// one clock lane, and the design asks for retuning a beat to be a line in this
// file rather than a rebuild. Two beats set to collide are visible here and
// nowhere else.
type Backup struct {
	// Dir is where copies go. Empty means platform.BackupDir -- a folder of
	// its own beside the state root, never inside it.
	Dir string
	// Enabled is false when the user does not want copies kept. A machine
	// whose state root is already on replicated storage does not need them.
	Enabled bool
	// Every is how often a copy is taken.
	Every time.Duration
	// Keep is how many copies survive. The oldest is dropped when the next
	// one lands.
	Keep int
}

// Orchestrator holds the knobs of the agent that splits and hands out work.
type Orchestrator struct {
	// MaxParallel caps how many steps of one wave run at a time. Zero means no
	// ceiling. The real limit is the machine, and the machine is not the same
	// one everywhere, so it is declared rather than compiled in.
	MaxParallel int
	// BudgetUSD is what one commission may spend, across every step and every
	// paid provider it dispatches to.
	//
	// It lives here and not on an adapter because money is a permission, and
	// permissions are granted per commission. An adapter holding its own
	// ceiling could only ever cap one invocation, so a run of four steps spent
	// it four times and no adapter could see the others doing the same.
	BudgetUSD float64
	// StandingEffects are granted to every commission and question this core
	// dispatches, on top of the read that is always free. It exists beside
	// the per-commission Effects on Task and Question because some effects
	// -- process, today -- are not a choice made per request: every real
	// implementation of code.search spawns a binary to answer at all, so
	// requiring it to be asked for on every single call would just move the
	// same yes to every caller instead of saying it once, here.
	StandingEffects []contract.Effect
	// ClientEffects is what a chat opened by a connected client is granted
	// standing, and the most such a chat may ever be granted. It exists apart
	// from StandingEffects because the two answer different questions that
	// used to share one line: what the person at this terminal may do, and
	// what anything that connects to them may do. Widening the first for an
	// afternoon silently widened the second, and the file could not say
	// otherwise.
	//
	// It is resolved here rather than at dispatch, so nothing downstream has
	// to know whether the key was written: an absent key is copied from
	// StandingEffects once, and the two are separate lists from then on.
	ClientEffects []contract.Effect
	// ClientEffectsInherited records that the copy above is a copy. Nothing
	// behaves differently for it -- it is on the status screen, because
	// inheriting is the case that still carries the sharp edge and a screen
	// that showed two equal lists without saying why would hide it.
	ClientEffectsInherited bool
	// CheckpointDir is where the paper copy of a run in flight is written. It
	// is empty when checkpointing is off.
	CheckpointDir string
	// Runners names the client adapters that are live, in the order they are
	// declared. It is a list and not a choice because omp and Claude Code are
	// both first-class: with one slot, whichever client lost would have every
	// implementation it serves permanently unreachable. An empty list leaves
	// the core able to plan and choose, with nothing to dispatch to.
	Runners        []string
	Local          LocalRunner
	OMP            OMPAdapter
	ClaudeCode     ClaudeCodeAdapter
	Serena         SerenaAdapter
	CodebaseMemory CodebaseMemoryAdapter
}

// Model fixes which model backs each of the two model-backed built-in
// agents, explore and plan, by role. Its fields mirror
// internal/agent/model.Options -- same names, same types, same order --
// because internal/config cannot import that package without a cycle
// (internal/core, which the model client dials for Atenea's own tools,
// already imports internal/config); the caller that holds both packages
// converts with a plain model.Options(cfg.Model) instead.
//
// Model choice is fixed here, per role, and not chosen per task: there is
// no cost data yet a per-task choice could be justified against, and a knob
// nobody can tune correctly is worse than one fixed default that is visible
// in a file and changed by hand once it is wrong.
type Model struct {
	// Binary is the CLI executable that answers a model turn. A bare name
	// is looked up on PATH.
	Binary string
	// Timeout caps one turn. A model turn is slower than a tool call by
	// nature; see ClaudeCodeAdapter.Timeout for the same reasoning.
	Timeout time.Duration
	// Explore is the model name for the agent that explores a repository:
	// an alias the CLI resolves itself ("sonnet", "opus") or a full name.
	// Empty means the role has no model configured, so a dispatch to it is
	// refused rather than silently spending against a hardcoded default --
	// the same reason ClaudeCodeAdapter ships out of Runners.
	Explore string
	// Plan is the model name for the agent that turns a discovery graph
	// into a plan.
	Plan string
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
	// Timeout caps one invocation. A model turn is slower than a tool call by
	// nature, so this sits far above omp's ceiling.
	Timeout time.Duration
}

// Instance is how many copies of a managed server there should be, and what
// distinguishes one from another.
//
// It exists because of one measured thing. Serena's `activate_project` is
// process-wide: a second chat pointing the same process at a different
// repository takes the first one's language server down with it, so two
// projects cannot stay warm in one process. This machine already solved that
// by hand -- two systemd units, identical but for a port and a `--project`,
// one per repository -- which is a policy typed out per repository in a place
// Atenea could not see. Naming it here makes it a rule instead.
//
// The set is deliberately small and closed. `per_chat` is not here: it would
// be a process per conversation, saving nothing, and nothing on this machine
// has ever needed it. An unknown value is refused rather than read as the
// default, for the reason every other refusal in this file exists -- a policy
// an operator believes is in force and is not is worse than one they can see
// is missing.
type Instance string

const (
	// InstanceShared is one process for the whole machine, and the default:
	// it is what every managed server did before this existed.
	InstanceShared Instance = "shared"
	// InstancePerRepository is one process per declared repository, each
	// pinned to that repository and started only when something asks for it.
	InstancePerRepository Instance = "per_repository"
)

// ProjectPlaceholder is replaced with a repository's path when a
// per-repository server is built. It is the seam that makes one declaration
// mean N processes: without it every instance would launch the same command
// on a different port, all pointed at the same project, which is why a
// per-repository block that omits it is refused.
const ProjectPlaceholder = "{{project}}"

// ManagedProcess declares that Atenea should launch this server itself and
// keep it alive, rather than assuming something else already started it at a
// fixed Endpoint. Present only when the settings file opts in; nil leaves the
// previous behavior untouched -- an externally managed server, taken on faith
// at whatever Endpoint says.
//
// The shape mirrors supervisor.Spec closely on purpose: this is that Spec as
// far as the settings file is allowed to state it, before the core fills in
// the ID and the pieces only it knows.
type ManagedProcess struct {
	// Command and Args launch the server. A bare Command is looked up on
	// PATH. An element of Args equal to "{{port}}" is replaced with the
	// chosen port before every spawn.
	Command string
	Args    []string
	// Env adds "KEY=VALUE" entries to the inherited environment, rather than
	// replacing it.
	Env []string
	// Lifecycle decides who may stop the server once it is up: see
	// supervisor.Persistent and supervisor.OnDemand.
	Lifecycle supervisor.Lifecycle
	// Port fixes the port the server listens on. Zero asks the OS for a free
	// one, the safer default for anything that does not need a stable
	// address on this machine.
	Port int
	// RestartLimit is how many crashes are retried before the server is
	// marked down for good. Unlike the five below it is resolved here, not
	// in the supervisor: zero means "never retry" rather than "unset", so
	// the supervisor has no way to spot an omitted key and leaves the
	// choice to whoever builds the Spec.
	RestartLimit int
	// RestartDelay, StableAfter, ReadyTimeout, IdleTimeout and StopGrace
	// tune the rest of the crash-recovery behavior. Zero takes the
	// supervisor package's own default for each, so those numbers are
	// decided in one place rather than two that could drift apart.
	RestartDelay time.Duration
	StableAfter  time.Duration
	ReadyTimeout time.Duration
	IdleTimeout  time.Duration
	StopGrace    time.Duration
	// Instance says how many of this server there should be. It is the one
	// field here with no counterpart in supervisor.Spec, because the
	// supervisor does not know what a repository is: the core answers it by
	// building one Spec per instance, and each of those is an ordinary
	// server to everything downstream.
	Instance Instance
}

// SerenaAdapter configures the Serena adapter.
//
// Serena is the first far side that is not a CLI: it is an MCP server behind a
// local proxy. Nothing above this line changes because of that -- a capability
// does not care whether its provider is a command or a server, which is the
// point of having the seam at all.
type SerenaAdapter struct {
	// Endpoint is where the MCP server is listening. Ignored once Process is
	// set: a managed server's real endpoint is whatever port the supervisor
	// actually chose, never this one.
	Endpoint string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. A language server indexing a cold repository is
	// slow long before it is stuck.
	Timeout time.Duration
	// Process is set when Atenea should launch and supervise Serena itself.
	// nil means Serena is assumed already running at Endpoint, unchanged
	// from before this existed.
	Process *ManagedProcess
}

// CodebaseMemoryAdapter configures the codebase-memory-mcp adapter.
//
// Unlike Serena, this far side needs no Process block: codebase-memory-mcp
// is a one-shot CLI, not a server, so there is no endpoint to reach and
// nothing for Atenea's supervisor to keep alive.
type CodebaseMemoryAdapter struct {
	// Binary is the codebase-memory-mcp executable. A bare name is looked up
	// on PATH.
	Binary string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. It sits at Serena's own ceiling: both open an
	// index before they can answer, and a cold one is slow long before it is
	// stuck.
	Timeout time.Duration
}

// Security is the one place delicate files are declared.
type Security struct {
	// Sensitive holds the path patterns that carry secrets. Reading is free by
	// default; these are the exception, and while exploring they are skipped in
	// silence rather than turned into a question.
	Sensitive []string
}

// RunnerOMP, RunnerClaudeCode, RunnerSerena, RunnerCodebaseMemory and
// RunnerLocal are the values orchestrator.runners accepts.
const (
	RunnerOMP            = "omp"
	RunnerClaudeCode     = "claudecode"
	RunnerSerena         = "serena"
	RunnerCodebaseMemory = "codebasememory"
	RunnerLocal          = "local"
)

// DefaultPath returns where Atenea looks for its settings when nothing else
// says otherwise.
func DefaultPath() string { return filepath.Join(platform.ConfigDir(), "atenea.toml") }

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
		cfg, parseErr := parse(raw, path)
		if parseErr != nil {
			return Config{}, parseErr
		}
		cfg.Missing = missingImplementations(cfg)
		return cfg, nil
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

// missingImplementations lists what the shipped catalog declares for the
// capabilities this file kept, and this file does not.
//
// Only capabilities the file still declares are considered: dropping a whole
// capability is a deliberate act, and naming the providers it no longer needs
// would be noise rather than a warning.
func missingImplementations(cfg Config) []string {
	shipped, err := Defaults()
	if err != nil {
		// Embedded defaults that do not parse are a build fault, and this
		// comparison is advisory: it must never be the reason a working
		// settings file fails to load.
		return nil
	}
	have := make(map[string]struct{}, len(cfg.Implementations))
	for _, impl := range cfg.Implementations {
		have[impl.ID] = struct{}{}
	}
	declared := make(map[string]struct{}, len(cfg.Capabilities))
	for _, capability := range cfg.Capabilities {
		declared[capability.ID] = struct{}{}
	}
	var out []string
	for _, impl := range shipped.Implementations {
		if _, ok := have[impl.ID]; ok {
			continue
		}
		if _, ok := declared[impl.Capability]; !ok {
			continue
		}
		out = append(out, impl.ID)
	}
	sort.Strings(out)
	return out
}

// shippedAgents returns the agent types the built-in settings declare and
// this file does not name, in the order they ship.
//
// A file that names an agent keeps its own: the shipped declaration is a
// default, and a person who edited `filereader` to spawn something else
// meant it. Nothing is ever removed here.
//
// The BuiltIn guard is what stops the recursion -- Defaults() is parse() of
// the shipped file -- and a defaults file that will not parse is a build
// fault this must not turn into a failure to load a working user file, the
// same reasoning missingImplementations spells out above.
func shippedAgents(source string, named map[string]bool) []AgentType {
	if source == BuiltIn {
		return nil
	}
	shipped, err := Defaults()
	if err != nil {
		return nil
	}
	var out []AgentType
	for _, agent := range shipped.Agents {
		if named[agent.Spec.Name] {
			continue
		}
		out = append(out, agent)
	}
	return out
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
	Model           fileModel        `toml:"model"`
	Workflow        fileWorkflow     `toml:"workflow"`
	Metrics         fileMetrics      `toml:"metrics"`
	Backup          fileBackup       `toml:"backup"`
	Security        fileSecurity     `toml:"security"`
	Selector        fileSelector     `toml:"selector"`
	Capabilities    []fileCapability `toml:"capability"`
	Implementations []fileImpl       `toml:"implementation"`
	Repositories    []fileRepository `toml:"repository"`
	Agents          []fileAgent      `toml:"agent"`
	MCPServers      []fileMCPServer  `toml:"mcp_server"`
}

// fileModel is [model] as written.
type fileModel struct {
	Binary  string `toml:"binary"`
	Timeout string `toml:"timeout"`
	Explore string `toml:"explore"`
	Plan    string `toml:"plan"`
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

type fileBackup struct {
	Dir     string `toml:"dir"`
	Enabled *bool  `toml:"enabled"`
	Every   string `toml:"every"`
	Keep    *int   `toml:"keep"`
}

type fileOrchestrator struct {
	MaxParallel *int     `toml:"max_parallel"`
	BudgetUSD   *float64 `toml:"budget_usd"`
	// Effects are granted standing, to every commission and question, on
	// top of the read that is always free. Nil means none, which is the
	// correct zero: no effect has ever needed one to be free by default.
	Effects []string `toml:"effects"`
	// ClientEffects is the same list for a chat opened by a connected client,
	// and it is a pointer for the reason Runners is: an omitted key inherits
	// Effects, while an explicitly empty list is how the operator says a
	// client may do nothing but read. Those two cannot be the same value.
	ClientEffects *[]string `toml:"client_effects"`
	Checkpoints   *bool     `toml:"checkpoints"`
	CheckpointDir string    `toml:"checkpoint_dir"`
	// Runners uses a pointer so an omitted list and an explicitly empty one
	// are different things: leaving the block out keeps the shipped adapter,
	// while writing an empty list is how a user says "dispatch nowhere".
	Runners        *[]string                 `toml:"runners"`
	Local          fileLocalRunner           `toml:"local"`
	OMP            fileOMPAdapter            `toml:"omp"`
	ClaudeCode     fileClaudeCodeAdapter     `toml:"claudecode"`
	Serena         fileSerenaAdapter         `toml:"serena"`
	CodebaseMemory fileCodebaseMemoryAdapter `toml:"codebasememory"`
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
	Timeout         string    `toml:"timeout"`
}

type fileSerenaAdapter struct {
	Endpoint        string              `toml:"endpoint"`
	Implementations *[]string           `toml:"implementations"`
	Timeout         string              `toml:"timeout"`
	Process         *fileManagedProcess `toml:"process"`
}

// fileManagedProcess uses a pointer on the field above so an omitted
// [orchestrator.serena.process] table and a present-but-empty one are
// different: the first leaves Process nil (unmanaged, unchanged behavior),
// the second is a user who opted in without a command, which is an error
// worth naming rather than silently doing nothing.
type fileManagedProcess struct {
	Command      string   `toml:"command"`
	Args         []string `toml:"args"`
	Env          []string `toml:"env"`
	Lifecycle    string   `toml:"lifecycle"`
	Port         *int     `toml:"port"`
	RestartLimit *int     `toml:"restart_limit"`
	RestartDelay string   `toml:"restart_delay"`
	StableAfter  string   `toml:"stable_after"`
	ReadyTimeout string   `toml:"ready_timeout"`
	IdleTimeout  string   `toml:"idle_timeout"`
	StopGrace    string   `toml:"stop_grace"`
	Instance     string   `toml:"instance"`
}

// fileCodebaseMemoryAdapter is the TOML shape of CodebaseMemoryAdapter.
type fileCodebaseMemoryAdapter struct {
	Binary          string    `toml:"binary"`
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
	Enum     []string    `toml:"enum"`
	Fields   []fileField `toml:"field"`
}

type fileImpl struct {
	ID             string          `toml:"id"`
	Provider       string          `toml:"provider"`
	Capability     string          `toml:"capability"`
	ScopeGuarantee string          `toml:"scope_guarantee"`
	Constraints    fileConstraints `toml:"constraints"`
	Cost           fileCost        `toml:"cost"`
	Health         fileHealth      `toml:"health"`
}

type fileConstraints struct {
	Languages     []string `toml:"languages"`
	RequiresIndex bool     `toml:"requires_index"`
	RequiresVCS   bool     `toml:"requires_vcs"`
	MinScale      string   `toml:"min_scale"`
	MaxScale      string   `toml:"max_scale"`
	// MaxInput bounds request inputs by name, inclusive. It is the one
	// constraint here that reads what was asked for rather than where.
	MaxInput map[string]int `toml:"max_input"`
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
	VCS       string   `toml:"vcs"`
	IndexedBy []string `toml:"indexed_by"`
}

type fileMCPServer struct {
	ID      string            `toml:"id"`
	URL     string            `toml:"url"`
	Command []string          `toml:"command"`
	Env     map[string]string `toml:"env"`
	Timeout string            `toml:"timeout"`
	Expose  string            `toml:"expose"`
	Tools   []string          `toml:"tools"`
	Effects []string          `toml:"effects"`
	Tool    []fileMCPTool     `toml:"tool"`
}

type fileMCPTool struct {
	Name    string   `toml:"name"`
	Effects []string `toml:"effects"`
}

// build validates one declared endpoint. The rules are all one rule: a
// declaration that cannot be checked is worse than no declaration, because
// it reads as a promise. So an entry must name something reachable, and it
// must name exactly one thing -- a block carrying both a url and a command
// has two answers to "where is this" and no way to choose between them.
func (m fileMCPServer) build(source string) (MCPServer, error) {
	fail := func(format string, args ...any) (MCPServer, error) {
		return MCPServer{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s", source, fmt.Sprintf(format, args...))
	}
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return fail("mcp_server: id is required")
	}
	// A dot would make the passthrough name ambiguous: a tool re-offered as
	// raw.<id>.<tool> can only be split back into a server and a tool while
	// the server is one segment. The refusal is here rather than at the
	// dispatch site because a name that cannot be parsed later is already
	// wrong when it is written, whether or not anything reads it yet.
	if strings.Contains(id, ".") {
		return fail("mcp_server %s: id must not contain a dot; it is one segment of raw.<id>.<tool>", id)
	}
	hasURL, hasCommand := strings.TrimSpace(m.URL) != "", len(m.Command) > 0
	switch {
	case hasURL && hasCommand:
		return fail("mcp_server %s: has both url and command; a server is reached one way", id)
	case !hasURL && !hasCommand:
		return fail("mcp_server %s: needs a url or a command", id)
	}
	out := MCPServer{ID: id, Command: m.Command, Env: m.Env}
	if hasURL {
		parsed, err := url.Parse(strings.TrimSpace(m.URL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fail("mcp_server %s: url %q is not an absolute http(s) url", id, m.URL)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fail("mcp_server %s: url scheme %q is not http or https", id, parsed.Scheme)
		}
		out.URL = parsed.String()
	}
	for i, part := range out.Command {
		if strings.TrimSpace(part) == "" {
			return fail("mcp_server %s: command has an empty argument at position %d", id, i)
		}
	}
	if m.Timeout != "" {
		d, err := time.ParseDuration(m.Timeout)
		if err != nil {
			return fail("mcp_server %s: timeout %q: %v", id, m.Timeout, err)
		}
		if d <= 0 {
			return fail("mcp_server %s: timeout %s is not positive", id, d)
		}
		out.Timeout = d
	}
	// An absent key inherits the pointer behavior that shipped before this
	// field existed, so a settings file written earlier keeps meaning what it
	// meant. An unknown value is refused rather than defaulted: silently
	// reading `expose = "true"` as off would leave the operator believing a
	// backend is reachable when nothing offers it.
	switch expose := strings.TrimSpace(m.Expose); expose {
	case "":
		out.Expose = ExposeOff
	case string(ExposeOff), string(ExposeRaw):
		out.Expose = Expose(expose)
	default:
		return fail("mcp_server %s: expose %q is not %s or %s", id, expose, ExposeOff, ExposeRaw)
	}
	// Both transports reach a raw backend now. Which one a block means was
	// already settled above -- a block carries a url or a command, never
	// both -- so there is nothing left to refuse here: the same budget, the
	// same effects and the same naming apply either way, and the only thing
	// that differs is who owns the process at the other end.
	// Everything below is a raw block's own vocabulary. On an off block
	// Atenea never sees a call at all, so a budget here would be a rule with
	// nothing to apply it to -- and a rule that looks enforced but is not is
	// worse than an absent one.
	if out.Expose != ExposeRaw {
		switch {
		case len(m.Tools) > 0:
			return fail("mcp_server %s: tools needs expose = %q; a pointer never sees a call to filter", id, ExposeRaw)
		case len(m.Effects) > 0:
			return fail("mcp_server %s: effects needs expose = %q", id, ExposeRaw)
		case len(m.Tool) > 0:
			return fail("mcp_server %s: [[mcp_server.tool]] needs expose = %q", id, ExposeRaw)
		}
		return out, nil
	}
	// The allow list has no default and an empty one is refused rather than
	// obeyed. Both readings of an absent list are defensible -- offer
	// everything, offer nothing -- and that is exactly why neither may be
	// guessed: one silently widens a machine's surface to whatever a backend
	// happens to ship next week, the other is a declaration that does
	// nothing. The operator says which tools, or there is no backend.
	tools, err := namedTools(m.Tools)
	if err != nil {
		return fail("mcp_server %s: %v", id, err)
	}
	if len(tools) == 0 {
		return fail("mcp_server %s: expose = %q needs tools; an allow list is the budget, and it has no default",
			id, ExposeRaw)
	}
	out.Tools = tools
	// Atenea cannot know what a raw tool does. A backend's own list can hold
	// `execute_shell_command` beside `find_symbol`, and nothing in a name or
	// a schema says which is which, so the effects are declared by the
	// operator or the backend does not load. An undeclared effect is not a
	// quiet `read`.
	if len(m.Effects) == 0 {
		return fail("mcp_server %s: expose = %q needs effects; Atenea cannot infer what somebody else's tool does",
			id, ExposeRaw)
	}
	if out.Effects, err = namedEffects(m.Effects); err != nil {
		return fail("mcp_server %s: effects: %v", id, err)
	}
	for _, tool := range m.Tool {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return fail("mcp_server %s: [[mcp_server.tool]] needs a name", id)
		}
		// A per-tool block for a tool the allow list never named is a
		// permission nothing can ever consult: the call it describes is
		// refused one layer earlier. Left in, it reads as coverage.
		if !slices.Contains(out.Tools, name) {
			return fail("mcp_server %s: tool %s is not in tools", id, name)
		}
		if _, seen := out.ToolEffects[name]; seen {
			return fail("mcp_server %s: tool %s is declared twice", id, name)
		}
		if len(tool.Effects) == 0 {
			return fail("mcp_server %s: tool %s: effects is required; omit the block to inherit the server's",
				id, name)
		}
		narrowed, err := namedEffects(tool.Effects)
		if err != nil {
			return fail("mcp_server %s: tool %s: %v", id, name, err)
		}
		if out.ToolEffects == nil {
			out.ToolEffects = make(map[string][]contract.Effect, len(m.Tool))
		}
		out.ToolEffects[name] = narrowed
	}
	return out, nil
}

// namedTools trims an allow list and refuses a repeat. A name listed twice is
// not a wider permission -- it is two people editing the same file, and the
// second one may have meant a different tool.
func namedTools(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("tools: an empty tool name")
		}
		if slices.Contains(out, name) {
			return nil, fmt.Errorf("tools: %s is listed twice", name)
		}
		out = append(out, name)
	}
	return out, nil
}

// namedEffects reads a declared effect list. Unknown names are refused by
// contract.ParseEffect, so a typo cannot quietly narrow a permission to
// nothing.
func namedEffects(raw []string) ([]contract.Effect, error) {
	out := make([]contract.Effect, 0, len(raw))
	for _, name := range raw {
		effect, err := contract.ParseEffect(strings.TrimSpace(name))
		if err != nil {
			return nil, err
		}
		if slices.Contains(out, effect) {
			return nil, fmt.Errorf("%s is listed twice", effect)
		}
		out = append(out, effect)
	}
	return out, nil
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
			"settings %s: contract %s is not supported by this core (%s): %s",
			source, cfg.Contract, contract.Current, contractRemedy(cfg.Contract))
	}

	if cfg.Core, err = decoded.Core.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Orchestrator, err = decoded.Orchestrator.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Model, err = decoded.Model.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Workflow, err = decoded.Workflow.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Metrics, err = decoded.Metrics.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Backup, err = decoded.Backup.build(source); err != nil {
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
	// Two agent types under one name would make the resolver's answer depend
	// on declaration order, and a caller asking by name would silently get
	// whichever came first. Refusing is how the file says which it meant.
	named := make(map[string]bool, len(decoded.Agents))
	for _, raw := range decoded.Agents {
		agent, err := raw.build(source)
		if err != nil {
			return Config{}, err
		}
		if named[agent.Spec.Name] {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: agent %s is declared twice", source, agent.Spec.Name)
		}
		named[agent.Spec.Name] = true
		cfg.Agents = append(cfg.Agents, agent)
	}
	// A settings file written by `config init` is a copy, not a reference:
	// it replaces the shipped defaults rather than layering over them. Every
	// agent type shipped after that copy was taken is therefore invisible,
	// and the only symptom is `declared are none` at dispatch -- measured
	// 2026-08-14 on a file six weeks old, which had lost all five shipped
	// agents and read as a working configuration until something tried to
	// run one.
	//
	// Agents, and not the rest of the catalog, on purpose. An implementation
	// names a program that has to exist on this machine, and handing back
	// one a person removed because they do not have it would be Atenea
	// claiming something can answer when it cannot; that class is reported
	// by Missing instead, which says so without acting. An agent type is
	// inert until a plan names it.
	cfg.Agents = append(cfg.Agents, shippedAgents(source, named)...)
	// Two blocks under one id would silently make one of them dead: the
	// payload is a map keyed by id, so the later would win and the earlier
	// would never be probed or declared. Refusing is the only way the file
	// can say which one it meant.
	seen := make(map[string]bool, len(decoded.MCPServers))
	for _, raw := range decoded.MCPServers {
		server, err := raw.build(source)
		if err != nil {
			return Config{}, err
		}
		if seen[server.ID] {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: mcp_server %s is declared twice", source, server.ID)
		}
		seen[server.ID] = true
		cfg.MCPServers = append(cfg.MCPServers, server)
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
	// defaultBudgetUSD is what one commission may spend when the settings file
	// does not say. It is deliberately small: the paid far side that ships
	// answers a flat text search, and a search that needs more than this has
	// gone wrong. It used to be the ceiling on ONE invocation, which is why a
	// four-step run could quietly draw four times it.
	defaultBudgetUSD = 0.25
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

// contractRemedy names the edit that fixes a refused settings file, because
// the two numbers alone do not say which of them is meant to move.
//
// A file behind by a major version is the case that has a one-line fix, and
// it is the common one: the number a file declares is the core it was written
// for, no settings key has ever changed shape with it, and a `Health` -- the
// thing the 2.0.0 break removed -- never appears in a settings file at all.
// So the file is almost always already correct and only says the wrong year.
//
// Every other direction is a file this binary is too old to read, and no edit
// to the file can fix that. Saying "change the line" there would be advice
// that produces a second, more confusing failure.
func contractRemedy(declared contract.Version) string {
	if declared.Major < contract.Current.Major {
		return fmt.Sprintf("change the contract line to %q; no other key moves", contract.Current)
	}
	return "this file was written for a newer core; upgrade atenea"
}

func (o fileOrchestrator) build(source string) (Orchestrator, error) {
	out := Orchestrator{
		MaxParallel:   defaultMaxParallel,
		BudgetUSD:     defaultBudgetUSD,
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
			Timeout:         claudecode.DefaultTimeout,
		},
		Serena: SerenaAdapter{
			Endpoint:        serena.DefaultEndpoint,
			Implementations: serena.DefaultImplementations(),
			Timeout:         serena.DefaultTimeout,
		},
		CodebaseMemory: CodebaseMemoryAdapter{
			Binary:          codebasememory.DefaultBinary,
			Implementations: codebasememory.DefaultImplementations(),
			Timeout:         codebasememory.DefaultTimeout,
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
	if o.BudgetUSD != nil {
		if *o.BudgetUSD <= 0 {
			// Same trap as omp's match_limit, with money instead of matches:
			// zero reads as "no ceiling" and is the one value that must not
			// mean that. A commission with nothing stopping it is a runaway.
			//
			// A grant that reaches zero while running is a different thing and
			// perfectly ordinary -- see contract.Permission. This is somebody
			// typing it, which is always a mistake.
			return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.budget_usd must be above 0, got %v",
				source, *o.BudgetUSD)
		}
		out.BudgetUSD = *o.BudgetUSD
	}
	if len(o.Effects) > 0 {
		effects := make([]contract.Effect, 0, len(o.Effects))
		for _, name := range o.Effects {
			effect, err := contract.ParseEffect(name)
			if err != nil {
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.effects: %v", source, err)
			}
			effects = append(effects, effect)
		}
		out.StandingEffects = effects
	}
	// The client floor is resolved against whatever the standing grant just
	// became, so the order of these two blocks is load-bearing: reading it
	// before the block above would inherit the built-in default rather than
	// the operator's line.
	if o.ClientEffects == nil {
		out.ClientEffects = slices.Clone(out.StandingEffects)
		out.ClientEffectsInherited = true
	} else {
		effects := make([]contract.Effect, 0, len(*o.ClientEffects))
		for _, name := range *o.ClientEffects {
			effect, err := contract.ParseEffect(name)
			if err != nil {
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.client_effects: %v", source, err)
			}
			effects = append(effects, effect)
		}
		out.ClientEffects = effects
	}
	if o.Runners != nil {
		seen := make(map[string]struct{}, len(*o.Runners))
		list := make([]string, 0, len(*o.Runners))
		for _, name := range *o.Runners {
			switch name {
			case RunnerOMP, RunnerClaudeCode, RunnerSerena, RunnerCodebaseMemory, RunnerLocal:
			default:
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.runners has %q, which is not one of %s, %s, %s, %s, %s",
					source, name, RunnerOMP, RunnerClaudeCode, RunnerSerena, RunnerCodebaseMemory, RunnerLocal)
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
	memory, err := o.CodebaseMemory.build(source, out.CodebaseMemory)
	if err != nil {
		return Orchestrator{}, err
	}
	out.CodebaseMemory = memory
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

func (c fileCodebaseMemoryAdapter) build(source string, out CodebaseMemoryAdapter) (CodebaseMemoryAdapter, error) {
	if strings.TrimSpace(c.Binary) != "" {
		out.Binary = strings.TrimSpace(c.Binary)
	}
	if c.Implementations != nil {
		out.Implementations = *c.Implementations
	}
	if c.Timeout != "" {
		timeout, err := time.ParseDuration(c.Timeout)
		if err != nil {
			return CodebaseMemoryAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.codebasememory.timeout %q: %v", source, c.Timeout, err)
		}
		if timeout <= 0 {
			return CodebaseMemoryAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.codebasememory.timeout must be above 0, got %s", source, timeout)
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

const (
	// Six hours and five copies are the design's numbers, not a guess: a day
	// of history in four snapshots plus the one being replaced.
	defaultBackupEvery = 6 * time.Hour
	defaultBackupKeep  = 5
)

func (b fileBackup) build(source string) (Backup, error) {
	out := Backup{
		Dir:     platform.BackupDir(),
		Enabled: true,
		Every:   defaultBackupEvery,
		Keep:    defaultBackupKeep,
	}
	if strings.TrimSpace(b.Dir) != "" {
		out.Dir = strings.TrimSpace(b.Dir)
	}
	if b.Enabled != nil {
		out.Enabled = *b.Enabled
	}
	if b.Every != "" {
		every, err := time.ParseDuration(b.Every)
		if err != nil {
			return Backup{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: backup.every %q: %v", source, b.Every, err)
		}
		if every <= 0 {
			// Same trap as the metrics beats: zero reads like "never", and a
			// backup that never fires is the one maintenance task whose
			// absence is invisible until the day it is needed.
			return Backup{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: backup.every must be above 0, got %s; use enabled = false to stop copying",
				source, every)
		}
		out.Every = every
	}
	if b.Keep != nil {
		if *b.Keep < 1 {
			// Keeping zero copies is not a rotation setting, it is copying
			// switched off with the work still being done every six hours.
			return Backup{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: backup.keep must be at least 1, got %d; use enabled = false to stop copying",
				source, *b.Keep)
		}
		out.Keep = *b.Keep
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
	if s.Process != nil {
		process, err := s.Process.build(source)
		if err != nil {
			return SerenaAdapter{}, err
		}
		out.Process = &process
	}
	return out, nil
}

func (p fileManagedProcess) build(source string) (ManagedProcess, error) {
	if strings.TrimSpace(p.Command) == "" {
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.serena.process.command is required once the process table is present", source)
	}
	out := ManagedProcess{
		Command: strings.TrimSpace(p.Command),
		Args:    append([]string(nil), p.Args...),
		Env:     append([]string(nil), p.Env...),
		// The five duration knobs below are left at zero on purpose: the
		// supervisor defaults those, so the numbers live in one place.
		// This one cannot work that way. Zero is a legitimate restart
		// limit -- never retry, go straight to down -- so the supervisor
		// cannot tell it apart from "not set" and says so, deferring the
		// distinction to whoever builds the Spec. That is here, and the
		// pointer above is what still carries it.
		RestartLimit: supervisor.DefaultRestartLimit,
	}
	switch supervisor.Lifecycle(p.Lifecycle) {
	case supervisor.Persistent, supervisor.OnDemand:
		out.Lifecycle = supervisor.Lifecycle(p.Lifecycle)
	default:
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.serena.process.lifecycle must be %q or %q, got %q",
			source, supervisor.Persistent, supervisor.OnDemand, p.Lifecycle)
	}
	// idle_timeout describes the idle reaper, and the reaper skips persistent
	// servers by definition, so the two keys together are a contradiction
	// rather than a redundancy. Every other knob here that stops applying has
	// something beside it saying why -- an `enabled` set to false, a limit set
	// to zero, a status line that reads "off". This one would have nothing to
	// read anywhere, which is the only reason it is refused and they are not.
	if out.Lifecycle == supervisor.Persistent && strings.TrimSpace(p.IdleTimeout) != "" {
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.serena.process.idle_timeout has no meaning for a %q server: only %q servers are stopped when idle",
			source, supervisor.Persistent, supervisor.OnDemand)
	}
	if p.Port != nil {
		if *p.Port < 0 || *p.Port > 65535 {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.serena.process.port must be between 0 and 65535, got %d", source, *p.Port)
		}
		out.Port = *p.Port
	}
	switch Instance(strings.TrimSpace(p.Instance)) {
	case "":
		out.Instance = InstanceShared
	case InstanceShared, InstancePerRepository:
		out.Instance = Instance(strings.TrimSpace(p.Instance))
	default:
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.serena.process.instance must be %q or %q, got %q",
			source, InstanceShared, InstancePerRepository, p.Instance)
	}
	// The three rules below are all one rule seen from three sides: a
	// per-repository declaration has to be able to produce servers that
	// differ from each other, and a shared one has to be able to produce the
	// single server it promises.
	hasProject := slices.Contains(out.Args, ProjectPlaceholder)
	switch {
	case out.Instance == InstancePerRepository && !hasProject:
		// Without it every instance is the same command on a different
		// port, all pointed at whatever project the server picks for
		// itself -- N processes doing one process's work, and no error
		// anywhere to say so.
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.serena.process.instance = %q needs %s in args: "+
				"without it every repository would start the same server",
			source, InstancePerRepository, ProjectPlaceholder)
	case out.Instance == InstanceShared && hasProject:
		// The mirror image: a placeholder with nothing to substitute would
		// reach the command line verbatim, and the server would be asked to
		// open a project literally named `{{project}}`.
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s in args needs instance = %q; a shared server has no repository to substitute",
			source, ProjectPlaceholder, InstancePerRepository)
	case out.Instance == InstancePerRepository && out.Port != 0:
		// One port cannot hold two servers, so a fixed port silently caps
		// this policy at one working instance: the first repository binds
		// and every other one crashes on startup with an address already in
		// use. Zero is not merely the better default here, it is the only
		// answer that works.
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.serena.process.port cannot be fixed with instance = %q: "+
				"each repository needs its own port, so leave it unset and Atenea picks them",
			source, InstancePerRepository)
	}
	if p.RestartLimit != nil {
		if *p.RestartLimit < 0 {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.serena.process.restart_limit must not be negative, got %d", source, *p.RestartLimit)
		}
		out.RestartLimit = *p.RestartLimit
	}
	for _, d := range []struct {
		key   string
		raw   string
		field *time.Duration
	}{
		{"restart_delay", p.RestartDelay, &out.RestartDelay},
		{"stable_after", p.StableAfter, &out.StableAfter},
		{"ready_timeout", p.ReadyTimeout, &out.ReadyTimeout},
		{"idle_timeout", p.IdleTimeout, &out.IdleTimeout},
		{"stop_grace", p.StopGrace, &out.StopGrace},
	} {
		if d.raw == "" {
			continue
		}
		dur, err := time.ParseDuration(d.raw)
		if err != nil {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.serena.process.%s %q: %v", source, d.key, d.raw, err)
		}
		if dur <= 0 {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.serena.process.%s must be above 0, got %s", source, d.key, dur)
		}
		*d.field = dur
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

// defaultModelTimeout mirrors claudecode's own DefaultTimeout: a model turn
// through internal/agent/model runs the same CLI, on the same machine, so
// the same measured ceiling applies. Duplicated rather than imported --
// internal/config cannot import internal/agent/model without a cycle
// (internal/core, which that package dials for Atenea's own tools, already
// imports internal/config) -- so the two constants are kept in sync by hand.
const (
	defaultModelBinary  = "claude"
	defaultModelTimeout = 90 * time.Second
)

func (m fileModel) build(source string) (Model, error) {
	out := Model{Binary: defaultModelBinary, Timeout: defaultModelTimeout}
	if strings.TrimSpace(m.Binary) != "" {
		out.Binary = strings.TrimSpace(m.Binary)
	}
	if m.Timeout != "" {
		timeout, err := time.ParseDuration(m.Timeout)
		if err != nil {
			return Model{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: model.timeout %q: %v", source, m.Timeout, err)
		}
		if timeout <= 0 {
			return Model{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: model.timeout must be positive, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	// Empty is a valid, deliberate answer for a role: see Model.Explore's
	// own doc for why an unconfigured role is refused at dispatch rather
	// than defaulted here to some model that costs money by surprise.
	out.Explore = strings.TrimSpace(m.Explore)
	out.Plan = strings.TrimSpace(m.Plan)
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
			Enum:     f.Enum,
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
	scopeGuarantee, err := contract.ParseScopeGuarantee(i.ScopeGuarantee)
	if err != nil {
		return fail("scope_guarantee: %v", err)
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
		ID:             i.ID,
		Provider:       i.Provider,
		Capability:     i.Capability,
		ScopeGuarantee: scopeGuarantee,
		Constraints: contract.Constraints{
			Languages:     languages,
			RequiresIndex: i.Constraints.RequiresIndex,
			RequiresVCS:   i.Constraints.RequiresVCS,
			MinScale:      minScale,
			MaxScale:      maxScale,
			MaxInput:      maps.Clone(i.Constraints.MaxInput),
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
	vcs, err := contract.ParseVCS(r.VCS)
	if err != nil {
		return contract.Repository{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: repository %s: vcs: %v", source, r.ID, err)
	}
	out := contract.NewRepository(r.ID, r.Path, r.Languages, scale, vcs, r.IndexedBy)
	if err := out.Validate(); err != nil {
		return contract.Repository{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	return out, nil
}
