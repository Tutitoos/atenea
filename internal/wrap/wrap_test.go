package wrap_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/wrap"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const okResult = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",` +
	`"serverInfo":{"name":"fake","version":"9.9.9"}}}`

// testCore is the door every payload carries. The command is nonsense on
// purpose: these tests are about who is in the payload, and a real path would
// invite the reading that the entry is checked like the others. It is not --
// it is the process doing the checking.
var testCore = wrap.Core{ID: "atenea", Command: []string{"/nonexistent/atenea", "mcp"}}

// live returns the URL of a server that completes the handshake.
func live(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, okResult)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// dead returns the URL of a server that is not there. Started and stopped so
// the port is real and unclaimed, which is the shape of the failure this
// whole package exists for -- a declaration that was true once.
func dead(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func payload(t *testing.T, plan wrap.Plan) map[string]map[string]any {
	t.Helper()
	raw, err := plan.OpenCodePayload(testCore)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	var out struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("payload is not JSON: %v: %s", err, raw)
	}
	return out.MCP
}

// The safety property this package rests on. Atenea declining to vouch for a
// server must not be the reason the client loses one: OpenCode deep-merges
// the payload, so an absent key leaves the user's own entry alone, while an
// `enabled:false` would reach in and switch off something that might work
// perfectly well without Atenea in the picture.
func TestARefusedServerIsAbsentRatherThanDisabled(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "gone", URL: dead(t), Timeout: 2 * time.Second},
	}, nil)
	if len(plan.Refused) != 1 {
		t.Fatalf("refused = %d, want the dead server refused", len(plan.Refused))
	}
	servers := payload(t, plan)
	if entry, ok := servers["gone"]; ok {
		t.Errorf("the refused server is in the payload as %v; it must be absent", entry)
	}
}

// The other half: a server that answered is declared, and it is declared as
// the endpoint that answered rather than anything reconstructed afterwards.
func TestALiveServerIsDeclaredAtTheAddressThatAnswered(t *testing.T) {
	url := live(t)
	plan := wrap.Check(t.Context(), []config.MCPServer{{ID: "up", URL: url}}, nil)
	if len(plan.Declared) != 1 {
		t.Fatalf("declared = %d, want 1: %v", len(plan.Declared), plan.Refused)
	}
	entry, ok := payload(t, plan)["up"]
	if !ok {
		t.Fatal("the live server is missing from the payload")
	}
	if entry["type"] != "remote" || entry["url"] != url {
		t.Errorf("entry = %v, want a remote at %s", entry, url)
	}
}

// A raw backend is the one case where handing a client the address would undo
// the work: the client would reach it directly, under no allow list and no
// effect check, and the budget would be routed around by the very command
// that exists to apply it. It is checked and reported, and it stays out of
// the payload whether or not it answered.
func TestARawBackendIsHeldRatherThanHandedToTheClient(t *testing.T) {
	url := live(t)
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "pointer", URL: url},
		{
			ID: "held", URL: url, Expose: config.ExposeRaw,
			Tools: []string{"scan"}, Effects: []contract.Effect{contract.EffectRead},
		},
	}, nil)
	if len(plan.Held) != 1 || plan.Held[0].Server.ID != "held" {
		t.Fatalf("held = %v, want the raw backend", plan.Held)
	}
	if len(plan.Declared) != 1 || plan.Declared[0].Server.ID != "pointer" {
		t.Fatalf("declared = %v, want only the pointer", plan.Declared)
	}
	servers := payload(t, plan)
	if entry, ok := servers["held"]; ok {
		t.Errorf("a raw backend was handed to the client as %v: the budget is bypassable", entry)
	}
	if _, ok := servers["pointer"]; !ok {
		t.Error("the pointer stopped being declared; only raw backends are held")
	}
	// An operator diffing this report against their client's tool list has
	// to find the server they declared, not an unexplained absence.
	var report strings.Builder
	plan.Report(&report, "opencode")
	if got := report.String(); !strings.Contains(got, "held") || !strings.Contains(got, "raw.held.<tool>") {
		t.Errorf("report does not account for the held backend:\n%s", got)
	}
}

// A merge is key by key, so a key present with a zero value is not the same
// as a key absent: `"command": []` would overwrite a real command in the
// user's own config with nothing. The unused half of the shape must not be
// serialized at all.
func TestTheUnusedHalfOfAnEntryIsNotSerialized(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{{ID: "up", URL: live(t)}}, nil)
	entry := payload(t, plan)["up"]
	for _, key := range []string{"command", "environment"} {
		if _, ok := entry[key]; ok {
			t.Errorf("a remote entry carries %q; a merge would overwrite the user's value", key)
		}
	}
}

// A stdio server is declared by the command that was run, not by a name that
// happens to be on PATH somewhere else at launch time.
//
// The fake reads the request before answering, the way a server does. One
// that answers and exits immediately loses a race with the probe's own write
// and fails with a broken pipe -- correctly, since a server that closed
// stdin before being asked cannot be asked, but it is not the thing under
// test here.
//
// Its reply comes from the declared env rather than from the command line,
// so the child could not answer at all unless the environment reached it.
func TestAStdioServerIsDeclaredAsLocalWithItsCommand(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{{
		ID:      "sh",
		Command: []string{"sh", "-c", `read -r _; printf '%s\n' "$REPLY_JSON"; cat >/dev/null`},
		Env:     map[string]string{"K": "v", "REPLY_JSON": okResult},
	}}, nil)
	if len(plan.Declared) != 1 {
		t.Fatalf("declared = %d, want the stdio server: %v", len(plan.Declared), plan.Refused)
	}
	entry := payload(t, plan)["sh"]
	if entry["type"] != "local" {
		t.Errorf("type = %v, want local", entry["type"])
	}
	command, _ := entry["command"].([]any)
	if len(command) != 3 || command[0] != "sh" {
		t.Errorf("command = %v, want the declared argv", entry["command"])
	}
	env, _ := entry["environment"].(map[string]any)
	if env["K"] != "v" {
		t.Errorf("environment = %v, want the declared env", entry["environment"])
	}
}

// The reason is the only thing in the report that is not already in the
// settings file. A refusal that names the server but not why it was refused
// is the silent-warning failure this replaces, one indent deeper.
func TestARefusalCarriesItsReason(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "gone", URL: dead(t), Timeout: 2 * time.Second},
	}, nil)
	var report strings.Builder
	plan.Report(&report, "opencode")
	got := report.String()
	if !strings.Contains(got, "refused") || !strings.Contains(got, "gone") {
		t.Fatalf("report does not name the refusal:\n%s", got)
	}
	reason := plan.Refused[0].Result.Err.Error()
	if !strings.Contains(got, reason) {
		t.Errorf("report omits the reason %q:\n%s", reason, got)
	}
}

// The all-green run is where the word `declared` does the most work and gets
// the least scrutiny: "5 declared, 0 refused" reads as "everything works",
// and what was actually measured is one handshake per server. A server can
// answer initialize and have every tool fail on the first call -- that
// happened to semgrep on the machine this was built on, for days, while it
// reported healthy. So the qualification is not conditional on bad news.
func TestTheReportSaysWhatDeclaredAttestsToEvenWhenNothingFailed(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{{ID: "up", URL: live(t)}}, nil)
	if len(plan.Refused) != 0 {
		t.Fatalf("wanted an all-green plan, got refusals: %v", plan.Refused)
	}
	var report strings.Builder
	plan.Report(&report, "opencode")
	got := report.String()
	if !strings.Contains(got, "handshake") {
		t.Errorf("an all-green report does not say what it checked:\n%s", got)
	}
	if !strings.Contains(got, "not that its tools work") {
		t.Errorf("an all-green report does not say what it did NOT check:\n%s", got)
	}
}

// Two runs against one machine must produce one payload. A diff between wrap
// runs should mean the world changed, never that a map was walked twice.
func TestTheOrderDoesNotDependOnDeclarationOrder(t *testing.T) {
	a, b := live(t), live(t)
	forward := wrap.Check(t.Context(), []config.MCPServer{{ID: "aa", URL: a}, {ID: "bb", URL: b}}, nil)
	backward := wrap.Check(t.Context(), []config.MCPServer{{ID: "bb", URL: b}, {ID: "aa", URL: a}}, nil)
	if forward.Declared[0].Server.ID != "aa" || backward.Declared[0].Server.ID != "aa" {
		t.Errorf("order = %q then %q, want both sorted",
			forward.Declared[0].Server.ID, backward.Declared[0].Server.ID)
	}
	first, _ := forward.OpenCodePayload(testCore)
	second, _ := backward.OpenCodePayload(testCore)
	if first != second {
		t.Errorf("payloads differ by declaration order:\n%s\n%s", first, second)
	}
}

// Declaring nothing is a legitimate state, and it has to be said out loud:
// silence would read as "wrap ran and everything is fine" when what happened
// is that wrap had nothing to check.
//
// What it must NOT say any more is "unchanged". Until 2026-08-09 the payload
// really was empty here, because no payload ever named Atenea; now the door
// goes in whether or not a single backend was declared, and a settings file
// with no backends still has a core with eight capabilities behind it.
func TestNothingDeclaredStillHandsTheClientTheDoor(t *testing.T) {
	plan := wrap.Check(t.Context(), nil, nil)
	var report strings.Builder
	plan.Report(&report, "opencode")
	if strings.Contains(report.String(), "unchanged") {
		t.Errorf("report = %q, but the client does change: it gains atenea", report.String())
	}
	if !strings.Contains(report.String(), "atenea") {
		t.Errorf("report = %q, want it to name what the client gains", report.String())
	}
	servers := payload(t, plan)
	if len(servers) != 1 {
		t.Fatalf("payload = %v, want exactly the core", servers)
	}
	if _, ok := servers["atenea"]; !ok {
		t.Errorf("payload = %v, want the core in it", servers)
	}
}

// The defect this phase existed to fix, pinned so it cannot come back.
//
// `serena` and `codebase-memory` carry every capability on the machine this
// was measured on, and wrap put both in the payload. The client then reached
// the funnel's own backends without going through the funnel: no allow list,
// no effect check, no receipt. The command that exists to point a client at
// the core was the one handing out ways around it.
func TestABackendBehindACapabilityIsNotHandedToTheClient(t *testing.T) {
	url := live(t)
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "serena", URL: url},
		{ID: "headroom", URL: url},
	}, map[string]bool{"serena": true})

	servers := payload(t, plan)
	if _, ok := servers["serena"]; ok {
		t.Error("serena is in the payload; a capability's backend must be reached through the capability")
	}
	// The other half of the same rule: holding back everything would be a
	// different bug wearing this one's clothes. A backend nobody implements
	// against is one Atenea cannot answer for, and the client keeps it.
	if _, ok := servers["headroom"]; !ok {
		t.Error("headroom is missing; a backend no capability uses is the client's to declare")
	}
	if len(plan.Held) != 1 || plan.Held[0].Server.ID != "serena" {
		t.Errorf("held = %v, want serena held", plan.Held)
	}
}

// A held backend is still reported. Dropping it from the report would make
// "my client cannot see serena" a silent outcome, and the operator would go
// looking in the settings file for something that is working as designed.
func TestAHeldBackendIsStillNamedInTheReport(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "codebase-memory", URL: live(t)},
	}, map[string]bool{"codebase-memory": true})

	var report strings.Builder
	plan.Report(&report, "opencode")
	if !strings.Contains(report.String(), "codebase-memory") {
		t.Errorf("report = %q, want the held backend named", report.String())
	}
}

// The report is a claim about what a client can reach, so it must not name a
// surface the core does not serve. Held has two causes and only one of them
// has tools: `expose = "raw"` is re-offered as raw.<id>.<tool>; a backend held
// because capabilities run on it is not re-offered at all.
//
// Measured on this machine on 2026-08-09, against the binary as shipped: wrap
// announced raw.serena.<tool> and raw.codebase-memory.<tool>, and `atenea mcp`
// served neither -- 19 raw tools, every one of them chrome-devtools, context7
// or semgrep, the three that carry expose. The check that exists to stop a
// client believing an unverified claim was making two of its own.
func TestOnlyARawBackendIsAnnouncedAsRaw(t *testing.T) {
	url := live(t)
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "serena", URL: url},
		{ID: "context7", URL: url, Expose: config.ExposeRaw},
	}, map[string]bool{"serena": true})

	var report strings.Builder
	plan.Report(&report, "opencode")
	got := report.String()
	if strings.Contains(got, "raw.serena.<tool>") {
		t.Errorf("report announces a raw surface serena does not have:\n%s", got)
	}
	// The other half, or the fix is just a deletion: a backend that really
	// is re-offered must still say so, under the name it answers to.
	if !strings.Contains(got, "raw.context7.<tool>") {
		t.Errorf("report drops the raw surface context7 does have:\n%s", got)
	}
	// Both are held either way; the fix is about what the row says, not
	// about who goes in the payload.
	if len(plan.Held) != 2 {
		t.Errorf("held = %d entries, want both", len(plan.Held))
	}
}

// The core is not a probe result and must not be treated as one. Every other
// entry earns its place by answering a handshake; this one is the process
// running the handshakes, and a bad probe elsewhere must not cost the client
// its only door.
func TestTheCoreSurvivesAPayloadWhereEverythingElseFailed(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "gone", URL: dead(t), Timeout: 2 * time.Second},
	}, nil)

	servers := payload(t, plan)
	if _, ok := servers["gone"]; ok {
		t.Error("the dead server is in the payload")
	}
	entry, ok := servers["atenea"]
	if !ok {
		t.Fatalf("payload = %v, want the core present even when every backend failed", servers)
	}
	if entry["type"] != "local" {
		t.Errorf("core type = %v, want a local stdio process", entry["type"])
	}
}

// claudePayload parses what `claude --mcp-config` would be handed.
func claudePayload(t *testing.T, plan wrap.Plan) map[string]map[string]any {
	t.Helper()
	args, err := plan.ClaudeArgs(testCore)
	if err != nil {
		t.Fatalf("claude args: %v", err)
	}
	if len(args) != 2 || args[0] != "--mcp-config" {
		t.Fatalf("args = %q, want the flag and one payload", args)
	}
	var out struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[1]), &out); err != nil {
		t.Fatalf("payload is not JSON: %v: %s", err, args[1])
	}
	return out.MCPServers
}

// One settings block, two renderings. Claude Code splits the executable from
// its arguments where OpenCode takes a single list, and it reads an entry with
// no `type` as stdio -- so a remote server rendered without one is skipped
// with its url never read. Both halves are shape, not preference.
func TestTheClaudePayloadSplitsTheCommandAndNamesEveryType(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "remote", URL: live(t)},
	}, nil)
	servers := claudePayload(t, plan)

	core, ok := servers["atenea"]
	if !ok {
		t.Fatal("the core is missing; a payload without it hands the client no door")
	}
	if core["command"] != "/nonexistent/atenea" {
		t.Errorf("command = %v, want the executable on its own", core["command"])
	}
	if args, _ := core["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v, want the tail of the command", core["args"])
	}
	if core["type"] != "stdio" {
		t.Errorf("core type = %v, want stdio written out", core["type"])
	}
	if got := servers["remote"]["type"]; got != "http" {
		t.Errorf("remote type = %v, want http; a url with no type is skipped", got)
	}
}

// The load-bearing difference between the two ways to write a codex override.
// `-c mcp_servers={...}` replaces the table and takes every server the user
// declared in their own config.toml with it, for the length of the session.
// One override per id sets one key and leaves the rest of the table alone.
func TestACodexOverrideAddressesOneServerNotTheTable(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "remote", URL: live(t)},
	}, nil)
	args, err := plan.CodexArgs(testCore)
	if err != nil {
		t.Fatalf("codex args: %v", err)
	}
	if len(args) != 4 {
		t.Fatalf("args = %q, want one -c pair for the core and one for the server", args)
	}
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			t.Fatalf("args[%d] = %q, want -c", i, args[i])
		}
		if !strings.HasPrefix(args[i+1], "mcp_servers.") {
			t.Errorf("override %q addresses the whole table; the user's own servers go with it", args[i+1])
		}
	}
	if want := `mcp_servers.remote={url=`; !strings.HasPrefix(args[3], want) {
		t.Errorf("override = %q, want it to start %q", args[3], want)
	}
}

// codex parses the override as TOML, so a quote or a backslash in a path is
// the difference between a wrapped session and a parse error at launch. The
// core's own command is the one string in the payload that is always present.
func TestACodexOverrideEscapesWhatTOMLWouldMisread(t *testing.T) {
	core := wrap.Core{ID: "atenea", Command: []string{`/no/a"b\c`, "mcp"}}
	args, err := wrap.Plan{}.CodexArgs(core)
	if err != nil {
		t.Fatalf("codex args: %v", err)
	}
	if len(args) != 2 {
		t.Fatalf("args = %q, want the core alone", args)
	}
	if want := `command="/no/a\"b\\c"`; !strings.Contains(args[1], want) {
		t.Errorf("override = %q, want it to carry %s", args[1], want)
	}
}

// The asymmetry the package rests on, carried into both new clients: a server
// Atenea could not reach is absent, never disabled. Claude Code and codex both
// add what arrives to what they already resolve, so an absent entry leaves the
// user's own declaration exactly where it was -- and a disabled one would
// reach in and switch off something that may work perfectly without Atenea.
func TestARefusedServerIsAbsentFromEveryClient(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "gone", URL: dead(t), Timeout: 2 * time.Second},
	}, nil)

	if _, ok := claudePayload(t, plan)["gone"]; ok {
		t.Error("the refused server is in the claude payload")
	}
	args, err := plan.CodexArgs(testCore)
	if err != nil {
		t.Fatalf("codex args: %v", err)
	}
	if strings.Contains(strings.Join(args, " "), "gone") {
		t.Errorf("the refused server is in the codex overrides: %q", args)
	}
}
