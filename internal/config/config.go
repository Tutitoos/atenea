// Package config reads Atenea's single settings file.
//
// Atenea is a declarative engine: the catalog of capabilities, the
// implementations behind them, the repositories they run against and the user's
// selector rules all live in this file. Changing behavior means editing it,
// not the core.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
	"github.com/Tutitoos/atenea/internal/adapter/codex"
	"github.com/Tutitoos/atenea/internal/adapter/desktop"
	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/adapter/scrapling"
	"github.com/Tutitoos/atenea/internal/adapter/serena"
	"github.com/Tutitoos/atenea/internal/adapter/tokensave"
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
	Workflow Workflow
	Metrics  Metrics
	Backup   Backup
	// Retention is how long receipts and traces are kept. See the type.
	Retention Retention
	Security  Security
	// Desktop is the allow-list for the desktop capabilities. See the type.
	Desktop Desktop
	// Web is where the web capabilities may reach. See the type.
	Web Web
	// LocalAgents caps the agent types a repository declares for itself.
	// Never the zero value: an absent block is DefaultLocalAgents.
	LocalAgents     LocalAgents
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
	// Dashboard is an optional HTTP(S) web UI belonging to this MCP. It is
	// metadata only: Atenea never opens it during startup or probing.
	Dashboard string
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
	// Instance controls the lifetime of a raw backend. Shared keeps one
	// upstream MCP session for the service; per_chat creates one for each
	// client connection and closes it when that connection ends. The field is
	// intentionally meaningful only for raw exposure: pointer servers are
	// owned by the client they are handed to.
	Instance Instance
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
	// HealthProbeEvery is how often the service probes declared MCP servers.
	// Zero disables the background probe; on-demand `detect` remains available.
	HealthProbeEvery time.Duration
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

// Retention is how long the record of what Atenea did is kept.
//
// It covers the two stores that grow without a shape of their own: the run
// receipts on disk and the agent traces in SQLite. The measurement base is not
// here -- it has a retention LADDER of its own, folding attempts into hours
// and hours into days, so it grows in detail rather than in rows.
//
// A receipt is the only record that a commission happened, and it carries the
// sentence somebody typed and what the run found. So this is a decision about
// what this machine is allowed to forget, and the default says ninety days
// rather than forever: long enough to answer "what did I ask last quarter",
// short enough that the five rotating backup copies stop compounding it.
type Retention struct {
	// Keep is how old a closed receipt or trace may be before it is removed.
	// Zero disables pruning entirely, which is a legitimate choice for a
	// machine whose state root is already managed elsewhere.
	Keep time.Duration
	// Every is how often the pass runs. It is guarded by a mark on disk, not
	// by a beat, for the reason metrics.CompactIfDue gives: most Atenea
	// processes are a command that lives for a second.
	Every time.Duration
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
	Runners    []string
	Local      LocalRunner
	OMP        OMPAdapter
	ClaudeCode ClaudeCodeAdapter
	Codex      CodexAdapter
	Serena     SerenaAdapter
	Kivgraph   KivgraphAdapter
	Tokensave  TokensaveAdapter
	Desktop    DesktopAdapter
	Scrapling  ScraplingAdapter
}

// Model fixes which model backs each of the two model-backed built-in
// agents, explore and plan, by role. Its fields mirror
// internal/agent/model.Options -- same names, same types, same order --
// because internal/config cannot import that package without a cycle
// (internal/core, which the model client dials for Atenea's own tools,
// already imports internal/config); the caller that holds both packages
// converts with a plain model.Options(cfg.Model) instead.
//
// Backend and model choice are fixed here, per role, and not chosen per task:
// there is
// no cost data yet a per-task choice could be justified against, and a knob
// nobody can tune correctly is worse than one fixed default that is visible
// in a file and changed by hand once it is wrong.
type Model struct {
	// Backend selects the model CLI protocol: "claude" or "opencode".
	// Empty is normalized to "claude" by the model client for compatibility
	// with older settings files.
	Backend string
	// Binary is the CLI executable that answers a model turn. A bare name
	// is looked up on PATH.
	Binary string
	// Timeout caps one turn. A model turn is slower than a tool call by
	// nature; see ClaudeCodeAdapter.Timeout for the same reasoning.
	Timeout time.Duration
	// Explore is the model name for the agent that explores a repository:
	// an alias the CLI resolves itself ("sonnet", "opus"), a full name, or
	// "auto" to let the decision router choose from its safe candidates.
	// Empty means the role has no model configured, so a dispatch to it is
	// refused rather than silently spending against a hardcoded default --
	// the same reason ClaudeCodeAdapter ships out of Runners.
	Explore string
	// Plan is the model name for the agent that turns a discovery graph into a
	// plan, or "auto" to select the pinned high-reasoning plan model.
	Plan string
	// ExploreFallbacks and PlanFallbacks are explicit provider-side fallbacks.
	// They are never inferred and travel with the selected route.
	ExploreFallbacks []string
	PlanFallbacks    []string
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
	// It is kept for compatibility with older settings; new settings should
	// use Source and TerminalBinary.
	Binary string
	// Source selects the CLI surface. "auto" currently means the terminal
	// Claude Code executable; Claude.app itself is a GUI client, not a
	// headless runner Atenea can safely invoke.
	Source string
	// TerminalBinary is the Claude Code executable used by the terminal and by
	// app installations that share Claude Code's user authentication state.
	TerminalBinary string
	// AppBinary is optional and must be a headless Claude Code executable if a
	// future app distribution provides one. Claude.app's GUI binary must not be
	// configured here.
	AppBinary string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one invocation. A model turn is slower than a tool call by
	// nature, so this sits far above omp's ceiling.
	Timeout time.Duration
}

// CodexAdapter configures the native Codex CLI adapter. It is kept separate
// from ClaudeCodeAdapter because the two CLIs have different invocation and
// output contracts.
type CodexAdapter struct {
	// Binary is a legacy explicit override. When set it wins over the source
	// candidates, so existing settings remain valid.
	Binary string
	// Source is "terminal", "app" or "auto". Auto tries the terminal binary
	// first and then the app-bundled CLI.
	Source          string
	TerminalBinary  string
	AppBinary       string
	Implementations []string
	Timeout         time.Duration
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
// The set is deliberately small and closed. Unknown values are refused rather
// than read as the default, for the reason every other refusal in this file
// exists -- a policy an operator believes is in force and is not is worse than
// one they can see is missing.
type Instance string

const (
	// InstanceShared is one process for the whole machine, and the default:
	// it is what every managed server did before this existed.
	InstanceShared Instance = "shared"
	// InstancePerChat is one upstream MCP session per client connection. It is
	// useful for servers whose session state belongs to a single conversation,
	// and is closed with that connection rather than retained by the service.
	InstancePerChat Instance = "per_chat"
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

// KivgraphAdapter configures the Kivgraph adapter.
//
// Kivgraph is the second far side that is not a CLI. For most of this
// adapter's life it was never anything else either: no proxy to point at, no
// externally managed instance to assume was already running, so the only
// child was a persistent stdio-MCP server Atenea itself spawned and
// supervised -- see Process below and supervisor.TransportStdio. Kivgraph
// 0.7.0 changed that: `kivgraph daemon` serves the same MCP tool surface over
// streamable HTTP at a fixed local URL, refuses every request without a
// bearer token (401), and is ordinarily already running under its own
// systemd user unit before Atenea starts, the same "assume it is there"
// shape Serena's own Endpoint assumes for its proxy. Endpoint and Token name
// that server; Process remains for the stdio child, now one of two ways to
// reach Kivgraph rather than the only one.
type KivgraphAdapter struct {
	// Endpoint is where the kivgraph daemon's MCP server is listening.
	// Mutually exclusive with Process: a server is reached one way, and
	// declaring both leaves an operator's later "the other one" edit
	// silently ignored instead of refused.
	Endpoint string
	// Token is the bearer token the daemon at Endpoint requires; the daemon
	// answers 401 without one. Meaningless without Endpoint, so build refuses
	// a Token with no Endpoint to send it to.
	Token string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. It sits at Serena's own
	// ceiling: opening a graph database cold is slow long before it is
	// stuck.
	Timeout time.Duration
	// Dashboard is the optional read-only web viewer exposed by a separate
	// Kivgraph UI process. The viewer is deliberately a different process and
	// transport from whichever one of Endpoint or Process reaches the MCP
	// server itself.
	Dashboard string
	// DashboardProcess is the optional supervised HTTP viewer process.
	DashboardProcess *ManagedProcess
	// Process launches and supervises the kivgraph server itself over a
	// stdio transport, the alternative to dialing Endpoint. Optional now
	// that Endpoint exists: build refuses both set and refuses neither, so a
	// non-nil Process here always means Endpoint is empty.
	Process *ManagedProcess
}

// TokensaveAdapter configures the tokensave adapter.
//
// Same transport as Kivgraph -- a stdio MCP server Atenea spawns and
// supervises -- and one field neither Kivgraph nor Serena needs: Root.
// Kivgraph publishes one corpus addressed by repository NAME, while
// tokensave serves ONE project rooted at a directory and speaks paths
// relative to it, so the adapter cannot translate a repository-relative path
// in either direction without being told where that root is. It is declared
// rather than derived: a root guessed from the repository list would be
// silently wrong the day a repository is added outside it.
type TokensaveAdapter struct {
	// Root is the directory the supervised server was pointed at, absolute.
	// Every repository this adapter answers for lives under it.
	Root string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. It sits at Kivgraph's own ceiling: tokensave
	// re-syncs its index before answering, which is slow long before it is
	// stuck.
	Timeout time.Duration
	// Process launches and supervises the tokensave server itself. As with
	// Kivgraph this is not optional: a stdio server has no address to dial.
	Process *ManagedProcess
}

// DesktopAdapter drives this machine's own screen, pointer and accessibility
// tree, through a helper process that owns every macOS API involved.
//
// It is off unless `runners` names it, and that is not the usual caution about
// a young feature. What sits behind it is a permission Atenea cannot grant
// itself and cannot revoke: macOS attributes a device grant to the responsible
// ancestor, so an Atenea started from a shell borrows that shell's screen and
// input access. The adapter refuses the device effect in that case rather than
// succeeding on somebody else's permission.
type DesktopAdapter struct {
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. Sized for latency rather than payload: walking
	// an accessibility tree is one IPC message per node, and a browser
	// measured 609ms while its whole tree was 91KB.
	Timeout time.Duration
	// Process launches and supervises the helper. Not optional, for the same
	// reason as Kivgraph and tokensave: a stdio server has no address to dial.
	Process *ManagedProcess
}

// ScraplingAdapter reaches the open web through a Scrapling MCP server.
//
// It is off unless `runners` names it, like every adapter that can cause an
// effect beyond reading this disk. What sits behind it is a fetcher, and the
// gate on where it may point lives in [web] rather than here: this block says
// how to launch the server and how long to wait for it, and the settings that
// decide what it may reach are kept somewhere a reader will find them without
// knowing which adapter implements the capability.
type ScraplingAdapter struct {
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. Sized for the stealth level rather than the
	// average: a plain request is milliseconds, and a stealth render starts a
	// browser and waits out a challenge.
	Timeout time.Duration
	// Process launches and supervises the server. Not optional, for the same
	// reason as Kivgraph, tokensave and desktop: a stdio server has no
	// address to dial.
	Process *ManagedProcess
}

// Desktop is what the desktop capabilities are allowed to look at.
//
// Two lists rather than one because they answer different questions and one of
// them must not be editable into a permission. Applications is what may be
// read; Denied is what may never be, whatever the first list says. A single
// list would make "never look at my password manager" a thing you state by
// omission, and omission is not a statement -- it is what happens when
// somebody adds an entry in a hurry.
type Desktop struct {
	// Applications is the allow-list, by bundle identifier. EMPTY MEANS DENY
	// ALL, which is the opposite of the usual reading and is the whole point:
	// a capability that could read every window on somebody's machine must
	// not be enabled by a settings file that forgot to mention it.
	//
	// Bundle identifiers rather than display names: a name is localized and
	// changes under the reader's feet, and two applications may share one.
	Applications []string
	// Denied always wins, and is seeded rather than empty. The defaults are
	// the applications where a single screenshot is a credential: password
	// managers, the keychain, banking. An operator who deletes the block gets
	// them back; one who writes an explicit empty list has said something.
	Denied []string
}

// DefaultDesktop is the shipped posture: nothing allowed, and the obvious
// hazards refused even if somebody allows them later.
func DefaultDesktop() Desktop {
	return Desktop{
		Applications: nil,
		Denied: []string{
			"com.apple.keychainaccess",
			"com.1password.1password",
			"com.agilebits.onepassword7",
			"com.bitwarden.desktop",
			"com.lastpass.LastPass",
			"org.keepassxc.keepassxc",
			"in.sinew.Enpass-Desktop",
			"com.dashlane.dashlanephonefinal",
		},
	}
}

// Web is where the `web.*` capabilities are allowed to reach.
//
// Two lists, like Desktop, and for the same reason: one of them must not be
// editable into a permission by omission. What is deliberately NOT like
// Desktop is the reading of an empty allow-list. There, empty denies
// everything, because any window on a machine can be a credential. Here it
// does not, because an arbitrary public web page is not the hazard -- the
// hazard is the inside of this network, and refusing that is Denied's job.
//
// Inverting Domains as well would have put the maintenance (one settings edit
// per site anybody wants to read) exactly where the risk is not, and the
// predictable result of that trade is somebody emptying Denied to make the
// nuisance stop.
type Web struct {
	// Domains narrows what may be reached, by host. EMPTY MEANS ANY PUBLIC
	// HOST; a non-empty list means those and nothing else. A bare domain
	// covers its subdomains, so "example.com" reaches "api.example.com".
	Domains []string
	// Denied always wins, and is seeded rather than empty. It holds CIDR
	// blocks and host patterns in one list because what it refuses is one
	// idea -- the private side of this network -- that is spelled two ways.
	//
	// An explicitly empty Denied is honored, and it is the one setting in
	// this file that hands out an unrestricted HTTP client. Somebody who
	// writes it has said so out loud, which is the only form of that request
	// worth answering.
	Denied []string
}

// DefaultWeb is the shipped posture: the open web, minus this machine and the
// network it sits on.
//
// The link-local block is the one worth naming. 169.254.169.254 is the cloud
// metadata endpoint on every major provider, it answers over plain HTTP with
// no authentication at all, and reaching it is the single most valuable thing
// an attacker can do with somebody else's fetcher.
func DefaultWeb() Web {
	return Web{
		Domains: nil,
		Denied: []string{
			"127.0.0.0/8",
			"::1/128",
			"169.254.0.0/16",
			"fe80::/10",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
			"fc00::/7",
			"0.0.0.0/8",
			"*.lan",
			"*.local",
			"*.internal",
			"localhost",
		},
	}
}

// Security is the one place delicate files are declared.
type Security struct {
	// Sensitive holds the path patterns that carry secrets. Reading is free by
	// default; these are the exception, and while exploring they are skipped in
	// silence rather than turned into a question.
	Sensitive []string
}

// LocalAgents is the ceiling on an agent type a repository declares in its
// own `.atenea/config.toml`.
//
// It exists because "may only narrow" needs something to be narrower than. A
// repository redefining a shipped type is measured against that type, but a
// repository adding a NEW one has no counterpart, and a rule with no referent
// is not a rule. This is the referent.
//
// The defaults are the whole design. A settings file that predates this
// feature says nothing about it, which is every settings file in existence
// when it shipped -- and an absent ceiling that reads as no ceiling is the
// same failure as an unmeasured cost that reads as free. So the zero value of
// this struct is never what applies: [DefaultLocalAgents] is, and the file
// may only be read on top of it. The same shape is already documented one
// field up in Config.Missing, where a settings file predating a release never
// gained what that release shipped and nothing said so.
type LocalAgents struct {
	// Effects is the most a locally declared type may cause. Read alone by
	// default: it is what every type this project ships holds, so it is the
	// floor of usefulness rather than a token gesture.
	//
	// An explicitly empty list turns the feature off -- a type that may
	// cause nothing is refused by AgentType.Validate -- which is the switch
	// for a machine that wants none of this.
	Effects []contract.Effect
	// Context is the most a locally declared type may be served. The
	// repository alone by default, because it is the only level that cannot
	// leak: `workspace` is the catalog of every repository on this machine
	// and `history` is what other runs of the same type were told.
	Context []contract.ContextLevel
	// Limits caps one run of a locally declared type. Zero on either field
	// means the machine states no number of its own and the ceiling is the
	// one already in force: the limits of the shipped type being run, which
	// a local type may lower and never raise. That is a real bound, so an
	// omitted key here is not an open door -- it is a deferral to a door
	// that is already shut.
	Limits contract.Limits
}

// orDefault fills in what nobody stated, field by field.
//
// Two ways to arrive here with nothing set, and they need the same answer.
// The settings file may have no [local_agents] block, which build already
// handles; or a Config may be assembled in code -- a test, an embedder --
// where the zero value is nil rather than the default. Left alone, nil reads
// as an empty list, which is the machine allowing local types NOTHING: fail
// closed, and safe, but a feature that quietly stops working is not the same
// as one that was turned off on purpose. An explicitly empty list still means
// off, because empty and absent stay different all the way down.
func (l LocalAgents) orDefault() LocalAgents {
	fallback := DefaultLocalAgents()
	if l.Effects == nil {
		l.Effects = fallback.Effects
	}
	if l.Context == nil {
		l.Context = fallback.Context
	}
	return l
}

// DefaultLocalAgents is what applies when the settings file says nothing.
func DefaultLocalAgents() LocalAgents {
	return LocalAgents{
		Effects: []contract.Effect{contract.EffectRead},
		Context: []contract.ContextLevel{contract.ContextRepository},
	}
}

// RunnerOMP, RunnerClaudeCode, RunnerCodex, RunnerSerena, RunnerKivgraph,
// RunnerTokensave, RunnerDesktop, RunnerScrapling and RunnerLocal are the
// values orchestrator.runners accepts.
const (
	RunnerOMP        = "omp"
	RunnerClaudeCode = "claudecode"
	RunnerCodex      = "codex"
	RunnerSerena     = "serena"
	RunnerKivgraph   = "kivgraph"
	RunnerTokensave  = "tokensave"
	RunnerDesktop    = "desktop"
	RunnerScrapling  = "scrapling"
	RunnerLocal      = "local"
)

// DefaultPath returns where Atenea looks for its settings when nothing else
// says otherwise.
func DefaultPath() string { return filepath.Join(platform.ConfigDir(), "atenea.toml") }

// settingsOrigin says who chose the settings path. Load needs it because the
// path alone cannot tell "nobody asked for a file" apart from "$ATENEA_CONFIG
// asked for one": both arrive as a plain string, and only the first may fall
// back to the built-in defaults.
type settingsOrigin int

const (
	originDefault settingsOrigin = iota
	originEnv
	originExplicit
)

// ResolvePath picks the settings file: an explicit path wins, then
// ATENEA_CONFIG, then the default location.
func ResolvePath(explicit string) string {
	path, _ := resolvePath(explicit)
	return path
}

// resolvePath is ResolvePath with the origin the caller was asked for kept.
func resolvePath(explicit string) (string, settingsOrigin) {
	if explicit != "" {
		return explicit, originExplicit
	}
	if fromEnv := os.Getenv("ATENEA_CONFIG"); fromEnv != "" {
		return fromEnv, originEnv
	}
	return DefaultPath(), originDefault
}

// Load reads the settings file at path. A missing file at the default location
// is not an error: Atenea falls back to the built-in defaults so a fresh
// install boots without ceremony. A missing file that somebody asked for -- by
// flag or by $ATENEA_CONFIG -- is an error, because staying quiet there would
// hide a typo, and a daemon booting on the built-in catalog instead of the
// operator's is a failure that only shows up as wrong answers much later.
func Load(explicit string) (Config, error) {
	path, origin := resolvePath(explicit)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		cfg, parseErr := parse(raw, path)
		if parseErr != nil {
			return Config{}, parseErr
		}
		cfg.Missing = missingImplementations(cfg)
		return cfg, nil
	case errors.Is(err, fs.ErrNotExist) && origin == originDefault:
		return Defaults()
	case errors.Is(err, fs.ErrNotExist) && origin == originEnv:
		return Config{}, contract.Fail(contract.FailureNotFound,
			"settings file %s named by ATENEA_CONFIG: %v", path, err)
	default:
		return Config{}, contract.Fail(contract.FailureNotFound,
			"settings file %s: %v", path, err)
	}
}

// groundedDefaults is the embedded settings with the shipped repository's
// relative path replaced by where this command is being run.
//
// `path = "."` is right for the file that ships and wrong for a file on disk.
// It is what makes a fresh install work against the tree you are standing in
// with no settings at all -- the CLI stands somewhere, so "." means something.
// The moment it is written down it stops being a convenience and becomes a
// declaration, read later by a service that stands nowhere, and a service is
// refused for exactly that reason.
//
// So `config init` writes what "." meant at the moment it was typed. A person
// running it inside a repository gets that repository, spelled out, and the
// file goes on saying the same thing after they cd somewhere else.
//
// A working directory that cannot be read leaves the file as shipped rather
// than failing: the settings are still writable and still valid, and the
// service's own refusal names the remedy.
func groundedDefaults() ([]byte, error) {
	// Written as "only on success" rather than as two early returns, so that
	// falling back reads as one decision instead of as two swallowed errors.
	absolute := ""
	if here, err := os.Getwd(); err == nil {
		if resolved, err := filepath.Abs(here); err == nil {
			absolute = resolved
		}
	}
	if absolute == "" {
		return defaultSettings, nil
	}
	// The home directory is not a repository, and writing it down as one is
	// worse than leaving the path relative.
	//
	// This is the same failure the service's WorkingDirectory exists to
	// prevent -- a `code.search` from any chat raking Documents, mail, .ssh
	// and .aws -- except recorded on disk, where it survives every restart and
	// looks like a deliberate choice. It is also the likeliest way to reach it:
	// a shell after ssh starts in $HOME, and `config init` is the command the
	// documentation sends an operator to. So it is refused by name, with the
	// one thing that fixes it.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if resolved, err := filepath.Abs(home); err == nil && resolved == absolute {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"`config init` writes down the directory it is run in, and this is %s -- "+
					"a home directory is not a repository, and declaring it as one lets any "+
					"chat search the whole of it; run this from the repository you mean",
				absolute)
		}
	}
	// Anchored on the whole line, so this cannot reach a `path = "."` that
	// belongs to some other block a later edit adds.
	quoted, err := tomlString(absolute)
	if err != nil {
		return nil, err
	}
	grounded := relativeRepositoryPath.ReplaceAll(defaultSettings,
		[]byte("path = "+quoted))
	if bytes.Equal(grounded, defaultSettings) {
		return nil, contract.Fail(contract.FailureUnavailable,
			"the embedded settings no longer declare a repository at %q, so "+
				"`config init` cannot ground it", ".")
	}
	return grounded, nil
}

// tomlString writes s as a TOML basic string.
//
// strconv.Quote was the obvious thing and it is the wrong one: it produces Go
// escapes, and the two languages do not agree. Go emits \a for 0x07 and \v for
// 0x0b, which TOML's grammar does not define -- a directory whose name carries
// one produced a settings file that would not parse afterwards. Worse, Go emits
// \xNN for a byte that is not valid UTF-8, which TOML's lexer does accept and
// reads as the code point U+00NN: the path written down stops being the path
// that was meant, silently. A file name on Linux is any byte sequence without a
// NUL, so neither case is theoretical.
//
// TOML defines \b \t \n \f \r \" \\ and the two \u forms, so everything that
// needs escaping goes out as \uXXXX. A path that is not valid UTF-8 cannot be
// written as a TOML string at all, and is refused here rather than mangled.
func tomlString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"the path %q is not valid UTF-8, so it cannot be written into a settings file",
			s)
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			// Control characters are the only other thing TOML forbids raw in
			// a basic string. Everything else -- accents, CJK, emoji -- is
			// written as itself, because a settings file an operator opens
			// should show the directory they typed.
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// relativeRepositoryPath matches the one line groundedDefaults rewrites.
var relativeRepositoryPath = regexp.MustCompile(`(?m)^path = "\."$`)

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
//
// The modes are 0700 on the directory and 0600 on the file, which is what the
// rest of this repository already uses for anything that can hold a secret --
// checkpoint, registry, backendstate, ipc, notebook, backup. This file is
// where `[[mcp_server]].env` and every `process` block's `env` live, and those
// are KEY=VALUE maps: the natural place for an API token. Nothing here is
// meant to be read by another account.
//
// The write goes through a temporary file and a rename because os.WriteFile
// over an existing path opens it with O_TRUNC: a `config init --force` that
// dies halfway -- full disk, signal -- would leave a truncated atenea.toml and
// no copy of what was there. Rename is the same shape internal/platform's
// writeUnit and internal/core/backendstate already use, for the same reason.
func WriteDefault(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return contract.Fail(contract.FailureInvalidInput,
			"settings file %s already exists; pass --force to overwrite", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"creating %s: %v", dir, err)
	}
	temp, err := os.CreateTemp(dir, "atenea.*.tmp")
	if err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"creating a temporary file in %s: %v", dir, err)
	}
	name := temp.Name()
	// The temporary file leaves here in exactly one shape: renamed into place.
	// Every other exit takes it along, so a failed init never leaves a stray
	// half-written catalog next to the real one.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()
	body, err := groundedDefaults()
	if err != nil {
		_ = temp.Close()
		return err
	}
	// Parsed before it is written, not after it is in place.
	//
	// This file is generated by substitution into an embedded template, which
	// is exactly the kind of thing that is correct until the day it is not:
	// a path needing an escape, an edit to the template, a regex that matched
	// one line too many. Nothing downstream re-reads the file before the
	// operator does, so an unparseable one would first be noticed by a service
	// refusing to start. Reading it back here costs one parse of a file this
	// function just built.
	if _, err := parse(body, path); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailureUnavailable,
			"the settings this would write do not parse, so nothing was written: %v", err)
	}
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailurePermissionDenied,
			"writing %s: %v", name, err)
	}
	// CreateTemp already makes it 0600; saying so here is what keeps the mode
	// from depending on that detail of the standard library.
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailurePermissionDenied,
			"setting the mode of %s: %v", name, err)
	}
	if err := temp.Close(); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"closing %s: %v", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"writing %s: %v", path, err)
	}
	renamed = true
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
	Retention       fileRetention    `toml:"retention"`
	Backup          fileBackup       `toml:"backup"`
	Security        fileSecurity     `toml:"security"`
	Desktop         fileDesktop      `toml:"desktop"`
	Web             fileWeb          `toml:"web"`
	LocalAgents     fileLocalAgents  `toml:"local_agents"`
	Selector        fileSelector     `toml:"selector"`
	Capabilities    []fileCapability `toml:"capability"`
	Implementations []fileImpl       `toml:"implementation"`
	Repositories    []fileRepository `toml:"repository"`
	Agents          []fileAgent      `toml:"agent"`
	MCPServers      []fileMCPServer  `toml:"mcp_server"`
}

// fileModel is [model] as written.
type fileModel struct {
	Backend          string   `toml:"backend"`
	Binary           string   `toml:"binary"`
	Timeout          string   `toml:"timeout"`
	Explore          string   `toml:"explore"`
	Plan             string   `toml:"plan"`
	ExploreFallbacks []string `toml:"explore_fallbacks"`
	PlanFallbacks    []string `toml:"plan_fallbacks"`
}

type fileCore struct {
	ShutdownGrace    string `toml:"shutdown_grace"`
	HealthProbeEvery string `toml:"health_probe_every"`
}

type fileMetrics struct {
	Path        string `toml:"path"`
	Enabled     *bool  `toml:"enabled"`
	Flush       string `toml:"flush"`
	Compact     string `toml:"compact"`
	BufferLimit *int   `toml:"buffer_limit"`
}

type fileRetention struct {
	Keep  string `toml:"keep"`
	Every string `toml:"every"`
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
	Runners    *[]string             `toml:"runners"`
	Local      fileLocalRunner       `toml:"local"`
	OMP        fileOMPAdapter        `toml:"omp"`
	ClaudeCode fileClaudeCodeAdapter `toml:"claudecode"`
	Codex      fileCodexAdapter      `toml:"codex"`
	Serena     fileSerenaAdapter     `toml:"serena"`
	Kivgraph   fileKivgraphAdapter   `toml:"kivgraph"`
	Tokensave  fileTokensaveAdapter  `toml:"tokensave"`
	Desktop    fileDesktopAdapter    `toml:"desktop"`
	Scrapling  fileScraplingAdapter  `toml:"scrapling"`
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

// fileLocalAgents is [local_agents] as written. Pointers throughout, so that
// an omitted key inherits the default and an explicitly empty list is the
// machine saying no -- the same distinction fileSecurity draws, for a closer
// reason: `effects = []` here refuses every locally declared type, and
// leaving `effects` out must not be read as the same statement.
type fileLocalAgents struct {
	Effects     *[]string `toml:"effects"`
	Context     *[]string `toml:"context"`
	MaxDuration *string   `toml:"max_duration"`
	MaxTokens   *int      `toml:"max_tokens"`
}

type fileClaudeCodeAdapter struct {
	Binary          string    `toml:"binary"`
	Source          string    `toml:"source"`
	TerminalBinary  string    `toml:"terminal_binary"`
	AppBinary       string    `toml:"app_binary"`
	Implementations *[]string `toml:"implementations"`
	Timeout         string    `toml:"timeout"`
}

type fileCodexAdapter struct {
	Binary          string    `toml:"binary"`
	Source          string    `toml:"source"`
	TerminalBinary  string    `toml:"terminal_binary"`
	AppBinary       string    `toml:"app_binary"`
	Implementations *[]string `toml:"implementations"`
	Timeout         string    `toml:"timeout"`
}

type fileSerenaAdapter struct {
	Endpoint        string              `toml:"endpoint"`
	Implementations *[]string           `toml:"implementations"`
	Timeout         string              `toml:"timeout"`
	Process         *fileManagedProcess `toml:"process"`
}

// fileKivgraphAdapter is the TOML shape of KivgraphAdapter. It reuses
// fileManagedProcess for its own .process table: the shape a supervised
// server takes in the settings file does not depend on which transport it
// talks over, only fileManagedProcess.build's caller does (see the section
// parameter it takes below).
type fileKivgraphAdapter struct {
	Endpoint         string              `toml:"endpoint"`
	Token            string              `toml:"token"`
	Implementations  *[]string           `toml:"implementations"`
	Timeout          string              `toml:"timeout"`
	Dashboard        string              `toml:"dashboard"`
	DashboardProcess *fileManagedProcess `toml:"dashboard_process"`
	Process          *fileManagedProcess `toml:"process"`
}

// fileTokensaveAdapter is the TOML shape of TokensaveAdapter. `root` is the
// one key its neighbor has no counterpart for: see TokensaveAdapter.Root.
type fileDesktopAdapter struct {
	Implementations *[]string           `toml:"implementations"`
	Timeout         string              `toml:"timeout"`
	Process         *fileManagedProcess `toml:"process"`
}

// fileScraplingAdapter is the TOML shape of ScraplingAdapter. Identical to
// its desktop neighbor, and kept as its own type rather than shared with it
// so that the two can grow apart without one of them silently gaining a key
// that means nothing on the other.
type fileScraplingAdapter struct {
	Implementations *[]string           `toml:"implementations"`
	Timeout         string              `toml:"timeout"`
	Process         *fileManagedProcess `toml:"process"`
}

type fileTokensaveAdapter struct {
	Root            string              `toml:"root"`
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

// fileSecurity uses a pointer so an omitted list and an explicitly empty one
// are different things. Leaving the block out must not quietly disarm the
// guard; emptying it on purpose is the user's call to make.
type fileSecurity struct {
	Sensitive *[]string `toml:"sensitive"`
}

type fileDesktop struct {
	Applications *[]string `toml:"applications"`
	Denied       *[]string `toml:"denied"`
}

// fileWeb is [web] as written. Pointers, so an omitted list inherits the
// shipped one and an explicitly empty list is honored as the statement it is.
type fileWeb struct {
	Domains *[]string `toml:"domains"`
	Denied  *[]string `toml:"denied"`
}

type fileSelector struct {
	Rules            []fileRule `toml:"rule"`
	HealthStaleAfter string     `toml:"health_stale_after"`
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
	// SubjectFrom names the input that says what a call is about beyond the
	// repository, and SubjectKind says how to read it. Both absent on every
	// capability that has no such thing, which is most of them.
	SubjectFrom string `toml:"subject_from"`
	SubjectKind string `toml:"subject_kind"`
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
	ID        string            `toml:"id"`
	URL       string            `toml:"url"`
	Command   []string          `toml:"command"`
	Env       map[string]string `toml:"env"`
	Dashboard string            `toml:"dashboard"`
	Timeout   string            `toml:"timeout"`
	Expose    string            `toml:"expose"`
	Instance  string            `toml:"instance"`
	Tools     []string          `toml:"tools"`
	Effects   []string          `toml:"effects"`
	Tool      []fileMCPTool     `toml:"tool"`
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
	out := MCPServer{ID: id, Command: m.Command, Env: m.Env, Dashboard: strings.TrimSpace(m.Dashboard)}
	if out.Dashboard != "" {
		validated, err := validateDashboardURL(source, "mcp_server", id, out.Dashboard)
		if err != nil {
			return MCPServer{}, err
		}
		out.Dashboard = validated
	}
	if hasURL {
		validated, err := validateAbsoluteURL(source, "mcp_server", id, "url", m.URL)
		if err != nil {
			return MCPServer{}, err
		}
		out.URL = validated
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
	instance := strings.TrimSpace(m.Instance)
	if instance == "" {
		out.Instance = InstanceShared
	} else {
		switch Instance(instance) {
		case InstanceShared, InstancePerChat:
			out.Instance = Instance(instance)
		default:
			return fail("mcp_server %s: instance %q is not %s or %s", id, instance, InstanceShared, InstancePerChat)
		}
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
		case out.Instance == InstancePerChat:
			return fail("mcp_server %s: instance = %q needs expose = %q", id, InstancePerChat, ExposeRaw)
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

// validateAbsoluteURL checks that raw is a URL a plain http(s) client could
// dial: some scheme, some host, nothing fancier. This is the shape the far
// side of an MCP connection itself takes -- mcp_server's own url and
// KivgraphAdapter's Endpoint -- as opposed to validateDashboardURL just
// below, which is a viewer bolted onto a server rather than the server
// itself and carries a dashboard alias requirement neither of these needs.
func validateAbsoluteURL(source, section, id, field, raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: %s %q is not an absolute http(s) url", source, section, id, field, raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: %s scheme %q is not http or https", source, section, id, field, parsed.Scheme)
	}
	return parsed.String(), nil
}

func validateDashboardURL(source, section, id, raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: dashboard %q is not an absolute http(s) url", source, section, id, raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: dashboard scheme %q is not http or https", source, section, id, parsed.Scheme)
	}
	if parsed.User != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: dashboard must not contain credentials", source, section, id)
	}
	if parsed.Hostname() == "" {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: dashboard has no host", source, section, id)
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s %s: dashboard port %q is invalid", source, section, id, port)
		}
	}
	if !dnsLabel(id) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %s: id is not a valid DNS label for its dashboard alias", source, section, id)
	}
	return parsed.String(), nil
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

// dnsLabel is deliberately narrower than a general hostname. Dashboard
// aliases are one local label, not a path or a multi-label name, so the
// mapping remains deterministic and safe to place in a hosts file.
func dnsLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
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
	if cfg.LocalAgents, err = decoded.LocalAgents.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Metrics, err = decoded.Metrics.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Retention, err = decoded.Retention.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Backup, err = decoded.Backup.build(source); err != nil {
		return Config{}, err
	}
	cfg.Security = decoded.Security.build()
	cfg.Desktop = decoded.Desktop.build()
	cfg.Web = decoded.Web.build()
	for _, rule := range decoded.Selector.Rules {
		cfg.Selector.Rules = append(cfg.Selector.Rules, selector.Rule{
			Capability: rule.Capability,
			Repository: rule.Repository,
			Prefer:     rule.Prefer,
		})
	}
	if raw := strings.TrimSpace(decoded.Selector.HealthStaleAfter); raw != "" {
		staleAfter, parseErr := time.ParseDuration(raw)
		if parseErr != nil || staleAfter <= 0 {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: selector.health_stale_after must be a positive duration", source)
		}
		cfg.Selector.HealthStaleAfter = staleAfter
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
	//
	// A dashboard alias is the server's own id, so the same check covers it:
	// there used to be a second map here guarding aliases, and it could never
	// fire, because two blocks claiming one alias are two blocks claiming one
	// id and the line above has already refused them. Its message even named
	// the same id twice. The conflict that IS real -- a [[mcp_server]] whose
	// alias collides with the graph viewer's -- is between this file and
	// orchestrator.kivgraph.dashboard, and dashboard.AllConfig makes it where
	// both are in hand.
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
	defaultBudgetUSD = 0.90
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

var defaultCodexImplementations = []string{"codex.search"}

// Los defaults de kivgraph y tokensave se leen de sus propios paquetes, como
// ya hace serena: un numero declarado dos veces es un
// numero que puede discrepar. Ninguno de los dos adapters importa
// internal/config, asi que no hay ciclo posible.

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
			Source:          "auto",
			TerminalBinary:  claudecode.DefaultBinary,
			Implementations: defaultClaudeImplementations,
			Timeout:         claudecode.DefaultTimeout,
		},
		Codex: CodexAdapter{
			Source:          "auto",
			TerminalBinary:  codex.DefaultTerminalBinary,
			AppBinary:       codex.DefaultAppBinary,
			Implementations: defaultCodexImplementations,
			Timeout:         codex.DefaultTimeout,
		},
		Serena: SerenaAdapter{
			Endpoint:        serena.DefaultEndpoint,
			Implementations: serena.DefaultImplementations(),
			Timeout:         serena.DefaultTimeout,
		},
		Kivgraph: KivgraphAdapter{
			Endpoint:        kivgraph.DefaultEndpoint,
			Implementations: kivgraph.DefaultImplementations(),
			Timeout:         kivgraph.DefaultTimeout,
		},
		Tokensave: TokensaveAdapter{
			Implementations: tokensave.DefaultImplementations(),
			Timeout:         tokensave.DefaultTimeout,
		},
		Desktop: DesktopAdapter{
			Implementations: desktop.DefaultImplementations(),
			Timeout:         desktop.DefaultTimeout,
		},
		Scrapling: ScraplingAdapter{
			Implementations: scrapling.DefaultImplementations(),
			Timeout:         scrapling.DefaultTimeout,
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
			case RunnerOMP, RunnerClaudeCode, RunnerCodex, RunnerSerena, RunnerKivgraph,
				RunnerTokensave, RunnerDesktop, RunnerScrapling, RunnerLocal:
			default:
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.runners has %q, which is not one of %s, %s, %s, %s, %s, %s, %s, %s, %s",
					source, name, RunnerOMP, RunnerClaudeCode, RunnerCodex, RunnerSerena,
					RunnerKivgraph, RunnerTokensave, RunnerDesktop, RunnerScrapling, RunnerLocal)
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
	checkpointDir, err := settingsPath(source, "orchestrator.checkpoint_dir", o.CheckpointDir)
	if err != nil {
		return Orchestrator{}, err
	}
	if checkpointDir != "" {
		out.CheckpointDir = checkpointDir
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
	codexAdapter, err := o.Codex.build(source, out.Codex)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Codex = codexAdapter
	symbols, err := o.Serena.build(source, out.Serena)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Serena = symbols
	graph, err := o.Kivgraph.build(source, out.Kivgraph)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Kivgraph = graph
	index, err := o.Tokensave.build(source, out.Tokensave)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Tokensave = index
	screen, err := o.Desktop.build(source, out.Desktop)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Desktop = screen
	web, err := o.Scrapling.build(source, out.Scrapling)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Scrapling = web
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
	path, err := settingsPath(source, "metrics.path", m.Path)
	if err != nil {
		return Metrics{}, err
	}
	if path != "" {
		out.Path = path
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
	// Ninety days, spelled in hours because time.ParseDuration has no day.
	//
	// Measured on the machine this was written on: 182 receipts in five days,
	// 82 on the busiest, at about 4 KiB each. Ninety days is therefore around
	// 30 MB of receipts and 6 MB of traces -- and, because every backup copy
	// carries the whole state root, five times that in the rotation. Forever
	// was the previous number and it was not chosen, it was the absence of a
	// choice.
	defaultRetentionKeep  = 2160 * time.Hour
	defaultRetentionEvery = 24 * time.Hour
)

func (r fileRetention) build(source string) (Retention, error) {
	out := Retention{Keep: defaultRetentionKeep, Every: defaultRetentionEvery}
	for _, field := range []struct {
		name string
		raw  string
		into *time.Duration
		zero string
	}{
		{"retention.keep", r.Keep, &out.Keep,
			"use keep = \"0s\" to keep everything forever"},
		{"retention.every", r.Every, &out.Every, ""},
	} {
		if field.raw == "" {
			continue
		}
		parsed, err := time.ParseDuration(field.raw)
		if err != nil {
			return Retention{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s %q: %v", source, field.name, field.raw, err)
		}
		if parsed < 0 {
			return Retention{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s must not be negative, got %s", source, field.name, parsed)
		}
		// Zero is meaningful for keep -- forget nothing -- and meaningless for
		// every, where it would ask for a pass on every beat of a rhythm that
		// deletes things.
		if parsed == 0 && field.zero == "" {
			return Retention{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s must be above 0, got %s", source, field.name, parsed)
		}
		*field.into = parsed
	}
	return out, nil
}

func (b fileBackup) build(source string) (Backup, error) {
	out := Backup{
		Dir:     platform.BackupDir(),
		Enabled: true,
		Every:   defaultBackupEvery,
		Keep:    defaultBackupKeep,
	}
	dir, err := settingsPath(source, "backup.dir", b.Dir)
	if err != nil {
		return Backup{}, err
	}
	if dir != "" {
		out.Dir = dir
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
	if strings.TrimSpace(c.Source) != "" {
		out.Source = strings.TrimSpace(c.Source)
	}
	if strings.TrimSpace(c.TerminalBinary) != "" {
		out.TerminalBinary = strings.TrimSpace(c.TerminalBinary)
	}
	if strings.TrimSpace(c.AppBinary) != "" {
		out.AppBinary = strings.TrimSpace(c.AppBinary)
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

func (c fileCodexAdapter) build(source string, out CodexAdapter) (CodexAdapter, error) {
	if strings.TrimSpace(c.Binary) != "" {
		out.Binary = strings.TrimSpace(c.Binary)
	}
	if strings.TrimSpace(c.Source) != "" {
		out.Source = strings.TrimSpace(c.Source)
	}
	if strings.TrimSpace(c.TerminalBinary) != "" {
		out.TerminalBinary = strings.TrimSpace(c.TerminalBinary)
	}
	if strings.TrimSpace(c.AppBinary) != "" {
		out.AppBinary = strings.TrimSpace(c.AppBinary)
	}
	if c.Implementations != nil {
		out.Implementations = *c.Implementations
	}
	if c.Timeout != "" {
		timeout, err := time.ParseDuration(c.Timeout)
		if err != nil {
			return CodexAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.codex.timeout %q: %v", source, c.Timeout, err)
		}
		if timeout <= 0 {
			return CodexAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.codex.timeout must be above 0, got %s", source, timeout)
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
		process, err := s.Process.build(source, "orchestrator.serena.process")
		if err != nil {
			return SerenaAdapter{}, err
		}
		out.Process = &process
	}
	return out, nil
}

// build is additive over the passed-in defaults, never zeroing: an omitted
// key keeps whatever the caller already put in out (the compiled-in
// defaults), the same shape every other adapter's build follows -- with one
// exception. A file that leaves the whole orchestrator.kivgraph table
// untouched is left exactly as out already was, kivgraph.DefaultEndpoint and
// all: the same "assume the daemon is already there" fallback Serena's own
// compiled endpoint gives it. The moment any field in this table IS written,
// though, the operator has started answering "where is this server" for
// themselves, and endpoint and process are the two answers TOML can give:
// naming both, or naming neither, is refused here instead of discovered
// later as a provider with no door in and no door out.
func (l fileKivgraphAdapter) build(source string, out KivgraphAdapter) (KivgraphAdapter, error) {
	if l == (fileKivgraphAdapter{}) {
		return out, nil
	}
	hasEndpoint := strings.TrimSpace(l.Endpoint) != ""
	hasProcess := l.Process != nil
	hasToken := strings.TrimSpace(l.Token) != ""
	switch {
	case hasEndpoint && hasProcess:
		return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.kivgraph has both endpoint and process; a server is reached one way", source)
	case !hasEndpoint && !hasProcess && !hasToken:
		return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.kivgraph needs an endpoint or a process", source)
	}
	if hasEndpoint {
		validated, err := validateAbsoluteURL(source, "orchestrator.kivgraph", "kivgraph", "endpoint", l.Endpoint)
		if err != nil {
			return KivgraphAdapter{}, err
		}
		out.Endpoint = validated
	}
	if hasToken {
		if !hasEndpoint {
			return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.kivgraph.token is set without endpoint; a bearer token for a stdio child has no server to authenticate against", source)
		}
		out.Token = strings.TrimSpace(l.Token)
	}
	if l.Implementations != nil {
		out.Implementations = *l.Implementations
	}
	if l.Timeout != "" {
		timeout, err := time.ParseDuration(l.Timeout)
		if err != nil {
			return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.kivgraph.timeout %q: %v", source, l.Timeout, err)
		}
		if timeout <= 0 {
			return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.kivgraph.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	if strings.TrimSpace(l.Dashboard) != "" {
		validated, err := validateDashboardURL(source, "orchestrator.kivgraph", "kivgraph", l.Dashboard)
		if err != nil {
			return KivgraphAdapter{}, err
		}
		out.Dashboard = validated
	}
	if l.DashboardProcess != nil {
		process, err := l.DashboardProcess.build(source, "orchestrator.kivgraph.dashboard_process")
		if err != nil {
			return KivgraphAdapter{}, err
		}
		if process.Port == 0 {
			return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.kivgraph.dashboard_process.port must be fixed when a dashboard URL is configured", source)
		}
		if out.Dashboard == "" {
			return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.kivgraph.dashboard is required with dashboard_process", source)
		}
		parsed, _ := url.Parse(out.Dashboard)
		if parsed.Port() == "" || parsed.Port() != strconv.Itoa(process.Port) {
			return KivgraphAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.kivgraph.dashboard port must match dashboard_process.port %d", source, process.Port)
		}
		out.DashboardProcess = &process
	}
	if l.Process != nil {
		process, err := l.Process.build(source, "orchestrator.kivgraph.process")
		if err != nil {
			return KivgraphAdapter{}, err
		}
		out.Process = &process
		// The compiled kivgraph.DefaultEndpoint fallback out arrived with is
		// exactly the "nothing was said" default this operator just said
		// something about: declaring process is a deliberate choice of the
		// stdio child over it, and leaving the fallback in place would leave
		// the built adapter naming both, contradicting the refusal above.
		out.Endpoint = ""
	}
	return out, nil
}

// settingsPath is the one rule every path a settings file writes has to pass,
// and it is orchestrator.tokensave.root's rule generalized: a relative path is
// refused here rather than resolved, because "resolved against what" has no
// honest answer. The process that loads this file is not always the process
// that uses the path -- in service mode it is the daemon, whose working
// directory is whatever the unit gave it -- so `path = "base.duckdb"` names one
// file from a shell and a different one from systemd, and the operator finds
// out by opening a measurement base with nothing in it.
//
// A leading `~` fails the same test, which is deliberate. Nothing in this
// package expands it -- os.UserHomeDir is called only from internal/platform --
// so accepting it would silently create a directory whose name is the single
// character `~`, in whatever directory the process happened to be standing in.
//
// An empty value is not a path: it means the key was omitted, and every caller
// already reads that as "keep the default".
func settingsPath(source, key, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if !filepath.IsAbs(trimmed) {
		hint := ""
		if strings.HasPrefix(trimmed, "~") {
			hint = "; ~ is not expanded, write the home directory out"
		}
		return "", contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s %q must be absolute%s", source, key, trimmed, hint)
	}
	return filepath.Clean(trimmed), nil
}

// build is additive over the passed-in defaults, like every neighbor. The one
// extra check is Root, which must be absolute for the reason settingsPath
// spells out.
func (d fileDesktopAdapter) build(source string, out DesktopAdapter) (DesktopAdapter, error) {
	if d.Implementations != nil {
		out.Implementations = *d.Implementations
	}
	if d.Timeout != "" {
		timeout, err := time.ParseDuration(d.Timeout)
		if err != nil {
			return DesktopAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.desktop.timeout %q: %v", source, d.Timeout, err)
		}
		if timeout <= 0 {
			return DesktopAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.desktop.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	if d.Process != nil {
		process, err := d.Process.build(source, "orchestrator.desktop.process")
		if err != nil {
			return DesktopAdapter{}, err
		}
		out.Process = &process
	}
	return out, nil
}

func (s fileScraplingAdapter) build(source string, out ScraplingAdapter) (ScraplingAdapter, error) {
	if s.Implementations != nil {
		out.Implementations = *s.Implementations
	}
	if s.Timeout != "" {
		timeout, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return ScraplingAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.scrapling.timeout %q: %v", source, s.Timeout, err)
		}
		if timeout <= 0 {
			return ScraplingAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.scrapling.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	if s.Process != nil {
		process, err := s.Process.build(source, "orchestrator.scrapling.process")
		if err != nil {
			return ScraplingAdapter{}, err
		}
		out.Process = &process
	}
	return out, nil
}

func (t fileTokensaveAdapter) build(source string, out TokensaveAdapter) (TokensaveAdapter, error) {
	root, err := settingsPath(source, "orchestrator.tokensave.root", t.Root)
	if err != nil {
		return TokensaveAdapter{}, err
	}
	if root != "" {
		out.Root = root
	}
	if t.Implementations != nil {
		out.Implementations = *t.Implementations
	}
	if t.Timeout != "" {
		timeout, err := time.ParseDuration(t.Timeout)
		if err != nil {
			return TokensaveAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.tokensave.timeout %q: %v", source, t.Timeout, err)
		}
		if timeout <= 0 {
			return TokensaveAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.tokensave.timeout must be above 0, got %s", source, timeout)
		}
		out.Timeout = timeout
	}
	if t.Process != nil {
		process, err := t.Process.build(source, "orchestrator.tokensave.process")
		if err != nil {
			return TokensaveAdapter{}, err
		}
		out.Process = &process
	}
	return out, nil
}

// build turns the file shape of a process table into a ManagedProcess.
// section is the settings key path this table lives at (e.g.
// "orchestrator.serena.process"), used to name it in every error below --
// this one function backs every supervised-process table in the file, and a
// serena-shaped error naming kivgraph's own mistake would send the reader
// to the wrong block.
func (p fileManagedProcess) build(source, section string) (ManagedProcess, error) {
	if strings.TrimSpace(p.Command) == "" {
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s.command is required once the process table is present", source, section)
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
			"settings %s: %s.lifecycle must be %q or %q, got %q",
			source, section, supervisor.Persistent, supervisor.OnDemand, p.Lifecycle)
	}
	// idle_timeout describes the idle reaper, and the reaper skips persistent
	// servers by definition, so the two keys together are a contradiction
	// rather than a redundancy. Every other knob here that stops applying has
	// something beside it saying why -- an `enabled` set to false, a limit set
	// to zero, a status line that reads "off". This one would have nothing to
	// read anywhere, which is the only reason it is refused and they are not.
	if out.Lifecycle == supervisor.Persistent && strings.TrimSpace(p.IdleTimeout) != "" {
		return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s.idle_timeout has no meaning for a %q server: only %q servers are stopped when idle",
			source, section, supervisor.Persistent, supervisor.OnDemand)
	}
	if p.Port != nil {
		if *p.Port < 0 || *p.Port > 65535 {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s.port must be between 0 and 65535, got %d", source, section, *p.Port)
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
			"settings %s: %s.instance must be %q or %q, got %q",
			source, section, InstanceShared, InstancePerRepository, p.Instance)
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
			"settings %s: %s.instance = %q needs %s in args: "+
				"without it every repository would start the same server",
			source, section, InstancePerRepository, ProjectPlaceholder)
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
			"settings %s: %s.port cannot be fixed with instance = %q: "+
				"each repository needs its own port, so leave it unset and Atenea picks them",
			source, section, InstancePerRepository)
	}
	if p.RestartLimit != nil {
		if *p.RestartLimit < 0 {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s.restart_limit must not be negative, got %d", source, section, *p.RestartLimit)
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
				"settings %s: %s.%s %q: %v", source, section, d.key, d.raw, err)
		}
		if dur <= 0 {
			return ManagedProcess{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s.%s must be above 0, got %s", source, section, d.key, dur)
		}
		*d.field = dur
	}
	return out, nil
}

// build reads [desktop] with each list independent of the other, so allowing
// an application does not silently clear the refusals and vice versa. Absent
// takes the shipped value; present-but-empty is a statement and is honored --
// an explicitly empty Denied is somebody saying they know what they are doing.
func (d fileDesktop) build() Desktop {
	out := DefaultDesktop()
	if d.Applications != nil {
		out.Applications = *d.Applications
	}
	if d.Denied != nil {
		out.Denied = *d.Denied
	}
	return out
}

// build reads [web] with each list independent of the other, the same way
// [desktop] is read. Absent takes the shipped value; present-but-empty is a
// statement and is honored -- an explicitly empty Denied is somebody asking
// for a fetcher with no destination gate and saying so.
func (w fileWeb) build() Web {
	out := DefaultWeb()
	if w.Domains != nil {
		out.Domains = *w.Domains
	}
	if w.Denied != nil {
		out.Denied = *w.Denied
	}
	return out
}

func (s fileSecurity) build() Security {
	if s.Sensitive == nil {
		return Security{Sensitive: defaultSensitive}
	}
	return Security{Sensitive: *s.Sensitive}
}

// build reads [local_agents] ON TOP OF the defaults, never instead of them.
// A file with no block, which is every file written before this shipped,
// comes back capped rather than uncapped.
func (l fileLocalAgents) build(source string) (LocalAgents, error) {
	out := DefaultLocalAgents()
	fail := func(format string, args ...any) (LocalAgents, error) {
		return LocalAgents{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: local_agents: %s", source, fmt.Sprintf(format, args...))
	}
	if l.Effects != nil {
		effects := make([]contract.Effect, 0, len(*l.Effects))
		for _, name := range *l.Effects {
			effect, err := contract.ParseEffect(name)
			if err != nil {
				return fail("%v", err)
			}
			effects = append(effects, effect)
		}
		out.Effects = effects
	}
	if l.Context != nil {
		levels := make([]contract.ContextLevel, 0, len(*l.Context))
		for _, name := range *l.Context {
			level, err := contract.ParseContextLevel(name)
			if err != nil {
				return fail("%v", err)
			}
			levels = append(levels, level)
		}
		out.Context = levels
	}
	if l.MaxDuration != nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(*l.MaxDuration))
		if err != nil {
			return fail("max_duration %q: %v", *l.MaxDuration, err)
		}
		if parsed <= 0 {
			return fail("max_duration must be positive, got %v", parsed)
		}
		out.Limits.MaxDuration = parsed
	}
	if l.MaxTokens != nil {
		// Zero is a declaration of no token limit, not a mistake -- the value
		// is advisory and nothing enforces it. See contract.Limits.MaxTokens.
		if *l.MaxTokens < 0 {
			return fail("max_tokens cannot be negative, got %d", *l.MaxTokens)
		}
		out.Limits.MaxTokens = *l.MaxTokens
	}
	return out, nil
}

func (c fileCore) build(source string) (Core, error) {
	out := Core{ShutdownGrace: defaultShutdownGrace, HealthProbeEvery: 15 * time.Minute}
	if c.ShutdownGrace != "" {
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
	}
	if c.HealthProbeEvery == "" {
		return out, nil
	}
	healthEvery, err := time.ParseDuration(c.HealthProbeEvery)
	if err != nil {
		return Core{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: core.health_probe_every %q: %v", source, c.HealthProbeEvery, err)
	}
	if healthEvery < 0 {
		return Core{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: core.health_probe_every must not be negative", source)
	}
	out.HealthProbeEvery = healthEvery
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
	defaultModelTimeout = 180 * time.Second
	defaultModelBackend = "claude"
)

func (m fileModel) build(source string) (Model, error) {
	out := Model{Backend: defaultModelBackend, Binary: defaultModelBinary, Timeout: defaultModelTimeout}
	if strings.TrimSpace(m.Backend) != "" {
		out.Backend = strings.ToLower(strings.TrimSpace(m.Backend))
	}
	if out.Backend != "claude" && out.Backend != "opencode" {
		return Model{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: model.backend %q must be claude or opencode", source, m.Backend)
	}
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
	exploreFallbacks, err := modelFallbacks(source, "explore", out.Explore, m.ExploreFallbacks)
	if err != nil {
		return Model{}, err
	}
	out.ExploreFallbacks = exploreFallbacks
	out.PlanFallbacks, err = modelFallbacks(source, "plan", out.Plan, m.PlanFallbacks)
	if err != nil {
		return Model{}, err
	}
	return out, nil
}

func modelFallbacks(source, role, primary string, raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, value := range raw {
		name := strings.TrimSpace(value)
		if name == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"settings %s: model.%s_fallbacks contains an empty model", source, role)
		}
		if name == primary {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"settings %s: model.%s_fallbacks repeats the primary model %q", source, role, name)
		}
		if seen[name] {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"settings %s: model.%s_fallbacks repeats %q", source, role, name)
		}
		seen[name] = true
		out = append(out, name)
	}
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
	if err := buildSubject(c, &out); err != nil {
		return fail("%v", err)
	}
	if err := out.Validate(); err != nil {
		return contract.Capability{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	return out, nil
}

// buildSubject reads the subject declaration, and refuses every half of it.
//
// A subject is a grouping key for measurements, so getting it wrong does not
// fail loudly at call time: it files health and cost under something that
// means nothing, and the funnel goes on ranking as if that were fine. There is
// no run to inspect afterwards and no error anybody would notice. So the whole
// declaration is checked at the door, where a mistake is still cheap:
//
//   - a `subject_from` naming an input the capability does not declare would
//     always read empty, which looks exactly like a capability that declared
//     no subject at all;
//   - a `subject_from` on a non-string input cannot be read as one, and
//     url_host is the only kind there is;
//   - either key without the other is somebody who meant to finish and did
//     not, and guessing which half they meant is worse than asking.
func buildSubject(c fileCapability, out *contract.Capability) error {
	from, kind := strings.TrimSpace(c.SubjectFrom), strings.TrimSpace(c.SubjectKind)
	if from == "" && kind == "" {
		return nil
	}
	if from == "" || kind == "" {
		return fmt.Errorf(
			"capability %s declares only half a subject: subject_from and subject_kind go together", c.ID)
	}
	parsed, err := contract.ParseSubjectKind(kind)
	if err != nil {
		return fmt.Errorf("capability %s: %w", c.ID, err)
	}
	if parsed == contract.SubjectNone {
		return fmt.Errorf(
			"capability %s: subject_kind is empty, which is how a capability says it has no subject -- "+
				"drop subject_from too", c.ID)
	}
	index := slices.IndexFunc(out.Inputs, func(f contract.Field) bool { return f.Name == from })
	if index < 0 {
		return fmt.Errorf(
			"capability %s: subject_from names %q, which is not one of its inputs", c.ID, from)
	}
	if field := out.Inputs[index]; field.Type != contract.TypeString {
		return fmt.Errorf(
			"capability %s: subject_from names %q, which is a %s -- a subject is read from a string",
			c.ID, from, field.Type)
	}
	out.SubjectFrom, out.SubjectKind = from, parsed
	return nil
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
