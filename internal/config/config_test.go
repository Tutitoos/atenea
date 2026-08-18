package config_test

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/codebasememory"
	"github.com/Tutitoos/atenea/internal/adapter/serena"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/selector"
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
	wantIDs := []string{"code.context", "code.impact", "code.search", "graph.status", "repository.index", "symbol.calls", "symbol.consumers", "symbol.definition", "symbol.get", "symbol.implementations", "symbol.overview", "symbol.references", "symbol.unresolved"}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("capabilities = %v, want %v", ids, wantIDs)
	}

	// The symbol capabilities and code.impact are read-only providers: none
	// of them may ship declaring an effect that lets a provider write. Only
	// code.search also spawns a process to answer -- every implementation
	// behind it, ripgrep or the local stand-in, is a binary, not a library.
	// repository.index is the one deliberate exception: building an index is
	// exactly the write detection itself must never make, and the tool that
	// makes it is a process too.
	for _, capability := range cfg.Capabilities {
		var want []contract.Effect
		switch capability.ID {
		case "repository.index":
			want = []contract.Effect{contract.EffectWrite, contract.EffectProcess}
		case "code.search":
			want = []contract.Effect{contract.EffectRead, contract.EffectProcess}
		default:
			want = []contract.Effect{contract.EffectRead}
		}
		if !slices.Equal(capability.Effects, want) {
			t.Errorf("%s effects = %v, want %v", capability.ID, capability.Effects, want)
		}
	}

	// code.search's output shape from the design: a list of records, each
	// with a path and a line number. Never address it by catalog position:
	// adding an alphabetically-earlier capability must not turn this into a
	// test of an unrelated output.
	var capability contract.Capability
	for _, candidate := range cfg.Capabilities {
		if candidate.ID == "code.search" {
			capability = candidate
			break
		}
	}
	if capability.ID == "" {
		t.Fatal("code.search is absent from the shipped catalog")
	}
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
		"codebase-memory.index",
		"codebase-memory.overview",
		"kivgraph.cross_repo_consumers",
		"kivgraph.definition",
		"kivgraph.get",
		"kivgraph.overview",
		"kivgraph.references",
		"kivgraph.status",
		"kivgraph.unresolved_references",
		"ripgrep",
		"serena.definition",
		"serena.implementations",
		"serena.overview",
		"serena.references",
		"serena.search",
		"tokensave.calls",
		"tokensave.context",
		"tokensave.overview",
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
	// ripgrep confines what it reads by construction; claude-code only
	// verifies its answer afterward. Collapsing that difference into a bare
	// bool would be the same "advisory implies enforced" ambiguity this field
	// exists to remove.
	for _, impl := range cfg.Implementations {
		switch impl.ID {
		case "ripgrep":
			if impl.ScopeGuarantee != contract.ScopeConfined {
				t.Errorf("ripgrep ships with scope_guarantee=%s, want confined", impl.ScopeGuarantee)
			}
		case "claude.search":
			if impl.ScopeGuarantee != contract.ScopeFiltered {
				t.Errorf("claude.search ships with scope_guarantee=%s, want filtered", impl.ScopeGuarantee)
			}
		case "serena.search":
			if impl.ScopeGuarantee != contract.ScopeUnspecified {
				t.Errorf("%s ships with scope_guarantee=%s, want unspecified: no adapter answers it yet",
					impl.ID, impl.ScopeGuarantee)
			}
		}
	}
	// A capability is only reachable if the runner that owns its provider is
	// told to serve it. That wiring lives in the settings file while the code
	// behind it lives in the adapter, so the two drift silently: shipping
	// symbol.overview without adding serena.overview here left the whole
	// capability answering "no runner" on a fresh install, and every other
	// test still passed. Each adapter publishes what it actually has code
	// for; the shipped whitelist has to be exactly that.
	for _, tc := range []struct {
		runner  string
		shipped []string
		want    []string
	}{
		{config.RunnerSerena, cfg.Orchestrator.Serena.Implementations, serena.DefaultImplementations()},
		{config.RunnerCodebaseMemory, cfg.Orchestrator.CodebaseMemory.Implementations, codebasememory.DefaultImplementations()},
	} {
		got, want := slices.Clone(tc.shipped), slices.Clone(tc.want)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s ships implementations %v, want %v: the settings whitelist and the adapter's own code disagree",
				tc.runner, got, want)
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

// The settings page tells a user that a knob they leave out falls back to a
// compiled constant, and that today those constants say the same thing the
// shipped file says. Both halves have to stay true: the first is what the code
// does, the second is a promise that only holds until somebody edits one side
// of the pair. A drift here is silent -- a partial file keeps working, it just
// stops meaning what the file it was copied from meant.
func TestShippedKnobsAgreeWithTheCompiledFallbacks(t *testing.T) {
	shipped, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	// minimal declares a catalog and nothing else: no [core], [orchestrator],
	// [model], [metrics], [backup] or [security]. What comes back for those
	// is purely what the binary falls back to.
	fallback, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !reflect.DeepEqual(shipped.Core, fallback.Core) {
		t.Errorf("[core] shipped %+v, fallback %+v", shipped.Core, fallback.Core)
	}
	if !reflect.DeepEqual(shipped.Metrics, fallback.Metrics) {
		t.Errorf("[metrics] shipped %+v, fallback %+v", shipped.Metrics, fallback.Metrics)
	}
	if !reflect.DeepEqual(shipped.Backup, fallback.Backup) {
		t.Errorf("[backup] shipped %+v, fallback %+v", shipped.Backup, fallback.Backup)
	}
	if !reflect.DeepEqual(shipped.Security, fallback.Security) {
		t.Errorf("[security] shipped %+v, fallback %+v", shipped.Security, fallback.Security)
	}
	if !reflect.DeepEqual(shipped.Model, fallback.Model) {
		t.Errorf("[model] shipped %+v, fallback %+v", shipped.Model, fallback.Model)
	}

	// Standing effects are the documented exception, and the reason this test
	// compares the rest field by field instead of the whole struct: a grant
	// nobody wrote down is a grant nobody made, so an omitted `effects` key is
	// none rather than the shipped list.
	if len(fallback.Orchestrator.StandingEffects) != 0 {
		t.Errorf("omitting effects granted %v, want none", fallback.Orchestrator.StandingEffects)
	}
	if len(shipped.Orchestrator.StandingEffects) == 0 {
		t.Error("the shipped file grants no standing effect; the settings page says it ships one")
	}

	// The client floor is the second exception and for the opposite reason:
	// an omitted key is not "none" but "whatever the operator granted
	// standing", so the fallback inherits and the shipped file does not. That
	// difference is the feature -- a file that writes the key has separated
	// the two grants, and one that does not has left them joined.
	if !fallback.Orchestrator.ClientEffectsInherited {
		t.Error("omitting client_effects did not inherit; a file written before the key existed would change behavior")
	}
	if shipped.Orchestrator.ClientEffectsInherited {
		t.Error("the shipped file leaves clients joined to the operator's grant; it is meant to ship them separated")
	}
	// Separated, but not different: on a fresh install the two lists agree, so
	// nothing a new operator does on day one behaves differently for having
	// the key. What it buys is the day they widen their own.
	if !slices.Equal(shipped.Orchestrator.ClientEffects, shipped.Orchestrator.StandingEffects) {
		t.Errorf("shipped client floor %v, standing %v: a fresh install should grant clients exactly what it grants the console",
			shipped.Orchestrator.ClientEffects, shipped.Orchestrator.StandingEffects)
	}

	shippedOrchestrator, fallbackOrchestrator := shipped.Orchestrator, fallback.Orchestrator
	shippedOrchestrator.StandingEffects, fallbackOrchestrator.StandingEffects = nil, nil
	shippedOrchestrator.ClientEffects, fallbackOrchestrator.ClientEffects = nil, nil
	shippedOrchestrator.ClientEffectsInherited, fallbackOrchestrator.ClientEffectsInherited = false, false
	if !reflect.DeepEqual(shippedOrchestrator, fallbackOrchestrator) {
		t.Errorf("[orchestrator] shipped %+v, fallback %+v", shippedOrchestrator, fallbackOrchestrator)
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
contract = "3.0.0"

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

// The per-repository policy, read back whole.
//
// This replaces `repository.serena_endpoint`, which said the same thing one
// repository at a time and pointed at a process Atenea did not own.
func TestAPerRepositoryProcessIsReadBack(t *testing.T) {
	body := minimal + `
[orchestrator.serena.process]
command = "serena"
args = ["start-mcp-server", "--port", "{{port}}", "--project", "{{project}}"]
lifecycle = "on_demand"
instance = "per_repository"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Orchestrator.Serena.Process.Instance; got != config.InstancePerRepository {
		t.Errorf("instance = %q, want %q", got, config.InstancePerRepository)
	}
}

// Absent means shared, which is what every managed server did before the key
// existed. A file that never mentions it must not change behavior.
func TestTheInstancePolicyDefaultsToShared(t *testing.T) {
	body := minimal + `
[orchestrator.serena.process]
command = "serena"
args = ["start-mcp-server", "--port", "{{port}}"]
lifecycle = "on_demand"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Orchestrator.Serena.Process.Instance; got != config.InstanceShared {
		t.Errorf("instance = %q, want %q", got, config.InstanceShared)
	}
}

// Each of these declares a policy that cannot do what it says, and each would
// fail silently rather than loudly: N identical servers, a project literally
// named `{{project}}`, or every repository after the first crashing on a port
// that is already bound.
func TestAnUnworkableInstancePolicyIsRefused(t *testing.T) {
	process := func(extra string) string {
		return minimal + "\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"on_demand\"\n" + extra
	}
	cases := map[string]string{
		"unknown policy": process("instance = \"per_chat\"\n"),
		"per repository without a project placeholder": process(
			"instance = \"per_repository\"\nargs = [\"--port\", \"{{port}}\"]\n"),
		"a project placeholder with nothing to substitute": process(
			"args = [\"--project\", \"{{project}}\"]\n"),
		"per repository on one fixed port": process(
			"instance = \"per_repository\"\nargs = [\"--project\", \"{{project}}\"]\nport = 9121\n"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, body))
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input (err=%v)", got, err)
			}
		})
	}
}

// The field this replaced is gone from the vocabulary, not merely unused. A
// key that still parsed and did nothing would be the exact failure the unknown
// key check exists to prevent.
func TestTheOldPerRepositoryEndpointIsNoLongerAKey(t *testing.T) {
	body := strings.Replace(minimal, "vcs = \"present\"\n",
		"vcs = \"present\"\nserena_endpoint = \"http://127.0.0.1:9121/mcp\"\n", 1)
	_, err := config.Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("err = %v, want an unknown key refusal", err)
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
		"missing":     strings.Replace(minimal, `contract = "3.0.0"`, "", 1),
		"unparseable": strings.Replace(minimal, `contract = "3.0.0"`, `contract = "one"`, 1),
		"too new":     strings.Replace(minimal, `contract = "3.0.0"`, `contract = "9.0.0"`, 1),
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

// Two version numbers in a sentence do not say which one is meant to move,
// and the two directions have opposite fixes: a file left behind by a
// breaking release needs one line changed, a file from the future needs a
// newer binary and no edit at all. Telling the second case to edit the line
// would buy it a second, more confusing failure.
func TestARefusedContractNamesTheEditThatFixesIt(t *testing.T) {
	behind := strings.Replace(minimal, `contract = "3.0.0"`, `contract = "1.0.0"`, 1)
	_, err := config.Load(write(t, behind))
	if err == nil {
		t.Fatal("a file from the previous major was accepted")
	}
	// Derived, not typed: the fix a refusal names is whatever this core speaks
	// today, and a literal here would have to be edited on every bump -- which
	// is a test that fails for being out of date rather than for being wrong.
	wantFix := `change the contract line to "` + contract.Current.String() + `"`
	if !strings.Contains(err.Error(), wantFix) {
		t.Errorf("err = %v, want %s", err, wantFix)
	}

	ahead := strings.Replace(minimal, `contract = "3.0.0"`, `contract = "9.0.0"`, 1)
	_, err = config.Load(write(t, ahead))
	if err == nil {
		t.Fatal("a file from the future was accepted")
	}
	if strings.Contains(err.Error(), "change the contract line") {
		t.Errorf("err = %v, want no edit suggested: no edit to the file can fix it", err)
	}
	if !strings.Contains(err.Error(), "upgrade atenea") {
		t.Errorf("err = %v, want it to name the binary as the thing that is behind", err)
	}
}

// rawBlock is the smallest declaration that reaches the raw-only rules: a
// valid HTTP endpoint exposed raw, with whatever the case under test adds.
func rawBlock(extra string) string {
	return "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\nexpose = \"raw\"\n" + extra
}

// A declaration nothing can check is worse than no declaration, because a
// client is told the server exists before anyone finds out it does not.
// Every rule here is that one rule: an entry must name exactly one
// reachable thing.
func TestBrokenMCPServerBlocksAreRefused(t *testing.T) {
	cases := map[string]string{
		"no id":            "\n[[mcp_server]]\nurl = \"http://127.0.0.1:1/mcp\"\n",
		"neither":          "\n[[mcp_server]]\nid = \"x\"\n",
		"both":             "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\ncommand = [\"sh\"]\n",
		"relative url":     "\n[[mcp_server]]\nid = \"x\"\nurl = \"/mcp\"\n",
		"wrong scheme":     "\n[[mcp_server]]\nid = \"x\"\nurl = \"ws://127.0.0.1:1/mcp\"\n",
		"empty argument":   "\n[[mcp_server]]\nid = \"x\"\ncommand = [\"sh\", \"\"]\n",
		"bad timeout":      "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\ntimeout = \"soon\"\n",
		"negative timeout": "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\ntimeout = \"-1s\"\n",
		"dotted id":        "\n[[mcp_server]]\nid = \"a.b\"\nurl = \"http://127.0.0.1:1/mcp\"\n",
		"unknown expose":   "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\nexpose = \"true\"\n",
		// A stdio raw block still has to carry the same budget as any other
		// one; what is no longer refused is the transport itself, which
		// TestAStdioBackendIsDeclaredLikeAnyOther pins from the other side.
		"raw over stdio without tools": "\n[[mcp_server]]\nid = \"x\"\ncommand = [\"sh\"]\nexpose = \"raw\"\n",
		// A raw block has to say what it may offer and what that costs.
		// Neither has a default: one reading of an absent list widens the
		// machine to whatever the backend ships next, the other is a
		// declaration that does nothing, and guessing between them is the
		// mistake.
		"raw without tools":    rawBlock("") + "effects = [\"read\"]\n",
		"raw with empty tools": rawBlock("") + "tools = []\neffects = [\"read\"]\n",
		"raw without effects":  rawBlock("") + "tools = [\"scan\"]\n",
		"empty tool name":      rawBlock("") + "tools = [\"scan\", \" \"]\neffects = [\"read\"]\n",
		"repeated tool":        rawBlock("") + "tools = [\"scan\", \"scan\"]\neffects = [\"read\"]\n",
		"unknown effect":       rawBlock("") + "tools = [\"scan\"]\neffects = [\"telepathy\"]\n",
		"repeated effect":      rawBlock("") + "tools = [\"scan\"]\neffects = [\"read\", \"read\"]\n",
		// A per-tool block for a tool the allow list never named describes a
		// call that is refused one layer earlier: left in, it reads as
		// coverage nobody has.
		"tool outside the list": rawBlock("") + "tools = [\"scan\"]\neffects = [\"read\"]\n" +
			"\n  [[mcp_server.tool]]\n  name = \"fix\"\n  effects = [\"write\"]\n",
		"tool with no name": rawBlock("") + "tools = [\"scan\"]\neffects = [\"read\"]\n" +
			"\n  [[mcp_server.tool]]\n  effects = [\"write\"]\n",
		"tool with no effects": rawBlock("") + "tools = [\"scan\"]\neffects = [\"read\"]\n" +
			"\n  [[mcp_server.tool]]\n  name = \"scan\"\n",
		"tool declared twice": rawBlock("") + "tools = [\"scan\"]\neffects = [\"read\"]\n" +
			"\n  [[mcp_server.tool]]\n  name = \"scan\"\n  effects = [\"write\"]\n" +
			"\n  [[mcp_server.tool]]\n  name = \"scan\"\n  effects = [\"read\"]\n",
		// A budget on a pointer is a rule with nothing to apply it to:
		// Atenea never sees the call, so the tools travel straight from the
		// client to the server.
		"tools without raw":   "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\ntools = [\"scan\"]\n",
		"effects without raw": "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\neffects = [\"read\"]\n",
		"tool block without raw": "\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\n" +
			"\n  [[mcp_server.tool]]\n  name = \"scan\"\n  effects = [\"read\"]\n",
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+block))
			if err == nil {
				t.Fatal("accepted")
			}
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("kind = %v", contract.KindOf(err))
			}
		})
	}
}

// Two blocks under one id is the case that would otherwise pass and then
// half-work: the payload is keyed by id, so the later entry wins silently
// and the earlier one is never probed, never declared, and never mentioned.
// Only the file can say which one it meant.
func TestADuplicateMCPServerIDIsRefused(t *testing.T) {
	body := minimal +
		"\n[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\n" +
		"\n[[mcp_server]]\nid = \"x\"\ncommand = [\"sh\"]\n"
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("two blocks under one id were accepted")
	}
	if !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("err = %v, want it to name the duplicate", err)
	}
}

// The happy path, and the one thing about it worth pinning: a url is
// normalized once here rather than at every point of use.
func TestADeclaredMCPServerIsReadBack(t *testing.T) {
	body := minimal + "\n[[mcp_server]]\nid = \"serena\"\nurl = \"http://127.0.0.1:40010/mcp\"\ntimeout = \"3s\"\n" +
		"\n[[mcp_server]]\nid = \"local\"\ncommand = [\"sh\", \"-c\", \"true\"]\n\n[mcp_server.env]\nK = \"v\"\n"
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("servers = %d, want 2", len(cfg.MCPServers))
	}
	if got := cfg.MCPServers[0]; got.URL != "http://127.0.0.1:40010/mcp" || got.Timeout != 3*time.Second {
		t.Errorf("first = %+v, want the declared url and timeout", got)
	}
	if got := cfg.MCPServers[1]; len(got.Command) != 3 || got.Env["K"] != "v" {
		t.Errorf("second = %+v, want the declared command and env", got)
	}
}

// expose is the field that separates a pointer from a passthrough, and a raw
// block carries the budget the passthrough is held to. What is worth pinning
// is the default an older file inherits, the budget reading back whole, and
// the catalog staying untouched by any of it.
func TestExposeDefaultsToPointerAndReadsBackRawWithItsBudget(t *testing.T) {
	body := minimal +
		"\n[[mcp_server]]\nid = \"quiet\"\nurl = \"http://127.0.0.1:1/mcp\"\n" +
		"\n[[mcp_server]]\nid = \"semgrep\"\nurl = \"http://127.0.0.1:40020/mcp\"\nexpose = \"raw\"\n" +
		"tools = [\"semgrep_scan\", \"semgrep_fix\"]\neffects = [\"read\"]\n" +
		"\n  [[mcp_server.tool]]\n  name = \"semgrep_fix\"\n  effects = [\"read\", \"write\"]\n"
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.MCPServers[0].Expose; got != config.ExposeOff {
		t.Errorf("absent expose = %q, want %q", got, config.ExposeOff)
	}
	raw := cfg.MCPServers[1]
	if raw.Expose != config.ExposeRaw {
		t.Errorf("declared expose = %q, want %q", raw.Expose, config.ExposeRaw)
	}
	if want := []string{"semgrep_scan", "semgrep_fix"}; !slices.Equal(raw.Tools, want) {
		t.Errorf("tools = %v, want %v", raw.Tools, want)
	}
	// A tool with no block of its own causes what the server declared; one
	// with a block causes what the block says. There is no third answer,
	// which is what makes an undeclared effect impossible rather than
	// merely discouraged.
	if got := raw.EffectsOf("semgrep_scan"); !slices.Equal(got, []contract.Effect{contract.EffectRead}) {
		t.Errorf("inherited effects = %v, want the server's read", got)
	}
	want := []contract.Effect{contract.EffectRead, contract.EffectWrite}
	if got := raw.EffectsOf("semgrep_fix"); !slices.Equal(got, want) {
		t.Errorf("narrowed effects = %v, want %v", got, want)
	}
	// A backend declared raw adds no capability and no implementation to the
	// catalog it loaded beside: the two namespaces stay disjoint.
	for _, c := range cfg.Capabilities {
		if strings.HasPrefix(c.ID, contract.ReservedNamespace+".") {
			t.Errorf("capability %s came from a declaration", c.ID)
		}
	}
	for _, i := range cfg.Implementations {
		if strings.HasPrefix(i.ID, contract.ReservedNamespace+".") {
			t.Errorf("implementation %s came from a declaration", i.ID)
		}
	}
}

// A stdio backend is declared the way an HTTP one is: same expose, same
// budget, same effects, and the command in place of the url.
//
// The transport used to be refused here with "not built yet", so this is the
// test that the refusal is gone and that nothing else went with it -- the
// rules that make a raw block honest are per-block, not per-transport.
func TestAStdioBackendIsDeclaredLikeAnyOther(t *testing.T) {
	body := minimal +
		"\n[[mcp_server]]\nid = \"codebase-memory\"\ncommand = [\"codebase-memory-mcp\", \"--ui=true\"]\n" +
		"expose = \"raw\"\ntools = [\"search_code\", \"index_repository\"]\neffects = [\"read\"]\n" +
		"env = { CBM_HOME = \"/tmp/cbm\" }\n" +
		"\n  [[mcp_server.tool]]\n  name = \"index_repository\"\n  effects = [\"read\", \"write\"]\n"
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	server := cfg.MCPServers[0]
	if server.Expose != config.ExposeRaw {
		t.Errorf("expose = %q, want %q", server.Expose, config.ExposeRaw)
	}
	if want := []string{"codebase-memory-mcp", "--ui=true"}; !slices.Equal(server.Command, want) {
		t.Errorf("command = %v, want %v", server.Command, want)
	}
	if server.URL != "" {
		t.Errorf("url = %q, want empty: this block declared a command", server.URL)
	}
	if got := server.Env["CBM_HOME"]; got != "/tmp/cbm" {
		t.Errorf("env = %v, want the declared CBM_HOME", server.Env)
	}
	// The budget and the effects are the same rules they are over HTTP:
	// nothing about them is transport-specific, and a copy that only ran on
	// one transport is how they would drift apart.
	if want := []string{"search_code", "index_repository"}; !slices.Equal(server.Tools, want) {
		t.Errorf("tools = %v, want %v", server.Tools, want)
	}
	if got := server.EffectsOf("search_code"); !slices.Equal(got, []contract.Effect{contract.EffectRead}) {
		t.Errorf("inherited effects = %v, want the server's read", got)
	}
	want := []contract.Effect{contract.EffectRead, contract.EffectWrite}
	if got := server.EffectsOf("index_repository"); !slices.Equal(got, want) {
		t.Errorf("narrowed effects = %v, want %v", got, want)
	}
}

func TestBrokenCatalogEntriesAreRefused(t *testing.T) {
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

// Settings replace the catalog wholesale rather than patching it, so a file
// written before a release never gains what that release shipped. Measured on
// a real machine: a file predating v0.6.0 still declared one implementation of
// symbol.overview after the binary began shipping two, so the funnel ran with
// a single candidate and the missing one was never the suspect.
func TestAStaleCatalogNamesWhatItIsMissing(t *testing.T) {
	full := filepath.Join(t.TempDir(), "full.toml")
	if err := config.WriteDefault(full, false); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Drop one implementation block, leaving its capability and its sibling
	// implementations in place: that is exactly the shape an older file has.
	blocks := strings.Split(string(body), "[[implementation]]")
	dropped := ""
	kept := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if dropped == "" && strings.Contains(block, `id = "codebase-memory.overview"`) {
			dropped = "codebase-memory.overview"
			continue
		}
		kept = append(kept, block)
	}
	if dropped == "" {
		t.Fatal("the shipped catalog no longer declares codebase-memory.overview; pick another block")
	}

	stale, err := config.Load(write(t, strings.Join(kept, "[[implementation]]")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Contains(stale.Missing, dropped) {
		t.Errorf("Missing = %v, want it to name %s", stale.Missing, dropped)
	}

	// The shipped file is never stale against itself, or the warning would be
	// on every screen from the first boot and worth nothing.
	shipped, err := config.Load(full)
	if err != nil {
		t.Fatalf("Load shipped: %v", err)
	}
	if len(shipped.Missing) != 0 {
		t.Errorf("Missing = %v on a freshly written file, want none", shipped.Missing)
	}

	// Dropping a whole capability is a deliberate act, not drift: `minimal`
	// declares code.search alone, so the providers behind the seven
	// capabilities it does not want must not be named at it.
	trimmed, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load minimal: %v", err)
	}
	catalog, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	searchOnly := make(map[string]bool)
	for _, impl := range catalog.Implementations {
		if impl.Capability == "code.search" {
			searchOnly[impl.ID] = true
		}
	}
	if len(trimmed.Missing) == 0 {
		t.Fatal("a catalog of one capability is missing none of its shipped providers")
	}
	for _, id := range trimmed.Missing {
		if !searchOnly[id] {
			t.Errorf("Missing names %s, which answers a capability this file dropped on purpose", id)
		}
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

// Two keys decide where a run's paper copy goes, and they are not
// symmetrical. `checkpoint_dir` follows the ordinary override rule, where an
// empty string is indistinguishable from an absent one and therefore
// inherits rather than blanks. So turning checkpointing off is not something
// a path can say: only `checkpoints = false` can, and it wins over a path
// written beside it. `atenea status` prints the result of this on its `runs`
// line, as a directory or as "off".
func TestOnlyCheckpointsFalseTurnsRunsOff(t *testing.T) {
	shipped, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	fallback := shipped.Orchestrator.CheckpointDir
	if fallback == "" {
		t.Fatal("a fresh install has nowhere to write its receipts")
	}
	for _, tc := range []struct {
		name string
		keys string
		want string
	}{
		{"omitted", "", fallback},
		{"an empty dir reads as absent", "checkpoint_dir = \"\"\n", fallback},
		{"an explicit dir is obeyed", "checkpoint_dir = \"/tmp/receipts\"\n", "/tmp/receipts"},
		{"on, with an empty dir", "checkpoints = true\ncheckpoint_dir = \"\"\n", fallback},
		{"off", "checkpoints = false\n", ""},
		{"off beats an explicit dir", "checkpoints = false\ncheckpoint_dir = \"/tmp/receipts\"\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(write(t, minimal+"\n[orchestrator]\n"+tc.keys))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Orchestrator.CheckpointDir; got != tc.want {
				t.Errorf("checkpoint_dir = %q, want %q", got, tc.want)
			}
		})
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

// The same refusal for the client floor, and it needs its own test rather than
// riding on the one above: the two lists are parsed by two loops, and a key
// that accepted "ghost" would hand a client a grant nobody could name.
func TestAnUnknownClientEffectIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"\n[orchestrator]\nclient_effects = [\"ghost\"]\n"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
	// Naming the key, not just the value: an operator with both lists written
	// has two places to look and one of them is right.
	if !strings.Contains(err.Error(), "orchestrator.client_effects") {
		t.Errorf("the error does not name the key that rejected it: %v", err)
	}
}

// An empty list is the one value that has to survive the round trip meaning
// what it says. Everywhere else in this file an empty list is indistinguishable
// from an absent key; here that would silently hand a client the operator's
// whole floor -- the precise client that was meant to have none.
func TestAnEmptyClientFloorIsNotAnAbsentOne(t *testing.T) {
	empty, err := config.Load(write(t, minimal+"\n[orchestrator]\neffects = [\"process\"]\nclient_effects = []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(empty.Orchestrator.ClientEffects) != 0 {
		t.Errorf("client_effects = [] granted %v, want none", empty.Orchestrator.ClientEffects)
	}
	if empty.Orchestrator.ClientEffectsInherited {
		t.Error("client_effects = [] was read as an absent key")
	}

	absent, err := config.Load(write(t, minimal+"\n[orchestrator]\neffects = [\"process\"]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(absent.Orchestrator.ClientEffects, absent.Orchestrator.StandingEffects) {
		t.Errorf("an absent key granted %v, want the standing %v",
			absent.Orchestrator.ClientEffects, absent.Orchestrator.StandingEffects)
	}
	if !absent.Orchestrator.ClientEffectsInherited {
		t.Error("an absent key was not recorded as inherited, so the screen cannot say so")
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

func TestAnUnusableModelTimeoutIsRefused(t *testing.T) {
	for _, timeout := range []string{"never", "0s", "-5s"} {
		_, err := config.Load(write(t, minimal+"\n[model]\ntimeout = \""+timeout+"\"\n"))
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("timeout = %q -> %v, want invalid_input", timeout, got)
		}
	}
}

// Model's fields have to keep the same names, types and order as
// model.Options: internal/agent/planner converts cfg.Model to it with a
// plain struct conversion, which the compiler only accepts when the two
// shapes are identical. This is the read half of that contract -- the file
// values have to reach the struct unchanged, per role.
func TestTheModelBlockIsReadPerRole(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+`
[model]
binary = "claude-custom"
timeout = "45s"
explore = "haiku"
plan = "opus"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Model{Binary: "claude-custom", Timeout: 45 * time.Second, Explore: "haiku", Plan: "opus"}
	if cfg.Model != want {
		t.Errorf("Model = %+v, want %+v", cfg.Model, want)
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
		"\n[model]\ntimeot = \"90s\"\n",
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

// The duration knobs are the supervisor package's to default, not config's:
// a Spec built from a Process that never mentioned them must arrive at
// supervisor.Spec.withDefaults still zero, so there is exactly one place
// those numbers are decided rather than two that could drift apart.
//
// restart_limit is the exception and is asserted separately below: zero is a
// legitimate value there, so the supervisor cannot tell it from an omitted key
// and config has to resolve it while the pointer still says which it was.
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
	if got.Port != 0 || got.RestartDelay != 0 ||
		got.StableAfter != 0 || got.ReadyTimeout != 0 || got.IdleTimeout != 0 || got.StopGrace != 0 {
		t.Errorf("Process = %+v, want every unset timing left at zero for the supervisor to default", got)
	}
}

// A knob that quietly does nothing is the failure this whole settings layer
// keeps being audited for, and idle_timeout under a persistent server is the
// one case with nothing anywhere to give it away. Every other knob that stops
// applying is explained by something the reader can see: `enabled = false`,
// `restart_limit = 0`, a status line that says "off". The idle reaper simply
// skips persistent servers, so the key is inert for a reason that lives in
// the supervisor and appears nowhere in the file. Refused at load instead.
func TestIdleTimeoutIsRefusedForAPersistentServer(t *testing.T) {
	const table = "\n[orchestrator.serena.process]\ncommand = \"serena\"\n"

	_, err := config.Load(write(t, minimal+table+
		"lifecycle = \"persistent\"\nidle_timeout = \"30s\"\n"))
	if err == nil {
		t.Fatal("idle_timeout was accepted for a server the reaper never touches")
	}
	if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", kind)
	}
	// The message has to name the way out, not just the mistake: both
	// lifecycles, so the reader can see which one the key belongs to.
	for _, want := range []string{"idle_timeout", "persistent", "on_demand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// The same key is ordinary on the lifecycle it describes, and a
	// persistent server that never mentions it stays perfectly legal.
	for name, body := range map[string]string{
		"on_demand with idle_timeout": "lifecycle = \"on_demand\"\nidle_timeout = \"30s\"\n",
		"persistent without one":      "lifecycle = \"persistent\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(write(t, minimal+table+body)); err != nil {
				t.Errorf("Load: %v", err)
			}
		})
	}
}

// supervisor.DefaultRestartLimit exists because the design wants "a couple of
// times" before a crashed server is given up on. Nothing was applying it: the
// supervisor documents the choice as the Spec builder's, config left the field
// at its zero, and zero means never retry. A settings file that opted into a
// managed Serena without naming restart_limit therefore got a server that was
// marked down on its first crash, and the constant was referenced nowhere.
func TestSerenaProcessRestartLimitIsResolvedWhereZeroStillMeansSomething(t *testing.T) {
	table := minimal + "\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"persistent\"\n"

	omitted, err := config.Load(write(t, table))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := omitted.Orchestrator.Serena.Process.RestartLimit; got != supervisor.DefaultRestartLimit {
		t.Errorf("omitted restart_limit = %d, want the supervisor default %d",
			got, supervisor.DefaultRestartLimit)
	}

	// Explicit zero has to survive, or turning retries off would be
	// impossible to say -- which is the whole reason the field is a pointer.
	off, err := config.Load(write(t, table+"restart_limit = 0\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := off.Orchestrator.Serena.Process.RestartLimit; got != 0 {
		t.Errorf("explicit restart_limit = 0 became %d; never retry must stay sayable", got)
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

// The shipped file is where a closed set stops being a language feature and
// starts being a promise, so both halves are checked against each other here:
// every value the file declares must be accepted, and one it does not declare
// must be refused. A set that is declared but not enforced, or enforced but
// not declared, is the drift this pins.
func TestEveryDeclaredSetInTheShippedFileIsTheEnforcedSet(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	checked := 0
	for _, capability := range cfg.Capabilities {
		for _, kind := range []struct {
			name   string
			fields []contract.Field
			check  func(map[string]any) error
		}{
			{"input", capability.Inputs, capability.ValidateInput},
			{"output", capability.Outputs, capability.ValidateOutput},
		} {
			for _, field := range kind.fields {
				if len(field.Enum) == 0 || field.Type != contract.TypeString {
					continue
				}
				checked++
				for _, value := range field.Enum {
					payload := required(kind.fields)
					payload[field.Name] = value
					if err := kind.check(payload); err != nil {
						t.Errorf("%s %s %s=%q refused: %v",
							capability.ID, kind.name, field.Name, value, err)
					}
				}
				payload := required(kind.fields)
				payload[field.Name] = "definitely-not-declared"
				if err := kind.check(payload); err == nil {
					t.Errorf("%s %s %s accepted a value outside its declared set",
						capability.ID, kind.name, field.Name)
				}
			}
		}
	}
	// Without this the test passes on a file that declares no sets at all,
	// which is exactly how the feature would quietly die in the catalog.
	if checked < 2 {
		t.Fatalf("only %d closed string field(s) found in the shipped catalog", checked)
	}
}

// required builds the smallest payload the field list accepts, so a set can be
// checked without a missing sibling failing first.
func required(fields []contract.Field) map[string]any {
	payload := make(map[string]any, len(fields))
	for _, field := range fields {
		if !field.Required {
			continue
		}
		switch field.Type {
		case contract.TypeString:
			if len(field.Enum) > 0 {
				payload[field.Name] = field.Enum[0]
			} else {
				payload[field.Name] = "x"
			}
		case contract.TypeInt:
			payload[field.Name] = 1
		case contract.TypeBool:
			payload[field.Name] = true
		case contract.TypeStringList:
			payload[field.Name] = []any{}
		case contract.TypeRecord:
			payload[field.Name] = required(field.Fields)
		case contract.TypeRecordList:
			payload[field.Name] = []any{}
		}
	}
	return payload
}

// A setting nobody documented is a setting nobody can find, and the shipped
// file is not the documentation: its comments explain a key to somebody who
// already knows it is there. The settings page is where a reader goes to
// learn what exists at all.
//
// This was 44 of 45 by hand before it was a test, so it is a guard on a
// convention that already held rather than a new demand. The one that was
// missing was the key added the same day this test was: the manual habit
// failed on its first opportunity, which is the whole argument for the test.
func TestEverySettingIsOnTheSettingsPage(t *testing.T) {
	shipped, err := os.ReadFile("default.toml")
	if err != nil {
		t.Fatalf("read the shipped settings: %v", err)
	}
	page, err := os.ReadFile("../../docs/content/settings.md")
	if err != nil {
		t.Fatalf("read the settings page: %v", err)
	}

	// Bare keys only. A table header names a section rather than a setting,
	// and a commented-out line is prose about one.
	key := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	seen := make(map[string]struct{})
	for line := range strings.SplitSeq(string(shipped), "\n") {
		match := key.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		if _, already := seen[match[1]]; already {
			continue
		}
		seen[match[1]] = struct{}{}
		if !strings.Contains(string(page), match[1]) {
			t.Errorf("%s is shipped in default.toml and never named on the settings page", match[1])
		}
	}
	if len(seen) == 0 {
		t.Fatal("no settings were found in default.toml; this test would pass for an empty file")
	}
}

// A fresh install has classified nothing, and the settings page says an
// unclassified scale "never disqualifies anyone: an unknown fact is not a
// proven mismatch, and dropping candidates over it would silently empty the
// funnel". The shipped repository is exactly that repository -- nobody has
// measured it -- so the shipped file has to leave the question open rather
// than answer it on the operator's behalf. Declaring a size costs whoever
// asked for it: a guess of "small" drops every graph implementation on day
// one, and the funnel reports the drop correctly, which makes the capability
// look unimplemented instead of unclassified.
func TestTheShippedRepositoryClassifiesNothing(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if len(cfg.Repositories) == 0 {
		t.Fatal("the shipped file declares no repository; this test would pass for an empty catalog")
	}

	// Teeth: the assertion below is only worth making while some shipped
	// implementation actually constrains scale. If none did, an unclassified
	// repository would drop nothing for reasons that have nothing to do with
	// the principle under test.
	constrains := false
	for _, impl := range cfg.Implementations {
		if impl.Constraints.MinScale != contract.ScaleUnspecified ||
			impl.Constraints.MaxScale != contract.ScaleUnspecified {
			constrains = true
			break
		}
	}
	if !constrains {
		t.Fatal("no shipped implementation constrains scale; this test would pass vacuously")
	}

	sel, err := selector.New(cfg.Selector)
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	reachable := make([]string, 0, len(cfg.Implementations))
	providers := make([]string, 0, len(cfg.Implementations))
	for _, impl := range cfg.Implementations {
		reachable = append(reachable, impl.ID)
		if !slices.Contains(providers, impl.Provider) {
			providers = append(providers, impl.Provider)
		}
	}

	for _, shipped := range cfg.Repositories {
		if shipped.Scale != contract.ScaleUnspecified {
			t.Errorf("the shipped repository %s is classified %q; a fresh install has measured nothing",
				shipped.ID, shipped.Scale.String())
		}
		// An index the operator has not declared is dropped before size is
		// ever weighed, which would hide the drop this test is looking for.
		// Declaring one is the single step a fresh install is expected to
		// take, so the funnel below runs on a repository that took it: after
		// that, a size nobody measured is the only thing left that could
		// empty it.
		repo := contract.NewRepository(shipped.ID, shipped.Path, shipped.Languages,
			shipped.Scale, shipped.VCS, providers)
		for _, capability := range cfg.Capabilities {
			candidates := make([]contract.Implementation, 0, len(cfg.Implementations))
			for _, impl := range cfg.Implementations {
				if impl.Capability == capability.ID {
					candidates = append(candidates, impl)
				}
			}
			if len(candidates) == 0 {
				continue
			}
			// A capability with nothing left is not this test's business: the
			// shipped file leaves indexed_by empty on purpose, and serena has
			// no probe that could fill it in, so an empty funnel is the
			// documented state of a fresh install. Select hands back the trace
			// either way, and the trace is what is under test.
			decision, _ := sel.Select(selector.Request{
				Capability: capability.ID,
				Repository: repo,
				Candidates: candidates,
				Reachable:  reachable,
			})
			for _, stage := range decision.Stages {
				for _, drop := range stage.Dropped {
					if strings.Contains(drop.Reason, "scale") ||
						strings.Contains(drop.Reason, "repository or bigger") {
						t.Errorf("%s in %s: %s dropped over an unmeasured size: %s",
							capability.ID, repo.ID, drop.Implementation, drop.Reason)
					}
				}
			}
		}
	}
}

// A key that parses into a struct field nobody assigns is read and thrown
// away: the file says one thing, the loaded settings say another, and no
// error is raised anywhere. That happened to `reads_subject` while it was
// being added and no existing test noticed, so the round trip is asserted
// here, in both of its readings.
func TestAnAgentThatDeclaresItReadsASubjectKeepsIt(t *testing.T) {
	body := minimal + `
[[agent]]
name = "planner"
kind = "specialized"
summary = "Reads an exploration and returns a graph"
command = "/bin/true"
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1
reads_subject = true

  [[agent.result]]
  name = "plan"
  type = "string"
  required = true
  summary = "The graph"

[[agent]]
name = "reader"
kind = "specialized"
summary = "Reads a file"
command = "/bin/true"
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1

  [[agent.result]]
  name = "ok"
  type = "bool"
  required = true
  summary = "it read"

[[agent]]
name = "critic"
kind = "specialized"
summary = "Audits an answer"
command = "/bin/true"
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1
pool = "review"

  [[agent.result]]
  name = "checked"
  type = "int"
  required = true
  summary = "how many claims"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byName := map[string]config.AgentType{}
	for _, a := range cfg.Agents {
		byName[a.Spec.Name] = a
	}

	planner := byName["planner"]
	if !planner.ReadsSubject {
		t.Error("reads_subject = true was read and dropped")
	}
	if !planner.ReadsASubject() {
		t.Error("a type that declares the input does not read one")
	}
	if planner.Pool != config.PoolAgent {
		t.Errorf("pool = %v: declaring the input must not move the lane", planner.Pool)
	}

	if byName["reader"].ReadsASubject() {
		t.Error("a plain agent reads a subject")
	}
	// The implication, which is why the flag is never written on a reviewer.
	if !byName["critic"].ReadsASubject() {
		t.Error("a reviewer does not read a subject")
	}
	if byName["critic"].ReadsSubject {
		t.Error("the reviewer's declared value was invented rather than implied")
	}
}

// A settings file is a copy of the shipped defaults, not a reference to them.
// Measured 2026-08-14: a file six weeks old had lost all five shipped agent
// types and read as working configuration until a workflow named one and got
// "declared are none".

func TestAFileThatPredatesAnAgentTypeStillGetsIt(t *testing.T) {
	cfg, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	shipped, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if len(shipped.Agents) == 0 {
		t.Fatal("the built-in settings declare no agents; this test proves nothing")
	}

	got := make(map[string]bool, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		got[agent.Spec.Name] = true
	}
	for _, agent := range shipped.Agents {
		if !got[agent.Spec.Name] {
			t.Errorf("agent %q is missing: a file that never mentioned it cannot have meant to drop it",
				agent.Spec.Name)
		}
	}
}

// `reader` is `explore` with Atenea's capability catalog taken off, and it is
// worth declaring only while it stays exactly that: the saving is entirely in
// what `agent-exec reader` hands the turn. Measured 2026-08-15 on one
// repository against one model, cold: starting an `explore` turn costs $0.27
// and 26,603 tokens of prefix, and the identical probe with no tools at all
// costs $0.06 and 4,991.
//
// So this asserts the pair rather than the block. Everything a step of either
// type may see, cause, spend and answer has to match, or they are no longer
// one agent on two surfaces -- and a planner choosing between them on price
// would be choosing between two different agents.
func TestTheShippedReaderIsExploreOnACheaperSurface(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	reader, err := cfg.AgentTypeByName("reader")
	if err != nil {
		t.Fatalf("the shipped settings declare no reader: %v", err)
	}
	explore, err := cfg.AgentTypeByName("explore")
	if err != nil {
		t.Fatalf("AgentTypeByName(explore): %v", err)
	}

	// What it spawns: this binary, under the name cmd/atenea dispatches the
	// cheap surface on. The whole difference between the two types is here.
	if reader.Command != explore.Command {
		t.Errorf("command = %q, want %q", reader.Command, explore.Command)
	}
	if want := []string{"agent-exec", "reader"}; !slices.Equal(reader.Args, want) {
		t.Errorf("args = %v, want %v", reader.Args, want)
	}

	// And everything it is: the same agent, so the same levels, the same
	// ceiling on what it may cause, the same ceiling on one run, the same
	// lane, and the same answer.
	if reader.Spec.Kind != explore.Spec.Kind {
		t.Errorf("kind = %v, want explore's %v", reader.Spec.Kind, explore.Spec.Kind)
	}
	if !slices.Equal(reader.Context, explore.Context) {
		t.Errorf("context = %v, want explore's %v", reader.Context, explore.Context)
	}
	if !slices.Equal(reader.Effects, explore.Effects) {
		t.Errorf("effects = %v, want explore's %v", reader.Effects, explore.Effects)
	}
	if reader.Limits != explore.Limits {
		t.Errorf("limits = %+v, want explore's %+v", reader.Limits, explore.Limits)
	}
	if reader.Pool != explore.Pool || reader.ReadsASubject() != explore.ReadsASubject() {
		t.Errorf("scheduled as %v/reads-subject %v, want explore's %v/%v",
			reader.Pool, reader.ReadsASubject(), explore.Pool, explore.ReadsASubject())
	}
	if !reflect.DeepEqual(reader.Spec.Result, explore.Spec.Result) {
		t.Errorf("result shape = %+v, want explore's %+v", reader.Spec.Result, explore.Spec.Result)
	}

	// The summary is the one line the planner picks a type off, so it has to
	// carry the tradeoff and not just the name.
	for _, want := range []string{"cannot search", "fifth"} {
		if !strings.Contains(reader.Summary, want) {
			t.Errorf("summary %q never says %q, so the menu does not state the tradeoff",
				reader.Summary, want)
		}
	}
}

// The shipped declaration is a default, and a file that names an agent means
// what it says -- including a command of its own.
func TestAFileThatNamesAnAgentKeepsItsOwn(t *testing.T) {
	body := minimal + `
[[agent]]
name = "filereader"
kind = "specialized"
summary = "the operator's own reader"
command = "/usr/local/bin/my-reader"
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1

  [[agent.result]]
  name = "content"
  type = "string"
  required = true
  summary = "what the file holds"
`
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var seen int
	for _, agent := range cfg.Agents {
		if agent.Spec.Name != "filereader" {
			continue
		}
		seen++
		if agent.Command != "/usr/local/bin/my-reader" {
			t.Errorf("command = %q, want the file's own", agent.Command)
		}
	}
	if seen != 1 {
		t.Fatalf("filereader appears %d times, want exactly one", seen)
	}
	// And the ones it did not name still arrive.
	if len(cfg.Agents) < 2 {
		t.Errorf("agents = %d, want the shipped set beside the override", len(cfg.Agents))
	}
}

// The built-in settings are parsed by the same function that merges them.
// Without the guard that is unbounded recursion, and with a wrong guard the
// shipped file would declare every agent twice.
func TestTheBuiltInSettingsAreNotMergedIntoThemselves(t *testing.T) {
	shipped, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	seen := make(map[string]int, len(shipped.Agents))
	for _, agent := range shipped.Agents {
		seen[agent.Spec.Name]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("agent %q appears %d times in the built-in settings", name, n)
		}
	}
}
