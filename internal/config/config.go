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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/adapter/android"
	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
	"github.com/Tutitoos/atenea/internal/adapter/codex"
	"github.com/Tutitoos/atenea/internal/adapter/desktop"
	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/adapter/tokensave"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/mcpprobe"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/resultcache"
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
	DesktopProfiles []DesktopProfile
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
	Metrics      Metrics
	// ResultCache is the bounded local cache for read-only code.context results.
	// Its limits are intentionally explicit and validated at load time.
	ResultCache resultcache.Config
	Backup      Backup
	// Retention is how long receipts and traces are kept. See the type.
	Retention Retention
	Security  Security
	// Desktop is the allow-list for the desktop capabilities. See the type.
	Desktop Desktop
	// Android is the explicit device allow-list for the Android bridge.
	Android         Android
	Selector        selector.Config
	Capabilities    []contract.Capability
	Implementations []contract.Implementation
	Repositories    []contract.Repository
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
	// WorkingDirectory fixes the cwd of a raw stdio child. Empty preserves the
	// inherited cwd. It cannot be selected or changed by a tool request.
	WorkingDirectory string
	// Timeout bounds the readiness check. Zero takes the probe's default.
	Timeout time.Duration
	// ProtocolMode selects the exact MCP transport policy: legacy (default),
	// auto, or modern-pin. It is kept as text at this configuration boundary
	// so unknown values can be rejected before a backend is constructed.
	ProtocolMode string
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
	Effects      []contract.Effect
	ToolEffects  map[string][]contract.Effect
	ToolTimeouts map[string]time.Duration
	ToolRules    map[string][]EffectRule
}

// EffectRule narrows a tool's conservative fallback when its arguments make
// the actual operation unambiguous.
type EffectRule struct {
	When    map[string]string
	Effects []contract.Effect
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

// ToolTimeoutOf returns the explicit timeout for a tool, or zero when the
// profile/backend timeout should be used.
func (m MCPServer) ToolTimeoutOf(tool string) time.Duration {
	return m.ToolTimeouts[tool]
}

// EffectsFor applies the first matching argument rule, falling back to the
// tool or server declaration when no rule matches.
func (m MCPServer) EffectsFor(tool string, args map[string]any) []contract.Effect {
	for _, rule := range m.ToolRules[tool] {
		matches := true
		for key, wanted := range rule.When {
			if fmt.Sprint(args[key]) != wanted {
				matches = false
				break
			}
		}
		if matches {
			return rule.Effects
		}
	}
	return m.EffectsOf(tool)
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
		ID:               m.ID,
		URL:              m.URL,
		Command:          m.Command,
		Env:              m.Env,
		WorkingDirectory: m.WorkingDirectory,
		Timeout:          m.Timeout,
		ProtocolMode:     mcpprobe.ProtocolMode(m.ProtocolMode),
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
	// ExposeOff is part of ATENEA's public orchestration contract.
	ExposeOff Expose = "off"
	// ExposeRaw is a passthrough: the tools are re-offered verbatim under
	// the reserved raw. namespace, with no funnel and no capability.
	// ExposeRaw is part of ATENEA's public orchestration contract.
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
// It covers the run receipts on disk. The measurement base is not
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
	// ClientDeniedCapabilities is the MCP kill switch for individual
	// capabilities. Effects describe what a call may cause; this list is the
	// narrower surface connected clients may see at all. It is separate so a
	// read-only desktop capability can remain available without exposing a
	// pointer-moving capability that happens to share `device`.
	ClientDeniedCapabilities []string
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
	Tokensave  TokensaveAdapter
	Desktop    DesktopAdapter
	Android    AndroidAdapter
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
// It separates machine-wide state from session- or repository-scoped state.
// Each adapter validates which instance policies it can actually support.
//
// The set is deliberately small and closed. Unknown values are refused rather
// than read as the default, for the reason every other refusal in this file
// exists -- a policy an operator believes is in force and is not is worse than
// one they can see is missing.
type Instance string

const (
	// InstanceShared is one process for the whole machine, and the default:
	// it is what every managed server did before this existed.
	// InstanceShared is part of ATENEA's public orchestration contract.
	InstanceShared Instance = "shared"
	// InstancePerChat is one upstream MCP session per client connection. It is
	// useful for servers whose session state belongs to a single conversation,
	// and is closed with that connection rather than retained by the service.
	// InstancePerChat is part of ATENEA's public orchestration contract.
	InstancePerChat Instance = "per_chat"
	// InstancePerRepository is one process per declared repository, each
	// pinned to that repository and started only when something asks for it.
	// InstancePerRepository is part of ATENEA's public orchestration contract.
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

// TokensaveAdapter configures the tokensave adapter.
//
// A stdio MCP server Atenea spawns and supervises. tokensave serves ONE
// project rooted at a directory and speaks paths relative to it, so the
// adapter cannot translate a repository-relative path in either direction
// without being told where that root is. It is declared rather than derived:
// a root guessed from the repository list would be silently wrong the day a
// repository is added outside it.
type TokensaveAdapter struct {
	// Root is the directory the supervised server was pointed at, absolute.
	// Every repository this adapter answers for lives under it.
	Root string
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call. Tokensave re-syncs its index before answering,
	// which is slow long before it is stuck.
	Timeout time.Duration
	// Process launches and supervises the tokensave server itself. It is not
	// optional: a stdio server has no address to dial.
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
	// reason as tokensave: a stdio server has no address to dial.
	Process *ManagedProcess
}

// AndroidAdapter drives explicitly allowed Android devices through fixed ADB,
// UIAutomator and scrcpy commands. It is a direct process adapter: unlike the
// macOS helper, none of its operations depend on TCC or a graphical ancestor.
type AndroidAdapter struct {
	Implementations []string
	ADBBinary       string
	ScrcpyBinary    string
	HelperMode      string
	Timeout         time.Duration
	FrameTTL        time.Duration
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
	//
	// The single entry "*" means every application Denied does not name. It is
	// a token that has to be TYPED, and that is the whole design: the rule
	// above is that omission is not a statement, so the widest possible
	// allow-list must not be reachable by leaving something blank. Mixing it
	// with named bundle ids is refused at load rather than resolved, because a
	// list reading "everything, and also these two" is an operator who does not
	// know which of the two sentences is in force.
	Applications []string
	// ActionApplications is the independent allow-list for mutations. When
	// ActionApplicationsInherited is true it is an effective copy of
	// Applications, preserving the pre-2.0 configuration behavior. An
	// explicitly empty list is therefore distinguishable and denies all acts.
	ActionApplications          []string
	ActionApplicationsInherited bool
	// Denied always wins, and is seeded rather than empty. The defaults are
	// the applications where a single screenshot is a credential: password
	// managers, the keychain, banking. An operator who deletes the block gets
	// them back; one who writes an explicit empty list has said something.
	//
	// It outranks the wildcard too, which is what keeps "*" survivable: the
	// widest allow-list still cannot reach a password manager.
	Denied []string
	// LookThenAct lets one chat act on the screen it has already read.
	//
	// False shipped, and the default is the security control rather than a
	// cautious guess. desktop.inspect and desktop.screenshot return whatever a
	// window chose to display, written by whoever controls that window; with
	// this off, a chat handed that content may no longer cause a device effect
	// that changes anything, so a sentence inside somebody else's email cannot
	// reach the pointer. See internal/core/tainted.go for the whole argument.
	//
	// Turning it on is what an operator does to get the continuous drive-the-
	// desktop loop -- look, click, look again -- and it removes the only
	// control Atenea has against prompt injection reaching input, because
	// Atenea runs no classifier over what it captured. What survives it is
	// Denied above, the hard refusal to type into a secure field, credential
	// redaction in desktop.type, and the audit receipts.
	LookThenAct bool
	// VisualFeedback controls the native macOS overlay, cursor and live
	// miniature. It is enabled by default so a person can see where an action
	// is being sent; disabling it never disables the window-safety checks.
	VisualFeedback bool
}

// Android is the device boundary for the Android computer-use bridge. Empty
// denies every serial; wildcard access is deliberately unsupported.
type Android struct {
	AllowedSerials []string
}

// AllApplications is the token that widens the desktop allow-list to every
// application Denied does not name. Defined by the adapter that enforces it so
// there is one spelling rather than two that must be kept in step.
const AllApplications = desktop.AllApplications

// DefaultDesktop is the shipped posture: nothing allowed, and the obvious
// hazards refused even if somebody allows them later.
func DefaultDesktop() Desktop {
	return Desktop{
		Applications:                nil,
		ActionApplications:          nil,
		ActionApplicationsInherited: true,
		VisualFeedback:              true,
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

// Security is the one place delicate files are declared.
type Security struct {
	// Sensitive holds the path patterns that carry secrets. Reading is free by
	// default; these are the exception, and while exploring they are skipped in
	// silence rather than turned into a question.
	Sensitive []string
}

// RunnerOMP, RunnerClaudeCode, RunnerCodex,
// RunnerTokensave, RunnerDesktop, RunnerAndroid and RunnerLocal are the
// values orchestrator.runners accepts.
const (
	// RunnerOMP is part of ATENEA's public orchestration contract.
	RunnerOMP = "omp"
	// RunnerClaudeCode is part of ATENEA's public orchestration contract.
	RunnerClaudeCode = "claudecode"
	// RunnerCodex is part of ATENEA's public orchestration contract.
	RunnerCodex = "codex"
	// RunnerTokensave is part of ATENEA's public orchestration contract.
	RunnerTokensave = "tokensave"
	// RunnerDesktop is part of ATENEA's public orchestration contract.
	RunnerDesktop = "desktop"
	// RunnerAndroid is part of ATENEA's public orchestration contract.
	RunnerAndroid = "android"
	// RunnerLocal is part of ATENEA's public orchestration contract.
	RunnerLocal = "local"
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
	Contract        string               `toml:"contract"`
	Core            fileCore             `toml:"core"`
	Orchestrator    fileOrchestrator     `toml:"orchestrator"`
	Metrics         fileMetrics          `toml:"metrics"`
	ResultCache     fileResultCache      `toml:"result_cache"`
	Retention       fileRetention        `toml:"retention"`
	Backup          fileBackup           `toml:"backup"`
	Security        fileSecurity         `toml:"security"`
	Desktop         fileDesktop          `toml:"desktop"`
	Android         fileAndroid          `toml:"android"`
	Selector        fileSelector         `toml:"selector"`
	Capabilities    []fileCapability     `toml:"capability"`
	Implementations []fileImpl           `toml:"implementation"`
	Repositories    []fileRepository     `toml:"repository"`
	MCPServers      []fileMCPServer      `toml:"mcp_server"`
	DesktopProfiles []fileDesktopProfile `toml:"desktop_profile"`
}

type fileResultCache struct {
	MaxEntries *int   `toml:"max_entries"`
	MaxBytes   *int64 `toml:"max_bytes"`
	TTL        string `toml:"ttl"`
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
	// An omitted key keeps the safe built-in MCP posture. An explicit empty
	// list is the deliberate phase-two answer that exposes every capability.
	ClientDeniedCapabilities *[]string `toml:"client_denied_capabilities"`
	Checkpoints              *bool     `toml:"checkpoints"`
	CheckpointDir            string    `toml:"checkpoint_dir"`
	// Runners uses a pointer so an omitted list and an explicitly empty one
	// are different things: leaving the block out keeps the shipped adapter,
	// while writing an empty list is how a user says "dispatch nowhere".
	Runners    *[]string             `toml:"runners"`
	Local      fileLocalRunner       `toml:"local"`
	OMP        fileOMPAdapter        `toml:"omp"`
	ClaudeCode fileClaudeCodeAdapter `toml:"claudecode"`
	Codex      fileCodexAdapter      `toml:"codex"`
	Tokensave  fileTokensaveAdapter  `toml:"tokensave"`
	Desktop    fileDesktopAdapter    `toml:"desktop"`
	Android    fileAndroidAdapter    `toml:"android"`
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

// fileTokensaveAdapter is the TOML shape of TokensaveAdapter. `root` is the
// one key its neighbor has no counterpart for: see TokensaveAdapter.Root.
type fileDesktopAdapter struct {
	Implementations *[]string           `toml:"implementations"`
	Timeout         string              `toml:"timeout"`
	Process         *fileManagedProcess `toml:"process"`
}

type fileAndroidAdapter struct {
	Implementations *[]string `toml:"implementations"`
	ADBBinary       string    `toml:"adb_binary"`
	ScrcpyBinary    string    `toml:"scrcpy_binary"`
	HelperMode      string    `toml:"helper_mode"`
	Timeout         string    `toml:"timeout"`
	FrameTTL        string    `toml:"frame_ttl"`
}

type fileTokensaveAdapter struct {
	Root            string              `toml:"root"`
	Implementations *[]string           `toml:"implementations"`
	Timeout         string              `toml:"timeout"`
	Process         *fileManagedProcess `toml:"process"`
}

// fileManagedProcess uses a pointer on the field above so an omitted
// [orchestrator.tokensave.process] table and a present-but-empty one are
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
	Applications       *[]string `toml:"applications"`
	ActionApplications *[]string `toml:"action_applications"`
	Denied             *[]string `toml:"denied"`
	LookThenAct        *bool     `toml:"look_then_act"`
	VisualFeedback     *bool     `toml:"visual_feedback"`
}

type fileAndroid struct {
	AllowedSerials *[]string `toml:"allowed_serials"`
}

type fileSelector struct {
	Rules                 []fileRule `toml:"rule"`
	HealthStaleAfter      string     `toml:"health_stale_after"`
	QualityMinimumSamples *int       `toml:"quality_minimum_samples"`
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
	Languages   []string `toml:"languages"`
	RequiresVCS bool     `toml:"requires_vcs"`
	MinScale    string   `toml:"min_scale"`
	MaxScale    string   `toml:"max_scale"`
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
}

type fileMCPServer struct {
	ID               string            `toml:"id"`
	URL              string            `toml:"url"`
	Command          []string          `toml:"command"`
	Env              map[string]string `toml:"env"`
	WorkingDirectory string            `toml:"working_directory"`
	// Dashboard is only read to refuse it: the key was removed in contract 5.0.0.
	Dashboard    *string       `toml:"dashboard"`
	Timeout      string        `toml:"timeout"`
	ProtocolMode string        `toml:"protocol_mode"`
	Expose       string        `toml:"expose"`
	Instance     string        `toml:"instance"`
	Tools        []string      `toml:"tools"`
	Effects      []string      `toml:"effects"`
	Tool         []fileMCPTool `toml:"tool"`
}

type fileMCPTool struct {
	Name    string            `toml:"name"`
	Effects []string          `toml:"effects"`
	Timeout string            `toml:"timeout"`
	Rule    []fileMCPToolRule `toml:"rule"`
}

type fileMCPToolRule struct {
	When    map[string]string `toml:"when"`
	Effects []string          `toml:"effects"`
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
	protocolMode := strings.TrimSpace(m.ProtocolMode)
	if protocolMode == "" {
		protocolMode = "legacy"
	}
	if protocolMode != "legacy" && protocolMode != "auto" && protocolMode != "modern-pin" {
		return fail("mcp_server %s: protocol_mode %q is not legacy, auto, or modern-pin", id, protocolMode)
	}
	out := MCPServer{ID: id, Command: m.Command, Env: m.Env, ProtocolMode: protocolMode}
	if m.WorkingDirectory != "" {
		if !hasCommand || !filepath.IsAbs(m.WorkingDirectory) || filepath.Clean(m.WorkingDirectory) != m.WorkingDirectory {
			return fail("mcp_server %s: working_directory requires a canonical absolute path and a stdio command", id)
		}
		out.WorkingDirectory = m.WorkingDirectory
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
	if out.WorkingDirectory != "" && out.Expose != ExposeRaw {
		return fail("mcp_server %s: working_directory is supported only for expose = raw", id)
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
		if strings.TrimSpace(tool.Timeout) != "" {
			d, err := time.ParseDuration(strings.TrimSpace(tool.Timeout))
			if err != nil || d <= 0 {
				if err == nil {
					err = fmt.Errorf("must be positive")
				}
				return fail("mcp_server %s: tool %s: timeout %q: %v", id, name, tool.Timeout, err)
			}
			if out.ToolTimeouts == nil {
				out.ToolTimeouts = make(map[string]time.Duration, len(m.Tool))
			}
			out.ToolTimeouts[name] = d
		}
		for _, rawRule := range tool.Rule {
			if len(rawRule.When) == 0 || len(rawRule.Effects) == 0 {
				return fail("mcp_server %s: tool %s: each rule needs when and effects", id, name)
			}
			ruleEffects, err := namedEffects(rawRule.Effects)
			if err != nil {
				return fail("mcp_server %s: tool %s rule: %v", id, name, err)
			}
			if out.ToolRules == nil {
				out.ToolRules = make(map[string][]EffectRule, len(m.Tool))
			}
			out.ToolRules[name] = append(out.ToolRules[name], EffectRule{
				When: maps.Clone(rawRule.When), Effects: ruleEffects,
			})
		}
	}
	return out, nil
}

// validateAbsoluteURL checks that raw is a URL a plain http(s) client could
// dial: some scheme, some host, nothing fancier. This is the shape the far
// side of an MCP connection itself takes -- mcp_server's own url.
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
	// A table the 5.0.0 cutover removed is named with its remedy instead of
	// being reported as a typo: the file was right when it was written.
	var gone []string
	named := map[string]bool{}
	for _, key := range meta.Undecoded() {
		top := first(key.String())
		if msg, ok := retiredSections[top]; ok && !named[top] {
			named[top] = true
			gone = append(gone, msg)
		} else if msg := retiredKeyMessage(key.String()); msg != "" && !named[msg] {
			named[msg] = true
			gone = append(gone, msg)
		}
	}
	gone = append(gone, retiredCatalog(decoded)...)
	if len(gone) > 0 {
		sort.Strings(gone)
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %s", source, strings.Join(gone, "; "))
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
	if cfg.Metrics, err = decoded.Metrics.build(source); err != nil {
		return Config{}, err
	}
	cfg.ResultCache = resultcache.DefaultConfig()
	if decoded.ResultCache.MaxEntries != nil {
		cfg.ResultCache.MaxEntries = *decoded.ResultCache.MaxEntries
	}
	if decoded.ResultCache.MaxBytes != nil {
		cfg.ResultCache.MaxBytes = *decoded.ResultCache.MaxBytes
	}
	if raw := strings.TrimSpace(decoded.ResultCache.TTL); raw != "" {
		ttl, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: result_cache.ttl must be a valid duration", source)
		}
		cfg.ResultCache.TTL = ttl
	}
	if err := cfg.ResultCache.Validate(); err != nil {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: result_cache: %v", source, err)
	}
	if cfg.Retention, err = decoded.Retention.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Backup, err = decoded.Backup.build(source); err != nil {
		return Config{}, err
	}
	cfg.Security = decoded.Security.build()
	if cfg.Desktop, err = decoded.Desktop.build(source); err != nil {
		return Config{}, err
	}
	if cfg.Android, err = decoded.Android.build(source); err != nil {
		return Config{}, err
	}
	for _, rule := range decoded.Selector.Rules {
		cfg.Selector.Rules = append(cfg.Selector.Rules, selector.Rule{
			Capability: rule.Capability,
			Repository: rule.Repository,
			Prefer:     rule.Prefer,
		})
	}
	cfg.Selector.HealthStaleAfter = 24 * time.Hour
	cfg.Selector.QualityMinimumSamples = 2
	if decoded.Selector.QualityMinimumSamples != nil {
		if *decoded.Selector.QualityMinimumSamples < 1 {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: selector.quality_minimum_samples must be positive", source)
		}
		cfg.Selector.QualityMinimumSamples = *decoded.Selector.QualityMinimumSamples
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
		if impl.Provider == "serena" || strings.HasPrefix(impl.ID, "serena.") {
			return Config{}, contract.Fail(contract.FailureInvalidInput, "settings %s: Serena is retired; remove implementation %s", source, impl.ID)
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
		if server.ID == "serena" {
			return Config{}, contract.Fail(contract.FailureInvalidInput, "settings %s: Serena is retired; remove its MCP declaration", source)
		}
		if seen[server.ID] {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: mcp_server %s is declared twice", source, server.ID)
		}
		seen[server.ID] = true
		cfg.MCPServers = append(cfg.MCPServers, server)
	}
	profiles, err := validateDesktopProfiles(defaultDesktopProfiles(), decoded.DesktopProfiles)
	if err != nil {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	if err := ValidateDesktopProfiles(profiles, cfg.MCPServers); err != nil {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	cfg.DesktopProfiles = profiles
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

// clientDeniedCapabilitiesDefault is the first-phase MCP surface. The list
// is capability-level rather than effect-level: desktop.move only causes
// read + device, so a client floor that allows observation would otherwise
// allow moving the pointer too.
var clientDeniedCapabilitiesDefault = []string{
	"desktop.open",
	"desktop.move",
	"desktop.drag",
	"desktop.scroll",
	"desktop.click",
	"desktop.type",
	"desktop.key",
	"android.tap",
	"android.swipe",
	"android.type",
	"android.key",
	"android.mirror",
	"android.unmirror",
}

// retiredSections are the tables contract 5.0.0 removed with the agents, the
// workflow engine, the decide command and the knowledge store. Each message
// names the section and the edit; none is ignored silently.
var retiredSections = map[string]string{
	"model":            retired("[model]", "the built-in agents", "delete the table"),
	"agent":            retired("[[agent]]", "the built-in agents", "delete every block"),
	"local_agents":     retired("[local_agents]", "the built-in agents", "delete the table"),
	"workflow":         retired("[workflow]", "the workflow engine", "delete the table"),
	"workflow_profile": retired("[[workflow_profile]]", "the workflow engine", "delete every block"),
	"decision":         retired("[decision]", "the decide command", "delete the table"),
	"knowledge":        retired("[knowledge]", "the knowledge store", "delete the table; knowledge.sqlite and traces.db stay on disk, unused"),
}

func retired(section, with, remedy string) string {
	return fmt.Sprintf("%s was removed in contract 5.0.0 with %s; %s (see docs/content/migration-5.md)", section, with, remedy)
}

// retiredKeys are keys and sub-tables, by dotted path prefix, that contract
// 5.0.0 removed from tables that still exist. parse names the edit instead of
// reporting them as typos.
var retiredKeys = map[string]string{
	"orchestrator.kivgraph":                     retired("[orchestrator.kivgraph]", "the Kivgraph provider", "delete the table and every sub-table"),
	"repository.indexed_by":                     retired("indexed_by", "the Kivgraph provider, the only provider that indexed repositories", "delete the key from every [[repository]] block"),
	"implementation.constraints.requires_index": retired("requires_index", "the Kivgraph provider, the only provider that needed an index", "delete the key from every [[implementation.constraints]] table"),
}

// retiredRunners are the orchestrator.runners names that no longer exist.
var retiredRunners = map[string]string{
	"kivgraph": retired(`runner "kivgraph"`, "the Kivgraph provider", "delete the entry from orchestrator.runners"),
}

// retiredCapabilities are the capability ids whose only implementations were
// Kivgraph's. They left the shipped catalog with it, so a settings file that
// still declares, implements or routes one is refused rather than left as a
// capability nothing can serve.
var retiredCapabilities = map[string]bool{
	"symbol.search": true, "symbol.intent_search": true, "symbol.definition": true,
	"symbol.references": true, "symbol.implementations": true, "symbol.dependencies": true,
	"symbol.consumers": true, "symbol.get": true, "symbol.unresolved": true,
	"symbol.source": true, "symbol.impact": true, "graph.status": true,
	"graph.repositories": true, "graph.ensure_fresh": true, "repository.index": true,
	"code.impact": true,
}

// retiredCatalog names, in one message per kind, everything a settings file
// still declares that left the shipped catalog with the Kivgraph provider: the
// runner name, the kivgraph.* implementation blocks and the capabilities only it
// served, wherever the file names them. It reads the decoded file rather than
// the built catalog so a file written for the old defaults is told about all of
// it at once instead of one refusal per edit.
func retiredCatalog(decoded file) []string {
	var out []string
	if decoded.Orchestrator.Runners != nil {
		for _, name := range *decoded.Orchestrator.Runners {
			if msg, gone := retiredRunners[name]; gone {
				out = append(out, msg)
			}
		}
	}
	var impls []string
	named := map[string]bool{}
	for _, impl := range decoded.Implementations {
		if impl.Provider == "kivgraph" || strings.HasPrefix(impl.ID, "kivgraph.") {
			impls = append(impls, impl.ID)
		}
		if retiredCapabilities[impl.Capability] {
			named[impl.Capability] = true
		}
	}
	for _, capability := range decoded.Capabilities {
		if retiredCapabilities[capability.ID] {
			named[capability.ID] = true
		}
	}
	for _, rule := range decoded.Selector.Rules {
		if retiredCapabilities[rule.Capability] {
			named[rule.Capability] = true
		}
	}
	if len(impls) > 0 {
		out = append(out, retired("[[implementation]] "+strings.Join(impls, ", "), "the Kivgraph provider", "delete the blocks"))
	}
	if len(named) > 0 {
		ids := slices.Sorted(maps.Keys(named))
		section := fmt.Sprintf("capability %q", ids[0])
		if len(ids) > 1 {
			quoted := make([]string, len(ids))
			for i, id := range ids {
				quoted[i] = fmt.Sprintf("%q", id)
			}
			section = "capabilities " + strings.Join(quoted, ", ")
		}
		out = append(out, retired(section, "the Kivgraph provider, the only implementation they had",
			"delete their [[capability]] blocks, every [[implementation]] that serves them and every [[selector.rule]] that names them"))
	}
	// Decoded rather than undecoded, so each refusal can name its server.
	for _, server := range decoded.MCPServers {
		if server.Dashboard != nil {
			out = append(out, retired(fmt.Sprintf("mcp_server %q key dashboard", server.ID), "the provider dashboard command", "delete the key"))
		}
	}
	return out
}

// retiredKeyMessage returns the retirement message for an undecoded settings
// key that sits under a retired path, or "" when it is not one.
func retiredKeyMessage(key string) string {
	for prefix, msg := range retiredKeys {
		if key == prefix || strings.HasPrefix(key, prefix+".") {
			return msg
		}
	}
	return ""
}

// contractRemedy names the edit that fixes a refused settings file, because
// the two numbers alone do not say which of them is meant to move.
//
// A file behind by a major version has its fix in the file: delete what the
// cutovers retired, then say the current number. Every other direction is a
// file this binary is too old to read, and no edit to the file can fix that.
// Saying "change the line" there would be advice that produces a second, more
// confusing failure.
func contractRemedy(declared contract.Version) string {
	if declared.Major < contract.Current.Major {
		remedy := fmt.Sprintf("contract 5.0.0 removed the agents, the workflow engine, the decide command, the knowledge store, the Kivgraph provider and the provider dashboard command: delete the [model], [workflow], [[workflow_profile]], [local_agents], [decision], [knowledge] and [[agent]] tables, the [orchestrator.kivgraph] tables, the \"kivgraph\" runner, the kivgraph.* implementations, the capabilities named in docs/content/migration-5.md every indexed_by key and the dashboard key of every [[mcp_server]] if present, then change the contract line to %q (see docs/content/migration-5.md)", contract.Current)
		if declared.Major < 4 {
			remedy = "remove Serena runner, adapter/process, MCP, implementations and indexed_by entries, and " + remedy
		}
		return remedy
	}
	return "this file was written for a newer core; upgrade atenea"
}

func (o fileOrchestrator) build(source string) (Orchestrator, error) {
	out := Orchestrator{
		MaxParallel:              defaultMaxParallel,
		BudgetUSD:                defaultBudgetUSD,
		Runners:                  []string{RunnerOMP},
		CheckpointDir:            checkpoint.DefaultDir(),
		ClientDeniedCapabilities: slices.Clone(clientDeniedCapabilitiesDefault),
		Local:                    LocalRunner{Implementations: defaultServedImplementations, SkipDirs: defaultSkipDirs},
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
		Tokensave: TokensaveAdapter{
			Implementations: tokensave.DefaultImplementations(),
			Timeout:         tokensave.DefaultTimeout,
		},
		Desktop: DesktopAdapter{
			Implementations: desktop.DefaultImplementations(),
			Timeout:         desktop.DefaultTimeout,
		},
		Android: AndroidAdapter{
			Implementations: android.DefaultImplementations(),
			ADBBinary:       android.DefaultADBBinary,
			ScrcpyBinary:    android.DefaultScrcpyBinary,
			HelperMode:      android.HelperModeAuto,
			Timeout:         android.DefaultTimeout,
			FrameTTL:        android.DefaultFrameTTL,
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
	if o.ClientDeniedCapabilities != nil {
		out.ClientDeniedCapabilities = slices.Clone(*o.ClientDeniedCapabilities)
	}
	if o.Runners != nil {
		seen := make(map[string]struct{}, len(*o.Runners))
		list := make([]string, 0, len(*o.Runners))
		for _, name := range *o.Runners {
			switch name {
			case RunnerOMP, RunnerClaudeCode, RunnerCodex,
				RunnerTokensave, RunnerDesktop, RunnerAndroid, RunnerLocal:
			default:
				return Orchestrator{}, contract.Fail(contract.FailureInvalidInput,
					"settings %s: orchestrator.runners has %q, which is not one of %s, %s, %s, %s, %s, %s, %s",
					source, name, RunnerOMP, RunnerClaudeCode, RunnerCodex,
					RunnerTokensave, RunnerDesktop, RunnerAndroid, RunnerLocal)
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
	androidAdapter, err := o.Android.build(source, out.Android)
	if err != nil {
		return Orchestrator{}, err
	}
	out.Android = androidAdapter
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
		{
			"retention.keep", r.Keep, &out.Keep,
			"use keep = \"0s\" to keep everything forever",
		},
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

func (a fileAndroidAdapter) build(source string, out AndroidAdapter) (AndroidAdapter, error) {
	if a.Implementations != nil {
		out.Implementations = slices.Clone(*a.Implementations)
	}
	if value := strings.TrimSpace(a.ADBBinary); value != "" {
		out.ADBBinary = value
	}
	if value := strings.TrimSpace(a.ScrcpyBinary); value != "" {
		out.ScrcpyBinary = value
	}
	if value := strings.ToLower(strings.TrimSpace(a.HelperMode)); value != "" {
		if value != android.HelperModeAuto && value != android.HelperModeADB && value != android.HelperModeHelper {
			return AndroidAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.android.helper_mode must be auto, adb, or helper, got %q", source, a.HelperMode)
		}
		out.HelperMode = value
	}
	if a.Timeout != "" {
		timeout, err := time.ParseDuration(a.Timeout)
		if err != nil || timeout <= 0 {
			return AndroidAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.android.timeout %q must be a positive duration", source, a.Timeout)
		}
		out.Timeout = timeout
	}
	if a.FrameTTL != "" {
		ttl, err := time.ParseDuration(a.FrameTTL)
		if err != nil || ttl <= 0 {
			return AndroidAdapter{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: orchestrator.android.frame_ttl %q must be a positive duration", source, a.FrameTTL)
		}
		out.FrameTTL = ttl
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
// "orchestrator.tokensave.process"), used to name it in every error below --
// this one function backs every supervised-process table in the file, and a
// provider-specific error would send the reader to the wrong block.
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
func (d fileDesktop) build(source string) (Desktop, error) {
	out := DefaultDesktop()
	if d.Applications != nil {
		out.Applications = *d.Applications
	}
	if d.ActionApplications == nil {
		out.ActionApplications = slices.Clone(out.Applications)
		out.ActionApplicationsInherited = true
	} else {
		out.ActionApplications = *d.ActionApplications
		out.ActionApplicationsInherited = false
	}
	if d.Denied != nil {
		out.Denied = *d.Denied
	}
	if d.LookThenAct != nil {
		out.LookThenAct = *d.LookThenAct
	}
	if d.VisualFeedback != nil {
		out.VisualFeedback = *d.VisualFeedback
	}
	// "everything, and also these two" is not a wider list than "everything":
	// it is two sentences that disagree about which one is in force. Refused
	// here rather than resolved to either reading, because whichever this code
	// picked would be the one the operator did not mean half the time.
	if slices.Contains(out.Applications, AllApplications) && len(out.Applications) > 1 {
		return Desktop{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: desktop.applications lists %q beside named applications; %q already means every "+
				"application desktop.denied does not name, so remove one or the other",
			source, AllApplications, AllApplications)
	}
	if slices.Contains(out.ActionApplications, AllApplications) && len(out.ActionApplications) > 1 {
		return Desktop{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: desktop.action_applications lists %q beside named applications; %q already means every "+
				"application desktop.denied does not name, so remove one or the other",
			source, AllApplications, AllApplications)
	}
	return out, nil
}

func (a fileAndroid) build(source string) (Android, error) {
	out := Android{}
	if a.AllowedSerials == nil {
		return out, nil
	}
	seen := make(map[string]bool, len(*a.AllowedSerials))
	for _, raw := range *a.AllowedSerials {
		serial := strings.TrimSpace(raw)
		if serial == "" || serial == "*" {
			return Android{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: android.allowed_serials must contain explicit non-empty serials", source)
		}
		if seen[serial] {
			return Android{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: android.allowed_serials lists %q twice", source, serial)
		}
		seen[serial] = true
		out.AllowedSerials = append(out.AllowedSerials, serial)
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
			"capability %s declares only half a subject: subject_from and subject_kind go together", c.ID,
		)
	}
	parsed, err := contract.ParseSubjectKind(kind)
	if err != nil {
		return fmt.Errorf("capability %s: %w", c.ID, err)
	}
	if parsed == contract.SubjectNone {
		return fmt.Errorf(
			"capability %s: subject_kind is empty, which is how a capability says it has no subject -- "+
				"drop subject_from too", c.ID,
		)
	}
	index := slices.IndexFunc(out.Inputs, func(f contract.Field) bool { return f.Name == from })
	if index < 0 {
		return fmt.Errorf(
			"capability %s: subject_from names %q, which is not one of its inputs", c.ID, from,
		)
	}
	if field := out.Inputs[index]; field.Type != contract.TypeString {
		return fmt.Errorf(
			"capability %s: subject_from names %q, which is a %s -- a subject is read from a string",
			c.ID, from, field.Type,
		)
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
			Languages:   languages,
			RequiresVCS: i.Constraints.RequiresVCS,
			MinScale:    minScale,
			MaxScale:    maxScale,
			MaxInput:    maps.Clone(i.Constraints.MaxInput),
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
	out := contract.NewRepository(r.ID, r.Path, r.Languages, scale, vcs)
	if err := out.Validate(); err != nil {
		return contract.Repository{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: %v", source, err)
	}
	return out, nil
}
