package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// This package's whole job is the same translation claudecode's adapter does
// -- what the CLI is told, and what its answer becomes -- so these tests
// follow that file's own shape: a stub stands in for the binary, one turn,
// one envelope, no login and no network.

// stub stands in for the real binary: one turn, one envelope on stdout,
// regardless of what argv it was called with.
func stub(t *testing.T, stdout string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat <<'ENVELOPE'\n" + stdout + "\nENVELOPE\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return path
}

// testClient builds a Client over a stub binary, both roles configured so
// any Request.Role in a test resolves to a model name.
func testClient(t *testing.T, stdout string) *Client {
	t.Helper()
	client, err := New(Options{
		Binary:  stub(t, stdout),
		Explore: "explore-model",
		Plan:    "plan-model",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// argvClient builds a Client with no real binary at all. args never touches
// the filesystem or PATH, so the argv-shape tests below do not need a stub.
func argvClient(t *testing.T) *Client {
	t.Helper()
	client, err := New(Options{Explore: "explore-model", Plan: "plan-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func baseRequest() Request {
	return Request{
		Role:      RoleExplore,
		Prompt:    "look around",
		BudgetUSD: 0.25,
	}
}

const answered = `{"is_error":false,"subtype":"success","result":"the answer",
  "usage":{"input_tokens":120,"output_tokens":40,"cache_read_input_tokens":5,"cache_creation_input_tokens":6},
  "total_cost_usd":0.0234,"num_turns":2}`

const unpriced = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":10,"output_tokens":2}}`

const structuredAnswer = `{"is_error":false,"subtype":"success","result":"see structured_output",
  "structured_output":{"path":"cmd/main.go","line":3},
  "usage":{"input_tokens":10,"output_tokens":2},"total_cost_usd":0.001}`

const arrayStructuredAnswer = `{"is_error":false,"subtype":"success","result":"see structured_output",
  "structured_output":[1,2,3],
  "usage":{"input_tokens":10,"output_tokens":2}}`

// expiredLogin is the same shape claudecode's own ceiling_test.go measured:
// a turn that failed to authenticate still reports subtype "success".
const expiredLogin = `{"is_error":true,"subtype":"success",
  "result":"Failed to authenticate: OAuth session expired and could not be refreshed"}`

// measuredCeilingEnvelope is copied, field for field, from claudecode's own
// ceiling_test.go: a real turn that ran 54s and stopped at a $0.25 ceiling.
// No `result` and no `structured_output` at all -- the fields that matter
// are the ones not here.
const measuredCeilingEnvelope = `{
  "type": "result",
  "subtype": "error_max_budget_usd",
  "is_error": true,
  "stop_reason": "tool_use",
  "terminal_reason": "budget_exhausted",
  "errors": ["Reached maximum budget ($0.25)"],
  "num_turns": 9,
  "duration_ms": 54377,
  "total_cost_usd": 0.3540745,
  "permission_denials": [],
  "usage": {
    "input_tokens": 4,
    "output_tokens": 530,
    "cache_read_input_tokens": 12130,
    "cache_creation_input_tokens": 1238
  }
}`

// ---------------------------------------------------------------------------
// What Claude Code is told
// ---------------------------------------------------------------------------

// The measured flags. Not --safe-mode: it traveled here from claudecode by
// copying, and it disables MCP servers -- see TestATurnIsNotStartedInSafeMode
// for the measurement that cost four explorations to find.
func TestTheArgvCarriesTheMeasuredFlags(t *testing.T) {
	client := argvClient(t)
	req := baseRequest()
	req.Schema = map[string]any{"type": "object"}
	args, err := client.args(req)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--print", "--output-format json", "--json-schema",
		"--setting-sources", "--no-session-persistence", "--max-budget-usd", "--model explore-model"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the command line is missing %q:\n%s", want, joined)
		}
	}
	for _, arg := range args {
		if arg == "--session-id" || arg == "--resume" || arg == "--continue" {
			t.Fatalf("the turn reuses a far-side session through %q", arg)
		}
	}
}

// Zero is a request granting no ceiling, not a request to spend nothing --
// see Request.BudgetUSD's own doc. The CLI has no separate "unlimited"
// spelling, so omitting the flag is the only one, and a positive figure has
// to still reach the command line unchanged.
func TestABudgetOfZeroOmitsTheCeilingFlag(t *testing.T) {
	client := argvClient(t)

	req := baseRequest()
	req.BudgetUSD = 0
	args, err := client.args(req)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	if strings.Contains(strings.Join(args, " "), "--max-budget-usd") {
		t.Errorf("a request granting no ceiling still carried --max-budget-usd: %v", args)
	}

	req.BudgetUSD = 0.1
	args, err = client.args(req)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), "--max-budget-usd 0.1") {
		t.Errorf("a granted ceiling did not reach the command line: %v", args)
	}
}

// One Client serves both roles, so which model reaches --model has to follow
// Request.Role rather than anything fixed on the Client at construction.
func TestTheModelFlagNamesTheRolesOwnModel(t *testing.T) {
	client := argvClient(t)
	for _, tc := range []struct {
		role Role
		want string
	}{
		{RoleExplore, "explore-model"},
		{RolePlan, "plan-model"},
	} {
		req := baseRequest()
		req.Role = tc.role
		args, err := client.args(req)
		if err != nil {
			t.Fatalf("args(%s): %v", tc.role, err)
		}
		if !strings.Contains(strings.Join(args, " "), "--model "+tc.want) {
			t.Errorf("role %s: args = %v, want --model %s", tc.role, args, tc.want)
		}
	}
}

// A role built with no model name for it is discovered here, not at New: a
// Client missing one role's model is only a problem for whichever Turn asks
// for that role.
func TestARoleWithNoModelConfiguredIsRefused(t *testing.T) {
	client, err := New(Options{Explore: "explore-model"}) // Plan left unconfigured
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := baseRequest()
	req.Role = RolePlan
	if _, err := client.args(req); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// A closed set of two: a caller cannot ask for a role settings never fixed a
// model for.
func TestAnUnknownRoleIsRefused(t *testing.T) {
	client := testClient(t, answered)
	req := baseRequest()
	req.Role = Role("summarize")
	_, err := client.Turn(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// --mcp-config is the one flag the CLI declares variadic, so anything placed
// after it is read as one more config source rather than its own flag.
// --strict-mcp-config has to land right after it, and both have to disappear
// entirely when the caller granted no tools.
func TestToolsAddsAStrictMcpConfigLast(t *testing.T) {
	client := argvClient(t)
	req := baseRequest()
	req.Tools = `{"mcpServers":{"atenea":{"command":"atenea","args":["mcp"]}}}`
	args, err := client.args(req)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	if len(args) < 2 || args[len(args)-1] != "--strict-mcp-config" || args[len(args)-2] != req.Tools {
		t.Fatalf("--mcp-config/--strict-mcp-config did not land last: %v", args)
	}

	req.Tools = ""
	args, err = client.args(req)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	if strings.Contains(strings.Join(args, " "), "--mcp-config") {
		t.Errorf("no tools were granted, but --mcp-config still appeared: %v", args)
	}
}

// ---------------------------------------------------------------------------
// What comes back
// ---------------------------------------------------------------------------

// A clean answer reports the usage block's own counts, unchanged, and the
// text answer even though this request asked for no structure.
func TestASuccessfulTurnReportsTokensAndText(t *testing.T) {
	client := testClient(t, answered)
	answer, err := client.Turn(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Text != "the answer" {
		t.Errorf("Text = %q, want %q", answer.Text, "the answer")
	}
	if answer.Spent.InputTokens != 120 || answer.Spent.OutputTokens != 40 ||
		answer.Spent.CacheReadTokens != 5 || answer.Spent.CacheWriteTokens != 6 {
		t.Errorf("Spent = %+v, want the usage block's own counts", answer.Spent)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.0234 {
		t.Errorf("USD = %v, want 0.0234", answer.Spent.USD)
	}
	if answer.Spent.PricedBy == "" {
		t.Error("a priced turn left PricedBy empty")
	}
}

// Measured, in claudecode: a turn that failed to authenticate still reports
// subtype "success", so subtype can never be trusted before is_error.
func TestAnErrorEnvelopeIsNeverTrustedOnSubtypeAlone(t *testing.T) {
	client := testClient(t, expiredLogin)
	_, err := client.Turn(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("a turn that failed to authenticate was read as having answered")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable: subtype said success, is_error said otherwise", got)
	}
}

// A turn stopped at its own --max-budget-usd ceiling did not deliver, which
// is FailureUnavailable here -- Request.BudgetUSD is this call's own
// ceiling with no larger pool behind it, unlike claudecode's shared grant.
// It still occupied the machine and spent real tokens, and that has to
// travel with the failure, not get dropped because the turn did not answer.
func TestABudgetExhaustedTurnIsUnavailableButStillCharged(t *testing.T) {
	client := testClient(t, measuredCeilingEnvelope)
	answer, err := client.Turn(context.Background(), baseRequest())
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
	const wantTokens = 4 + 530 + 12130 + 1238
	if got := answer.Spent.Tokens(); got != wantTokens {
		t.Errorf("tokens = %d, want %d -- a turn stopped at its ceiling still occupied the machine", got, wantTokens)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.3540745 {
		t.Errorf("USD = %v, want 0.3540745", answer.Spent.USD)
	}
}

// total_cost_usd absent and total_cost_usd present-but-zero are different
// facts, and only a nil check tells them apart. A turn this package cannot
// attribute a price to must read as unpriced, not as a free one.
func TestAnUnpricedTurnLeavesUSDNilNotZero(t *testing.T) {
	client := testClient(t, unpriced)
	answer, err := client.Turn(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Spent.USD != nil {
		t.Errorf("USD = %v, want nil -- total_cost_usd was never in the envelope", *answer.Spent.USD)
	}
	if answer.Spent.PricedBy != "" {
		t.Errorf("PricedBy = %q, want empty when USD is nil", answer.Spent.PricedBy)
	}
	if !answer.Spent.Measured() {
		t.Error("tokens were reported, so this charge should still read as measured")
	}
}

// Structured is raw JSON, not a decoded map: a caller decodes it into
// whatever shape it actually expects, and this only has to prove the bytes
// that crossed are the bytes the model answered with.
func TestStructuredOutputTravelsAsRawJSON(t *testing.T) {
	client := testClient(t, structuredAnswer)
	req := baseRequest()
	req.Schema = map[string]any{"type": "object"}

	answer, err := client.Turn(context.Background(), req)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(answer.Structured, &decoded); err != nil {
		t.Fatalf("Structured did not round-trip as JSON: %v", err)
	}
	if decoded["path"] != "cmd/main.go" {
		t.Errorf("decoded = %+v, want path = cmd/main.go", decoded)
	}
}

// A Schema was asked for and the envelope carries no structured_output at
// all -- the model answered, just not in the shape it was told to.
func TestATurnWithoutTheAskedStructureIsRefused(t *testing.T) {
	client := testClient(t, answered)
	req := baseRequest()
	req.Schema = map[string]any{"type": "object"}
	_, err := client.Turn(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// structured_output can be syntactically valid JSON and still be the wrong
// shape: a schema asks for an object, and an array is not one.
func TestNonObjectStructuredOutputIsRefused(t *testing.T) {
	client := testClient(t, arrayStructuredAnswer)
	req := baseRequest()
	req.Schema = map[string]any{"type": "object"}
	_, err := client.Turn(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input: structured_output was a JSON array, not an object", got)
	}
}

// ---------------------------------------------------------------------------
// Request.Validate
// ---------------------------------------------------------------------------

func TestAnEmptyPromptIsRefused(t *testing.T) {
	client := testClient(t, answered)
	req := baseRequest()
	req.Prompt = "   "
	_, err := client.Turn(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// Negative is nonsense no caller's own accounting produces on purpose; zero
// is deliberately not refused here -- see TestABudgetOfZeroOmitsTheCeilingFlag.
func TestANegativeBudgetIsRefused(t *testing.T) {
	client := testClient(t, answered)
	req := baseRequest()
	req.BudgetUSD = -0.01
	_, err := client.Turn(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

func TestNewRefusesANegativeTimeout(t *testing.T) {
	if _, err := New(Options{Timeout: -time.Second}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// ---------------------------------------------------------------------------
// AteneaTools
// ---------------------------------------------------------------------------

// There is no live-service fixture to stub here -- the socket is either
// answering on this machine or it is not -- so this checks consistency with
// a direct dial instead of a fixed outcome, which is what keeps it from
// being flaky on a machine that happens to be running the service.
func TestAteneaToolsMatchesWhetherTheSocketAnswers(t *testing.T) {
	reachable := false
	if conn, err := net.Dial("unix", core.SocketPath()); err == nil {
		reachable = true
		_ = conn.Close()
	}

	tools, err := AteneaTools()
	if reachable {
		if err != nil {
			t.Fatalf("AteneaTools: %v, but the socket answered", err)
		}
		// An absolute path, not a bare name: the spawned CLI has its own
		// PATH, and an install outside it would get a model with no tools.
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("os.Executable: %v", err)
		}
		if !strings.Contains(tools, `"command":"`+self+`"`) {
			t.Errorf("tools = %q, want it to name this binary by path (%s)", tools, self)
		}
		return
	}
	if err == nil {
		t.Fatal("AteneaTools succeeded with no service listening")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable", got)
	}
	if !strings.Contains(err.Error(), "atenea service is listening") {
		t.Errorf("message = %q, want it to name the missing service", err.Error())
	}
}

// What a turn may call is the whole point of handing it capabilities: an
// exploration that can reach for the tool the capability replaces will.

// argvOf builds the command line for one request, which is what the CLI is
// actually told -- the only place the allow-list is observable.
func argvOf(t *testing.T, req Request) []string {
	t.Helper()
	client, err := New(Options{Binary: "/nonexistent/claude", Explore: "m", Plan: "m"})
	if err != nil {
		t.Fatalf("building the client: %v", err)
	}
	argv, err := client.args(req)
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	return argv
}

func flagValue(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// Measured 2026-08-14: three explorations, $1.87, zero capability dispatches.
// The turn had Atenea's tools and the CLI's built-ins at once and used only
// the built-ins. The allow-list is what makes the offer real.
func TestATurnIsToldExactlyWhichToolsItMayCall(t *testing.T) {
	tools, err := AteneaTools()
	if err != nil {
		t.Skipf("no atenea tools to hand over here: %v", err)
	}
	argv := argvOf(t, Request{
		Role: RoleExplore, Prompt: "look", Tools: tools,
		Builtins: []string{"Read", "Glob"},
	})

	for _, flag := range []string{"--tools", "--allowedTools"} {
		list, ok := flagValue(argv, flag)
		if !ok {
			t.Fatalf("%s was not passed: the turn keeps the whole built-in set", flag)
		}
		for _, want := range []string{"mcp__atenea", "Read", "Glob"} {
			if !strings.Contains(list, want) {
				t.Errorf("%s = %q, want %s in it", flag, list, want)
			}
		}
		// Grep is code.search, and Bash makes read-only a hope.
		for _, unwanted := range []string{"Grep", "Bash", "Write", "Edit"} {
			if strings.Contains(list, unwanted) {
				t.Errorf("%s = %q, want %s absent", flag, list, unwanted)
			}
		}
	}
}

// The allow-list addresses the server by the name the config registers it
// under. Rename one alone and the turn is handed a server it may not call --
// which reads exactly like a model choosing not to use it.
func TestTheAllowListNamesTheServerTheConfigRegisters(t *testing.T) {
	tools, err := AteneaTools()
	if err != nil {
		t.Skipf("no atenea tools to hand over here: %v", err)
	}
	var cfg struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(tools), &cfg); err != nil {
		t.Fatalf("parsing the mcp config: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %v, want exactly one", cfg.Servers)
	}
	for name := range cfg.Servers {
		list, _ := flagValue(argvOf(t, Request{Role: RoleExplore, Prompt: "x", Tools: tools}), "--allowedTools")
		if want := "mcp__" + name; !strings.Contains(list, want) {
			t.Errorf("allow-list = %q, want the registered server %q", list, want)
		}
	}
}

// A turn with nothing to call gets no list rather than an empty one: the CLI
// reads an empty --tools as "nothing", and a turn that can call nothing
// should not have been started.
func TestATurnWithNothingToCallGetsNoList(t *testing.T) {
	argv := argvOf(t, Request{Role: RolePlan, Prompt: "plan from what you were given"})
	if list, ok := flagValue(argv, "--tools"); ok {
		t.Errorf("--tools = %q, want the flag absent", list)
	}
}

// "refused 1 action(s)" cost a debugging session: the run had spent $1.26 and
// nothing said whether the model reached for a shell, a write, or a tool
// nobody granted.
func TestARefusalNamesWhatWasRefused(t *testing.T) {
	envelope := `{"is_error":false,"result":"",` +
		`"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}],` +
		`"usage":{"input_tokens":10,"output_tokens":1}}`
	client := testClient(t, envelope)

	_, err := client.Turn(context.Background(), Request{Role: RoleExplore, Prompt: "look"})
	if err == nil {
		t.Fatal("a refused turn answered cleanly")
	}
	if got := contract.MessageOf(err); !strings.Contains(got, "Bash") {
		t.Errorf("message = %q, want the refused tool named", got)
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("kind = %v, want permission_denied", got)
	}
}

// A denial the provider did not name is still a denial. Dropping it would
// turn a refused turn into a silent one.
func TestAnUnnamedRefusalIsStillReported(t *testing.T) {
	envelope := `{"is_error":false,"result":"","permission_denials":[{}],` +
		`"usage":{"input_tokens":10,"output_tokens":1}}`
	client := testClient(t, envelope)

	_, err := client.Turn(context.Background(), Request{Role: RoleExplore, Prompt: "look"})
	if err == nil {
		t.Fatal("a refused turn answered cleanly")
	}
	if got := contract.MessageOf(err); !strings.Contains(got, "unnamed") {
		t.Errorf("message = %q, want the unnamed denial reported", got)
	}
}

// --safe-mode disables MCP servers -- the CLI's own --help says so, and it was
// measured on 2026-08-14 after four explorations costing $4.43 dispatched zero
// capabilities. A turn handed --mcp-config and --safe-mode together is a turn
// told about tools it cannot see.
func TestATurnIsNotStartedInSafeMode(t *testing.T) {
	tools, err := AteneaTools()
	if err != nil {
		t.Skipf("no atenea tools to hand over here: %v", err)
	}
	argv := argvOf(t, Request{Role: RoleExplore, Prompt: "look", Tools: tools})

	if slices.Contains(argv, "--safe-mode") {
		t.Error("--safe-mode is passed: the turn gets no atenea tools at all")
	}
	// What replaces it, and must stay: ambient settings and skills off.
	if v, ok := flagValue(argv, "--setting-sources"); !ok || v != "" {
		t.Errorf("--setting-sources = %q (present=%v), want it passed empty", v, ok)
	}
	if !slices.Contains(argv, "--disable-slash-commands") {
		t.Error("skills are not suppressed")
	}
	// And the server list stays closed, which is what makes dropping
	// --safe-mode survivable.
	if !slices.Contains(argv, "--strict-mcp-config") {
		t.Error("--strict-mcp-config is missing: the turn would see this machine's other servers")
	}
}

// A prompt that cannot be read cannot be measured: this records the string on
// the way out, so a reader is never comparing against a stub's copy of it.
func TestThePromptLogRecordsWhatWasSent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(PromptLogEnv, dir)

	client := testClient(t, `{"result":"ok","is_error":false,"subtype":"success"}`)
	req := baseRequest()
	req.Prompt = "the exact words the planner reads"
	if _, err := client.Turn(t.Context(), req); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	entries, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one recorded turn, got %v (%v)", entries, err)
	}
	body, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	for _, want := range []string{"the exact words the planner reads", "--print"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the record is missing %q:\n%s", want, body)
		}
	}
}

// Unset is the normal case: no directory, no recording, and no failure.
func TestNoPromptLogWithoutADirectory(t *testing.T) {
	t.Setenv(PromptLogEnv, "")
	client := testClient(t, `{"result":"ok","is_error":false,"subtype":"success"}`)
	if _, err := client.Turn(t.Context(), baseRequest()); err != nil {
		t.Fatalf("Turn: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The reserved answer
// ---------------------------------------------------------------------------

// Everything below drives the other path through Turn, where one turn is a
// conversation instead of one envelope. The stub above cannot stand in for
// that: it answers whatever it is asked once and exits, and what has to be
// observable here is the alternation -- which pass answered, what was said
// back to it, and what happened when the process died mid-conversation.

// fakeCLI stands in for the binary on the reserved-answer path. It answers the
// Nth message it is given with the Nth scripted pass, records both sides, and
// once the passes run out it dies the way the script says -- which is how a
// turn killed at its hard ceiling ends.
//
// Same shape as stub: a shell script in t.TempDir(), no login and no network.
// This one just has two sides to record instead of one.
type fakeCLI struct {
	binary string
	dir    string
}

// fakeScript is the fake's whole behavior. It follows the flags it was given
// the way the real CLI does: with no --input-format there is no conversation
// to hold open, so it prints one envelope and leaves.
//
// Otherwise it models a turn the way the real CLI runs one: a message starts
// it, the assistant events are what it prints while it is still working, and
// the result event is what ends it. So the steps of one turn are emitted as a
// group, and the next message is not read until that group's result event has
// gone out -- which is what makes an assistant event arrive with no result
// event anywhere behind it, the exact shape the mid-turn trigger exists for.
//
// The stderr text is single-quoted into the script, so a fixture that needs an
// apostrophe needs a different quoting than this.
const fakeScript = `#!/bin/sh
dir="%s"
printf '%%s\n' "$@" > "$dir/argv"
case " $* " in
  *" --input-format "*) ;;
  *) cat "$dir/step1"; exit 0 ;;
esac
n=0
while IFS= read -r line; do
  printf '%%s\n' "$line" >> "$dir/stdin"
  while :; do
    n=$((n + 1))
    step="$dir/step$n"
    if [ ! -f "$step" ]; then
      # A fake told to hang stops answering without leaving, which is what a
      # turn that runs into its own timeout looks like from the Go side.
      [ -f "$dir/hang" ] && sleep 30
      printf '%%s\n' '%s' >&2
      exit %d
    fi
    body=$(cat "$step")
    printf '%%s\n' "$body"
    case "$body" in *'"type":"result"'*) break ;; esac
  done
done
`

// scriptCLI writes the fake and the event lines it will print, in order. A
// line carrying a result event ends a turn and the fake reads the next message
// before going on; anything else is printed straight away. said and exit are
// how it ends once the steps run out.
func scriptCLI(t *testing.T, steps []string, said string, exit int) fakeCLI {
	t.Helper()
	dir := t.TempDir()
	for i, step := range steps {
		name := filepath.Join(dir, fmt.Sprintf("step%d", i+1))
		if err := os.WriteFile(name, []byte(step+"\n"), 0o600); err != nil {
			t.Fatalf("writing step %d: %v", i+1, err)
		}
	}
	binary := filepath.Join(dir, "claude")
	script := fmt.Sprintf(fakeScript, dir, said, exit)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake: %v", err)
	}
	return fakeCLI{binary: binary, dir: dir}
}

// hangs makes the fake stop answering without leaving, once its steps run
// out. What the turn hits then is its own timeout, with whatever it has.
func (f fakeCLI) hangs(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "hang"), nil, 0o600); err != nil {
		t.Fatalf("telling the fake to hang: %v", err)
	}
}

// client points a Client at the fake, both roles configured, the same way
// testClient does for the single-envelope stub.
func (f fakeCLI) client(t *testing.T) *Client {
	t.Helper()
	client, err := New(Options{Binary: f.binary, Explore: "explore-model", Plan: "plan-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// messages is the text of everything the fake was told, in order.
//
// Every line is checked against the shape the CLI validates before its text is
// returned: read out of the shipped binary, a line whose message.role is not
// "user" comes back as "Error: Expected message role 'user'", and one carrying
// an unknown type is dropped silently -- which from the Go side would look
// exactly like a model that stopped answering.
func (f fakeCLI) messages(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.dir, "stdin"))
	if err != nil || strings.TrimSpace(string(body)) == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var one struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &one); err != nil {
			t.Fatalf("the cli was sent a line that is not JSON: %v\n%s", err, line)
		}
		if one.Type != "user" || one.Message.Role != "user" ||
			len(one.Message.Content) != 1 || one.Message.Content[0].Type != "text" {
			t.Fatalf("the cli was sent a message it would refuse or ignore: %s", line)
		}
		out = append(out, one.Message.Content[0].Text)
	}
	return out
}

// argv is the command line the fake was actually started with.
func (f fakeCLI) argv(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.dir, "argv"))
	if err != nil {
		t.Fatalf("the fake recorded no argv, so it was never started: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}

// passEvent renders one scripted result event: what a stream-json turn prints
// when a pass ends. One line, always -- the reader on the other side splits on
// newlines, exactly as the CLI's own stdout guard promises it may.
//
// The cost and the tokens are cumulative across passes in a real stream, so
// the fixtures below grow them: they are the running total, not the pass's own
// share.
func passEvent(cost float64, tokens int, structured string) string {
	return fmt.Sprintf(`{"type":"result","is_error":false,"subtype":"success","result":"a pass",`+
		`"structured_output":%s,"usage":{"input_tokens":%d,"output_tokens":10},"total_cost_usd":%v}`,
		structured, tokens, cost)
}

// assistantEvent renders one assistant event: the only line that reports what
// a turn has spent while it is still running.
//
// The id is what makes the reading count once. Measured on the live CLI, one
// message's content blocks arrive as separate events restating identical usage,
// so two events sharing an id are one request seen twice -- which is why every
// fixture here names one.
func assistantEvent(id string, input, cacheWrite, cacheRead, output int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"role":"assistant",`+
		`"content":[{"type":"text","text":"reading"}],"usage":{"input_tokens":%d,`+
		`"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`,
		id, input, cacheWrite, cacheRead, output)
}

// readAllowance is the token allowance the tests below hold answers back with.
// Its exact value matters to the fixtures, which are written to cross it at a
// named point and not before.
const readAllowance = 900

// reservedRequest is a turn that holds an answer back: a token allowance, and
// a schema for the model to put the answer in.
func reservedRequest() Request {
	req := baseRequest()
	req.Schema = map[string]any{"type": "object"}
	req.BudgetUSD = 0.5
	req.ReadTokens = readAllowance
	return req
}

// nudges counts how many of the messages sent are the one that replaces the
// kill. Exactly one, ever: a second would pay a whole pass to repeat itself.
func nudges(messages []string) int {
	count := 0
	for _, m := range messages {
		if strings.Contains(m, "Stop reading") {
			count++
		}
	}
	return count
}

// A pass that says it covered the whole objective ends the turn on the spot:
// there is nothing left to buy, so nothing is said back to it.
func TestAPassClaimingTheWholeObjectiveEndsTheTurn(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"all of it","completeness":1,"stopped_at":""}`),
	}, "", 0)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Passes != 1 {
		t.Errorf("Passes = %d, want 1", answer.Passes)
	}
	if answer.Completeness == nil || *answer.Completeness != 1 {
		t.Errorf("Completeness = %v, want 1", answer.Completeness)
	}
	if answer.StoppedAt != "" {
		t.Errorf("StoppedAt = %q, want empty on a whole answer", answer.StoppedAt)
	}
	messages := fake.messages(t)
	if len(messages) != 1 {
		t.Fatalf("the cli was told %d things, want only the prompt: %q", len(messages), messages)
	}
	if !strings.Contains(messages[0], "Work in passes") {
		t.Errorf("the prompt went in without the pass protocol:\n%s", messages[0])
	}
	if nudges(messages) != 0 {
		t.Error("a turn that answered whole was still told to stop reading")
	}
}

// The boundary half of the trigger, on its own: this stream carries no
// assistant events at all, so the only reading of what the turn has spent is
// the cumulative usage on each result event. Three cheap passes, the fourth
// crosses the allowance, and the answer comes from the pass after the one
// nudge -- not from a kill.
func TestTheReadAllowanceBuysTheAnswerWithOneNudge(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":0.1,"stopped_at":"most of it"}`),
		passEvent(0.04, 200, `{"findings":"ab","completeness":0.3,"stopped_at":"much of it"}`),
		passEvent(0.06, 300, `{"findings":"abc","completeness":0.5,"stopped_at":"half of it"}`),
		// 1000 input + 10 output weighs 1050, which is past the allowance.
		passEvent(0.12, 1000, `{"findings":"abcd","completeness":0.7,"stopped_at":"some of it"}`),
		passEvent(0.14, 1100, `{"findings":"abcde","completeness":0.8,"stopped_at":"the last file"}`),
	}, "", 0)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	messages := fake.messages(t)
	if len(messages) != 5 {
		t.Fatalf("the cli was told %d things, want 5 -- a prompt, three continues, one finalize: %q",
			len(messages), messages)
	}
	if got := nudges(messages); got != 1 {
		t.Fatalf("finalize messages = %d, want exactly 1: %q", got, messages)
	}
	if !strings.Contains(messages[4], "Stop reading") {
		t.Errorf("the finalize did not land on the pass that crossed the allowance: %q", messages)
	}
	// The answer is the one after the nudge, not the best of the cheap ones.
	var decoded struct {
		Findings string `json:"findings"`
	}
	if err := json.Unmarshal(answer.Structured, &decoded); err != nil {
		t.Fatalf("Structured did not round-trip: %v", err)
	}
	if decoded.Findings != "abcde" {
		t.Errorf("findings = %q, want the pass that answered after the nudge", decoded.Findings)
	}
	if answer.Passes != 5 {
		t.Errorf("Passes = %d, want 5", answer.Passes)
	}
	if answer.Completeness == nil || *answer.Completeness != 0.8 {
		t.Errorf("Completeness = %v, want 0.8", answer.Completeness)
	}
	if answer.StoppedAt != "the last file" {
		t.Errorf("StoppedAt = %q, want the last pass's own remainder", answer.StoppedAt)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.14 {
		t.Errorf("USD = %v, want 0.14 -- the cumulative total, not one pass's share", answer.Spent.USD)
	}
	if answer.Spent.InputTokens != 1100 {
		t.Errorf("input tokens = %d, want 1100: the last event's usage is the whole turn's",
			answer.Spent.InputTokens)
	}
}

// THE CENTRAL GUARANTEE. Measured 2026-08-14: 12 of 12 steps died at their
// ceiling with result_len 0, and the $3.78 already spent bought nothing. A
// death after a pass answered now costs the passes that had not happened yet
// and nothing else -- no error, and the receipt still travels.
func TestADeathAtTheCeilingNoLongerCostsTheAnswer(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":0.2,"stopped_at":"most of it"}`),
		passEvent(0.05, 200, `{"findings":"ab","completeness":0.4,"stopped_at":"the rest of it"}`),
	}, "Reached maximum budget ($0.50)", 1)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("a death after two answered passes was still reported as a failure: %v", err)
	}
	if answer.Passes != 2 {
		t.Fatalf("Passes = %d, want 2", answer.Passes)
	}
	var decoded struct {
		Findings string `json:"findings"`
	}
	if err := json.Unmarshal(answer.Structured, &decoded); err != nil {
		t.Fatalf("Structured did not round-trip: %v", err)
	}
	if decoded.Findings != "ab" {
		t.Errorf("findings = %q, want the last pass that answered", decoded.Findings)
	}
	if answer.Completeness == nil || *answer.Completeness != 0.4 {
		t.Errorf("Completeness = %v, want 0.4 -- a partial answer is still an answer", answer.Completeness)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.05 {
		t.Errorf("USD = %v, want 0.05: a turn that died still spent what it spent", answer.Spent.USD)
	}
	if !answer.Spent.Measured() {
		t.Error("the charge reads as unmeasured, so the death took the receipt with it")
	}
}

// The one death the guarantee cannot cover: nobody obtained an answer, so
// there is nothing to hand back and the failure is the whole story -- sorted
// exactly as the single-shot path sorts the same ending.
func TestADeathWithNoPassIsStillAFailure(t *testing.T) {
	fake := scriptCLI(t, nil, "Reached maximum budget ($0.50)", 1)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
	if !strings.Contains(contract.MessageOf(err), "spending ceiling") {
		t.Errorf("message = %q, want the ceiling named", contract.MessageOf(err))
	}
	if answer.Passes != 0 {
		t.Errorf("Passes = %d, want 0 -- nobody answered", answer.Passes)
	}
	if answer.Completeness != nil {
		t.Errorf("Completeness = %v, want nil: there is no answer to have covered anything",
			*answer.Completeness)
	}
}

// A read allowance of zero is the feature off, and off means the turn the CLI
// runs natively: one shot, the prompt as an argument, no stream-json anywhere.
func TestAReadAllowanceOfZeroIsTheSingleShotTurn(t *testing.T) {
	fake := scriptCLI(t, []string{
		`{"is_error":false,"subtype":"success","result":"one shot",` +
			`"structured_output":{"findings":"a"},"usage":{"input_tokens":10,"output_tokens":2}}`,
	}, "", 0)
	req := reservedRequest()
	req.ReadTokens = 0

	answer, err := fake.client(t).Turn(t.Context(), req)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	argv := fake.argv(t)
	for _, unwanted := range []string{"--input-format", "stream-json", "--verbose"} {
		if slices.Contains(argv, unwanted) {
			t.Errorf("a single-shot turn was started with %s: %q", unwanted, argv)
		}
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--output-format json") {
		t.Errorf("the old output format did not survive: %q", argv)
	}
	if !slices.Contains(argv, req.Prompt) {
		t.Errorf("the prompt did not travel as an argument: %q", argv)
	}
	if msgs := fake.messages(t); len(msgs) != 0 {
		t.Errorf("a single-shot turn was sent %d messages on stdin: %q", len(msgs), msgs)
	}
	if answer.Passes != 1 {
		t.Errorf("Passes = %d, want 1 -- one shot is one pass", answer.Passes)
	}
	if answer.Completeness != nil {
		t.Errorf("Completeness = %v, want nil: a single-shot turn claims nothing", *answer.Completeness)
	}
}

// A schema-less turn has nowhere to put a completeness, so it is never held
// open however much allowance it was granted.
func TestASchemalessTurnIsNeverHeldOpen(t *testing.T) {
	fake := scriptCLI(t, []string{`{"is_error":false,"subtype":"success","result":"free text"}`}, "", 0)
	req := reservedRequest()
	req.Schema = nil

	answer, err := fake.client(t).Turn(t.Context(), req)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Text != "free text" {
		t.Errorf("Text = %q, want the envelope's own result", answer.Text)
	}
	if slices.Contains(fake.argv(t), "--input-format") {
		t.Errorf("a schema-less turn was held open: %q", fake.argv(t))
	}
}

// The answer and the receipt come from different passes when the turn ends on
// an event that carries no answer -- which is exactly what a real ceiling
// death looks like: is_error, a terminal_reason, no result and no structure,
// and the full cumulative cost.
func TestTheReceiptIsTheLastEventEvenWhenTheAnswerIsNot(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":0.2,"stopped_at":"most of it"}`),
		passEvent(0.05, 200, `{"findings":"ab","completeness":0.4,"stopped_at":"the rest"}`),
		`{"type":"result","subtype":"error_max_budget_usd","is_error":true,` +
			`"terminal_reason":"budget_exhausted","errors":["Reached maximum budget ($0.50)"],` +
			`"usage":{"input_tokens":900,"output_tokens":10},"total_cost_usd":0.55}`,
	}, "Reached maximum budget ($0.50)", 1)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("the ceiling event was read as a failure even though two passes answered: %v", err)
	}
	if answer.Passes != 2 {
		t.Errorf("Passes = %d, want 2 -- the ceiling event answered nothing", answer.Passes)
	}
	if answer.StoppedAt != "the rest" {
		t.Errorf("StoppedAt = %q, want the last pass that answered", answer.StoppedAt)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.55 {
		t.Errorf("USD = %v, want 0.55 -- what the turn spent, including the pass that died",
			answer.Spent.USD)
	}
}

// A model that never converges is ended by the cap, and ended with an answer:
// the last pass it gets is a finalize pass, like every other stopping
// condition here. These passes are unpriced, so the allowance never fires --
// the cap is the only thing that can end this turn.
func TestTheCapEndsATurnThatNeverConverges(t *testing.T) {
	var passes []string
	for i := range maxPasses + 1 {
		passes = append(passes, fmt.Sprintf(
			`{"type":"result","is_error":false,"subtype":"success","result":"a pass",`+
				`"structured_output":{"findings":"%d","completeness":0.1,"stopped_at":"nearly all"},`+
				`"usage":{"input_tokens":%d,"output_tokens":10}}`, i, 100*(i+1)))
	}
	fake := scriptCLI(t, passes, "", 0)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Passes != maxPasses {
		t.Errorf("Passes = %d, want %d: the cap is what ends an unpriced turn", answer.Passes, maxPasses)
	}
	messages := fake.messages(t)
	if len(messages) != maxPasses {
		t.Errorf("the cli was told %d things, want %d", len(messages), maxPasses)
	}
	if got := nudges(messages); got != 1 {
		t.Errorf("finalize messages = %d, want exactly 1", got)
	}
	if !strings.Contains(messages[len(messages)-1], "Stop reading") {
		t.Errorf("the last thing said was not the finalize: %q", messages)
	}
}

// A partial answer that names nothing it missed would build a contract.Report
// that Report's own validation refuses, and what that would cost is the
// answer. So it is named badly rather than not at all.
func TestAPartialAnswerAlwaysNamesWhatItMissed(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":0.5}`),
	}, "", 1)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Completeness == nil || *answer.Completeness != 0.5 {
		t.Fatalf("Completeness = %v, want 0.5", answer.Completeness)
	}
	if strings.TrimSpace(answer.StoppedAt) == "" {
		t.Error("a partial answer came back claiming less than the whole and naming nothing")
	}
}

// At or below zero is not a measurement of an answer that exists, and
// repairing it into range would report coverage nobody has. It reads as
// unclaimed, which the planner refuses rather than reads as whole.
func TestACompletenessAtOrBelowZeroIsUnclaimed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		structure string
	}{
		{"zero", `{"findings":"a","completeness":0,"stopped_at":"everything"}`},
		{"negative", `{"findings":"a","completeness":-1,"stopped_at":"everything"}`},
		{"absent", `{"findings":"a"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Nothing claims completeness 1 here, so the turn only ends when
			// the fake runs out of passes.
			fake := scriptCLI(t, []string{passEvent(0.02, 100, tc.structure)}, "", 0)
			answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
			if err != nil {
				t.Fatalf("Turn: %v", err)
			}
			if answer.Completeness != nil {
				t.Errorf("Completeness = %v, want nil", *answer.Completeness)
			}
			if answer.Passes != 1 {
				t.Errorf("Passes = %d, want 1 -- the pass answered, it just measured nothing",
					answer.Passes)
			}
		})
	}
}

// An over-claim is the one out-of-range figure that says something: above 1
// means whole, which is exactly what complete() ends a turn on. It is clamped
// rather than dropped, because dropping it would hand the planner an absence
// and get the turn refused for saying nothing when it said too much.
func TestAnOverClaimIsClampedToWhole(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":1.7,"stopped_at":""}`),
	}, "", 0)
	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Completeness == nil || *answer.Completeness != 1 {
		t.Fatalf("Completeness = %v, want 1", answer.Completeness)
	}
	if answer.StoppedAt != "" {
		t.Errorf("StoppedAt = %q, want empty: a whole answer reached everything", answer.StoppedAt)
	}
}

// The allowance and the ceiling are in different units, and nothing here
// compares them: turning tokens into dollars needs a price, and a price is
// what this package refuses to assume. So an allowance that looks enormous
// beside the ceiling is still a valid request -- whether it fires before the
// CLI's own ceiling arrives is the caller's arithmetic, not this one's.
func TestTheAllowanceIsNotComparedWithTheCeiling(t *testing.T) {
	req := baseRequest()
	req.BudgetUSD = 0.25
	req.ReadTokens = 5_000_000
	if err := req.Validate(); err != nil {
		t.Fatalf("a large token allowance under a small dollar ceiling was refused: %v", err)
	}

	req.BudgetUSD = 0
	if err := req.Validate(); err != nil {
		t.Fatalf("an allowance under an unbounded ceiling was refused: %v", err)
	}
}

func TestANegativeReadAllowanceIsRefused(t *testing.T) {
	req := baseRequest()
	req.ReadTokens = -1
	if got := contract.KindOf(req.Validate()); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// The turn timeout is a ceiling on reading now, not a reason to throw away
// what was read. Measured 2026-08-14, before this path existed: exploration
// of this repository was cut at 90s having answered nothing, and reported no
// charge while having spent real money.
func TestATimeoutAfterAPassKeepsTheAnswer(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":0.2,"stopped_at":"the rest"}`),
	}, "", 0)
	fake.hangs(t)
	req := reservedRequest()
	req.Timeout = 300 * time.Millisecond

	started := time.Now()
	answer, err := fake.client(t).Turn(t.Context(), req)
	if err != nil {
		t.Fatalf("a turn that answered once and then ran out of time was reported as a failure: %v", err)
	}
	if answer.Passes != 1 || answer.StoppedAt != "the rest" {
		t.Errorf("Passes = %d, StoppedAt = %q, want 1 and the pass's own remainder",
			answer.Passes, answer.StoppedAt)
	}
	// And the process was actually contained: a turn that had to wait out the
	// fake's own sleep would take thirty seconds, not the timeout it was given.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("the turn took %s, so the hung fake was waited out rather than stopped", elapsed)
	}
}

// A held-open turn is told its prompt on stdin and nowhere else. Read out of
// the shipped binary: with --input-format stream-json the CLI reads stdin and
// never looks at the prompt argument, so a prompt passed there would look
// sent, be recorded as sent, and never arrive.
func TestAHeldOpenTurnIsToldItsPromptOnlyOnStdin(t *testing.T) {
	fake := scriptCLI(t, []string{
		passEvent(0.02, 100, `{"findings":"a","completeness":1,"stopped_at":""}`),
	}, "", 0)
	req := reservedRequest()

	if _, err := fake.client(t).Turn(t.Context(), req); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	argv := fake.argv(t)
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--input-format stream-json", "--output-format stream-json", "--verbose",
		// The runaway guard is unchanged: the hard ceiling is still the CLI's
		// to enforce, and the allowance never reaches the command line.
		"--max-budget-usd 0.5",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the command line is missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--read-tokens") || strings.Contains(joined, "900") {
		t.Errorf("the read allowance reached the cli, which has no such flag:\n%s", joined)
	}
	for _, arg := range argv {
		if strings.Contains(arg, req.Prompt) {
			t.Errorf("the prompt was passed as an argument, where this mode ignores it: %q", arg)
		}
	}
	if got := fake.messages(t); len(got) != 1 || !strings.Contains(got[0], req.Prompt) {
		t.Errorf("the prompt did not arrive on stdin: %q", got)
	}
}

// THE FAILURE THIS TRIGGER EXISTS FOR, and the one a boundary check cannot
// reach. Measured 2026-08-14 on the re-run of the real 18-step plan: an
// explore step does all of its work inside turn 1, so no result event ever
// arrives before the ceiling, and an allowance checked only between passes is
// never checked at all -- 12 of 12 steps died with zero passes, exactly as
// they had before there was an allowance. Here the stream is that stream: a
// run of assistant events, no result event, and a kill at the end.
func TestTheAllowanceFiresBeforeTheFirstResultEvent(t *testing.T) {
	fake := scriptCLI(t, []string{
		// Three requests, 322 input-equivalent each; the third is what
		// crosses 900. Shaped like the live stream: cache creation carries
		// the weight and input_tokens is a rounding error beside it.
		assistantEvent("msg_a", 2, 160, 0, 0),
		assistantEvent("msg_b", 2, 160, 0, 0),
		assistantEvent("msg_c", 2, 160, 0, 0),
		// Answered only because it was told to -- which is the whole point.
		// Deliberately cheap: 150 input-equivalent, so nothing about this
		// event crosses the allowance and the nudge it answers can only have
		// come from the assistant events above it.
		passEvent(0.31, 100, `{"findings":"what I had","completeness":0.4,"stopped_at":"the rest"}`),
	}, "", 0)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	messages := fake.messages(t)
	if len(messages) != 2 {
		t.Fatalf("the cli was told %d things, want 2 -- the prompt and one finalize: %q",
			len(messages), messages)
	}
	if got := nudges(messages); got != 1 {
		t.Fatalf("finalize messages = %d, want exactly 1 mid-turn nudge: %q", got, messages)
	}
	if answer.Passes != 1 {
		t.Errorf("Passes = %d, want 1: the nudge bought the only answer there is", answer.Passes)
	}
	if answer.Completeness == nil || *answer.Completeness != 0.4 {
		t.Errorf("Completeness = %v, want 0.4", answer.Completeness)
	}
	if answer.StoppedAt != "the rest" {
		t.Errorf("StoppedAt = %q, want what the pass said it missed", answer.StoppedAt)
	}
}

// One nudge, not one per event. A turn keeps reading after the nudge lands --
// it has a tool call in flight and its own answer to write, measured live at
// 11.5s of work after the message went in -- so every event after the crossing
// arrives with the allowance still spent, and a second finalize would pay a
// whole pass to repeat a sentence.
func TestTheMidTurnNudgeIsSentOnce(t *testing.T) {
	fake := scriptCLI(t, []string{
		assistantEvent("msg_a", 1000, 0, 0, 0),
		assistantEvent("msg_b", 1000, 0, 0, 0),
		assistantEvent("msg_c", 1000, 0, 0, 0),
		passEvent(0.4, 3000, `{"findings":"enough","completeness":0.5,"stopped_at":"the rest"}`),
	}, "", 0)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := nudges(fake.messages(t)); got != 1 {
		t.Fatalf("finalize messages = %d, want exactly 1: %q", got, fake.messages(t))
	}
	if answer.Passes != 1 {
		t.Errorf("Passes = %d, want 1", answer.Passes)
	}
}

// The events of ONE message are one request seen more than once, and counting
// them twice is counting money twice. Measured 2026-08-14 on the live CLI: 4
// assistant events carried 2 message ids, and the pair sharing an id carried
// byte-identical usage. Summed per event that turn read 78,386 cache-creation
// tokens; per message, 39,193 -- exactly what the CLI's own result event then
// reported. Here the same shape crosses the allowance if, and only if, the
// repeats are counted.
func TestRepeatedEventsForOneMessageCountOnce(t *testing.T) {
	fake := scriptCLI(t, []string{
		// 802 input-equivalent for the message, restated four times. Counted
		// per event that is 3,208 and well past the allowance; counted per
		// message it is not.
		assistantEvent("msg_a", 2, 400, 0, 0),
		assistantEvent("msg_a", 2, 400, 0, 0),
		assistantEvent("msg_a", 2, 400, 0, 0),
		assistantEvent("msg_a", 2, 400, 0, 0),
	}, "Reached maximum budget ($0.50)", 1)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err == nil {
		t.Fatal("a death with no pass answered was reported as a success")
	}
	if got := nudges(fake.messages(t)); got != 0 {
		t.Errorf("finalize messages = %d, want 0: one request was counted four times", got)
	}
	// And the same double-count would have been the receipt handed back.
	if answer.Spent.CacheWriteTokens != 400 {
		t.Errorf("cache write = %d, want 400 -- the message's own usage, once",
			answer.Spent.CacheWriteTokens)
	}
}

// A death with no result event at all is the measured shape: no `result`
// field, and therefore no cost and no usage from the CLI. The assistant events
// are the only receipt that exists, so they are the one that travels -- with
// no dollar figure, because the CLI never printed one and inventing one here
// is what contract.Charge.PricedBy exists to prevent.
func TestTheAssistantEventsAreTheReceiptWhenNoPassLanded(t *testing.T) {
	fake := scriptCLI(t, []string{
		assistantEvent("msg_a", 300, 40, 500, 8),
		assistantEvent("msg_b", 200, 60, 1500, 12),
	}, "Reached maximum budget ($0.50)", 1)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err == nil {
		t.Fatal("a death with no pass answered was reported as a success")
	}
	if answer.Passes != 0 {
		t.Errorf("Passes = %d, want 0", answer.Passes)
	}
	// Summed field by field across both messages, the same arithmetic the CLI
	// would have done for the total it never got to print.
	if answer.Spent.InputTokens != 500 || answer.Spent.OutputTokens != 20 {
		t.Errorf("input/output = %d/%d, want 500/20 -- the events summed, not the last one",
			answer.Spent.InputTokens, answer.Spent.OutputTokens)
	}
	if answer.Spent.CacheReadTokens != 2000 || answer.Spent.CacheWriteTokens != 100 {
		t.Errorf("cache read/write = %d/%d, want 2000/100",
			answer.Spent.CacheReadTokens, answer.Spent.CacheWriteTokens)
	}
	if answer.Spent.USD != nil {
		t.Errorf("USD = %v, want nil: the cli priced nothing, so neither does this",
			*answer.Spent.USD)
	}
}

// The two readings of what a turn has spent must not be added together. The
// result event's usage is already the cumulative total the assistant events
// were summed into (read out of the shipped binary 2.1.232), so adding them
// would double-count every pass and fire the nudge on a turn that has spent
// half what it looks like.
func TestTheTwoUsageReadingsAreNotAddedTogether(t *testing.T) {
	fake := scriptCLI(t, []string{
		// 500 input-equivalent, well under the allowance.
		assistantEvent("msg_a", 500, 0, 0, 0),
		// The CLI's own cumulative figure for the same 500, plus its
		// answer's output. Added to the above it would clear 900; read as
		// the total it is, it does not.
		passEvent(0.05, 500, `{"findings":"a","completeness":0.2,"stopped_at":"the rest"}`),
		passEvent(0.06, 520, `{"findings":"ab","completeness":1,"stopped_at":""}`),
	}, "", 0)

	answer, err := fake.client(t).Turn(t.Context(), reservedRequest())
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := nudges(fake.messages(t)); got != 0 {
		t.Fatalf("finalize messages = %d, want 0: 500 spent was counted as 1000+", got)
	}
	if answer.Passes != 2 {
		t.Errorf("Passes = %d, want 2 -- the turn ran on to answer whole", answer.Passes)
	}
}

// The ordering claim the end-to-end tests cannot make race-free: exactly when
// the nudge goes out, counted in assistant events and with no result event in
// the picture at all. Driven directly, because through a process the fake's
// next print and this write are concurrent and the count would flake.
//
// This is the whole mechanism in four lines: nothing is said while the turn is
// inside its allowance, one thing is said the moment it is not, and nothing is
// said again however long the turn keeps working afterwards.
func TestTheNudgeFiresOnAnAssistantEventAlone(t *testing.T) {
	var sent bytes.Buffer
	var conv conversation
	// 322 input-equivalent apiece against an allowance of 900: two fit, the
	// third does not. A fresh id each time, so each one is its own request,
	// and shaped like the live stream -- input_tokens 2, the weight in cache
	// creation -- so a check reading input_tokens alone would see 2 and nudge
	// nothing.
	reported := usage{InputTokens: 2, CacheWrite: 160}

	for i, want := range []int{0, 0, 1, 1, 1} {
		if stop := conv.spend(fmt.Sprintf("msg_%d", i), reported, &sent, readAllowance); stop {
			t.Fatalf("event %d stopped the turn: a written message is not a reason to stop", i+1)
		}
		if got := strings.Count(sent.String(), "Stop reading"); got != want {
			t.Fatalf("after %d events the cli had been nudged %d times, want %d:\n%s",
				i+1, got, want, sent.String())
		}
	}
	if conv.rounds != 0 || conv.answered != 0 {
		t.Errorf("rounds/answered = %d/%d, want 0/0: no result event ever arrived",
			conv.rounds, conv.answered)
	}
	// The nudge is a message like any other, so it must be the shape the CLI
	// validates -- the same check messages makes of a real stream.
	var line struct {
		Type    string `json:"type"`
		Message struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	if err := json.Unmarshal(sent.Bytes(), &line); err != nil {
		t.Fatalf("the nudge is not a JSON line: %v", err)
	}
	if line.Type != "user" || line.Message.Role != "user" {
		t.Errorf("the nudge would be refused or ignored: %s", sent.String())
	}
}

// A turn the CLI reports no usage for gets no mid-turn signal, and must not
// get a false one. Zero read as "spent nothing" is right here and is the same
// reading everywhere else in this file; what stops such a turn is maxPasses.
func TestAnEventReportingNoUsageNudgesNothing(t *testing.T) {
	var sent bytes.Buffer
	var conv conversation
	for i := range maxPasses * 2 {
		if stop := conv.spend(fmt.Sprintf("msg_%d", i), usage{}, &sent, readAllowance); stop {
			t.Fatal("an event reporting nothing stopped the turn")
		}
	}
	if sent.Len() != 0 {
		t.Errorf("a turn that reported no usage was told to stop reading:\n%s", sent.String())
	}
}

// The four weights, pinned to the turn they were measured against, because
// three of them are invisible to any test that only watches a nudge fire.
//
// Measured 2026-08-14, one live turn surveying this repository: the first
// assistant event reported input_tokens 2, cache_creation 32,799, cache_read 0,
// output 5, and the result event that ended the turn reported 4 / 39,193 /
// 32,799 / 1,067 -- and charged $0.261685 for it. That last figure is what
// makes this a measurement rather than a restatement of the code: the weights
// have to turn the same usage into the same money.
func TestWeighIsTheMeasuredPriceRatios(t *testing.T) {
	firstEvent := usage{InputTokens: 2, CacheWrite: 32_799, CacheRead: 0, OutputTokens: 5}
	if got := weigh(firstEvent); got != 65_625 {
		t.Errorf("weigh(first assistant event) = %d, want 65625 -- about $0.20 before the "+
			"turn has read anything of its own", got)
	}
	wholeTurn := usage{InputTokens: 4, CacheWrite: 39_193, CacheRead: 32_799, OutputTokens: 1_067}
	if got := weigh(wholeTurn); got != 87_004 {
		t.Errorf("weigh(the turn's own total) = %d, want 87004", got)
	}
	// The reconciliation. 333,333 input-equivalent tokens to the dollar is
	// what normalising to input at $3/M means, so weighing the turn's usage
	// has to reproduce the price the CLI put on it. Within 1%: the weights
	// are ratios, and the CLI counts a few things these fields do not name.
	const perUSD = 333_333.0
	const charged = 0.261685
	if implied := float64(weigh(wholeTurn)) / perUSD; math.Abs(implied-charged)/charged > 0.01 {
		t.Errorf("weighed to $%.6f, the cli charged $%.6f: the ratios do not price this turn",
			implied, charged)
	}
	// Each kind on its own, so a weight that drifts is named by the failure
	// rather than hidden in a sum.
	for _, tc := range []struct {
		name string
		u    usage
		want int
	}{
		{"input at 1x", usage{InputTokens: 1000}, 1000},
		// x2, not x1.25: this CLI writes 1-hour cache entries. Measured on
		// the same turn -- cache_creation reported ephemeral_1h_input_tokens
		// 39,193 and ephemeral_5m_input_tokens 0 -- and at x1.25 the
		// reconciliation above lands 34% under what was charged.
		{"cache creation at 2x", usage{CacheWrite: 1000}, 2000},
		{"cache read at 0.1x", usage{CacheRead: 1000}, 100},
		{"output at 5x", usage{OutputTokens: 1000}, 5000},
		{"nothing weighs nothing", usage{}, 0},
	} {
		if got := weigh(tc.u); got != tc.want {
			t.Errorf("%s: weigh = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Floor
// ---------------------------------------------------------------------------

// floorScript stands in for the real binary on Floor's own path: it answers
// --version on its own, without letting that overwrite the turn's own argv
// recording, and otherwise answers exactly one single-shot turn the way
// stub does. A dedicated fake rather than reusing stub or scriptCLI: stub
// records no argv at all, and scriptCLI's fake would have the version probe
// itself recorded as if it were the turn.
const floorScript = `#!/bin/sh
case "$1" in --version) echo '2.1.232 (Claude Code)'; exit 0;; esac
printf '%%s\n' "$@" > '%s'
cat <<'ENVELOPE'
%s
ENVELOPE
`

// floorClient builds a Client over floorScript and returns the path the
// turn's own argv was recorded to.
func floorClient(t *testing.T, stdout string) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	binary := filepath.Join(dir, "claude")
	script := fmt.Sprintf(floorScript, argvPath, stdout)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the floor stub: %v", err)
	}
	client, err := New(Options{Binary: binary, Explore: "explore-model", Plan: "plan-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, argvPath
}

func floorArgv(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the floor probe recorded no argv, so it was never started: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}

// floorAnswer is a clean, single-pass probe: no tool call (num_turns 1, no
// denials) and a real cache-write figure -- shaped after the 25,340-token
// floor this whole feature exists to measure.
const floorAnswer = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":25340},
  "total_cost_usd":0.28,"num_turns":1}`

// The warm reading, in the shape the live CLI answered on the second probe of
// the evening: the same prefix the cold probe wrote, read back for a
// twenty-eighth of the price.
const floorWarmAnswer = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":25340,"cache_creation_input_tokens":0},
  "total_cost_usd":0.01,"num_turns":1}`

// A warm reading is no longer refused: the prefix was already paid for by an
// earlier turn, and CacheReadTokens plus PrefixTokens say exactly how much
// of it -- see FloorMeasurement.Cold. USD stays whatever the receipt priced
// this particular turn at; converting that into a cold-equivalent floor is
// internal/floor's job, not this package's.
func TestAFloorProbeAgainstAWarmCacheReportsTheReadPrefix(t *testing.T) {
	client, _ := floorClient(t, floorWarmAnswer)
	m, err := client.Floor(context.Background(), FloorRequest{Role: RoleExplore})
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if m.Cold {
		t.Error("Cold = true, want false for a warm-cache reading")
	}
	if m.CacheReadTokens != 25340 {
		t.Errorf("CacheReadTokens = %d, want 25340", m.CacheReadTokens)
	}
	if m.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0", m.CacheWriteTokens)
	}
	if m.PrefixTokens != 25340 {
		t.Errorf("PrefixTokens = %d, want 25340", m.PrefixTokens)
	}
	if m.USD != 0.01 {
		t.Errorf("USD = %v, want the receipt's own 0.01", m.USD)
	}
}

// A cold reading has nothing read back, so PrefixTokens is exactly the
// cache-write figure -- the invariant internal/floor's PriceForModel relies
// on to turn a cold row into a price per token.
func TestAFloorProbeAgainstAColdCacheReportsPrefixEqualsWrite(t *testing.T) {
	client, _ := floorClient(t, floorAnswer)
	m, err := client.Floor(context.Background(), FloorRequest{Role: RoleExplore})
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if !m.Cold {
		t.Error("Cold = false, want true for a cold-cache reading")
	}
	if m.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want 0", m.CacheReadTokens)
	}
	if m.PrefixTokens != m.CacheWriteTokens {
		t.Errorf("PrefixTokens = %d, CacheWriteTokens = %d, want them equal on a cold reading", m.PrefixTokens, m.CacheWriteTokens)
	}
}

// A clean probe reports USD, the token breakdown, the resolved model name,
// and the version token trimmed off the stub's own banner.
func TestAFloorProbeMeasuresUSDTokensAndVersion(t *testing.T) {
	client, _ := floorClient(t, floorAnswer)
	m, err := client.Floor(context.Background(), FloorRequest{Role: RoleExplore})
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if m.USD != 0.28 {
		t.Errorf("USD = %v, want 0.28", m.USD)
	}
	if m.CacheWriteTokens != 25340 || m.InputTokens != 4 || m.OutputTokens != 3 {
		t.Errorf("token breakdown = %+v, want the usage block's own counts", m)
	}
	if m.Model != "explore-model" {
		t.Errorf("Model = %q, want %q", m.Model, "explore-model")
	}
	if m.CLIVersion != "2.1.232" {
		t.Errorf("CLIVersion = %q, want the banner trimmed to its first token", m.CLIVersion)
	}
}

// num_turns > 1 means the far side read a tool result back -- see
// claudecode's own completenessDoubt, measured the same way. A probe that
// did work priced that work into the same total the floor is meant to be,
// and that total must never reach a caller who would trust it as one.
const floorUsedATool = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_creation_input_tokens":25340},
  "total_cost_usd":0.30,"num_turns":3}`

func TestAFloorProbeThatUsedAToolIsRefused(t *testing.T) {
	client, _ := floorClient(t, floorUsedATool)
	if _, err := client.Floor(context.Background(), FloorRequest{Role: RoleExplore}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("Floor: kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// A permission denial is a tool call too, even one refused before it ran --
// the far side still reached for something this probe never asked it to.
const floorDeniedATool = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_creation_input_tokens":25340},
  "total_cost_usd":0.30,"num_turns":1,
  "permission_denials":[{"tool_name":"Bash"}]}`

func TestAFloorProbeThatWasDeniedAToolIsRefused(t *testing.T) {
	client, _ := floorClient(t, floorDeniedATool)
	if _, err := client.Floor(context.Background(), FloorRequest{Role: RoleExplore}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("Floor: kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// total_cost_usd absent is "the cli did not price this turn" -- see
// chargeFrom's own doc -- and FloorMeasurement.USD has no pointer to leave
// nil instead. A probe with nothing to report honestly is refused rather
// than made to report zero.
const floorUnpriced = `{"is_error":false,"subtype":"success","result":"ok",
  "usage":{"input_tokens":4,"output_tokens":3,"cache_creation_input_tokens":25340}}`

func TestAFloorProbeWithNoCostIsRefused(t *testing.T) {
	client, _ := floorClient(t, floorUnpriced)
	if _, err := client.Floor(context.Background(), FloorRequest{Role: RoleExplore}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("Floor: kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// The probe has to be shaped exactly like the step it is standing in for:
// the role's own model, the tools config a step would carry, and no
// --json-schema -- floorPrompt asks for free text, never structure.
func TestAFloorProbeArgvCarriesTheRolesModelAndToolsNoSchema(t *testing.T) {
	client, argvPath := floorClient(t, floorAnswer)
	tools := `{"mcpServers":{"atenea":{"command":"atenea","args":["mcp"]}}}`
	_, err := client.Floor(context.Background(), FloorRequest{
		Role:     RolePlan,
		Tools:    tools,
		Builtins: []string{"Read"},
	})
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	argv := floorArgv(t, argvPath)
	if got, ok := flagValue(argv, "--model"); !ok || got != "plan-model" {
		t.Errorf("--model = %q, ok=%v, want %q", got, ok, "plan-model")
	}
	if got, ok := flagValue(argv, "--mcp-config"); !ok || got != tools {
		t.Errorf("--mcp-config = %q, ok=%v, want the request's own Tools", got, ok)
	}
	if slices.Contains(argv, "--json-schema") {
		t.Errorf("argv carries --json-schema, want none: %v", argv)
	}
}

// ---------------------------------------------------------------------------
// FirstCall
// ---------------------------------------------------------------------------

// firstCallStream is the event stream of a turn that made one tool call,
// shaped after the real one measured 2026-08-15: a ~5,650-token prefix on the
// first assistant message and a ~41,930-token block on the second, the one
// that arrives with the first tool result.
//
// The first message is restated twice on purpose. Measured 2026-08-14, one
// message's content blocks arrive as separate events carrying identical usage,
// and a reader that appended every event would count this prefix twice --
// which is the whole reason Client.observe keys on the message id.
const firstCallStream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":2,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":5647}}}
{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":2,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":5647}}}
{"type":"user","subtype":"tool_result"}
{"type":"assistant","message":{"id":"msg_2","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":0,"cache_creation_input_tokens":41927}}}
{"type":"result","is_error":false,"subtype":"success","result":"ok","num_turns":3,"usage":{"input_tokens":5,"output_tokens":14,"cache_read_input_tokens":0,"cache_creation_input_tokens":47574},"total_cost_usd":0.4935}`

// The same turn on a machine whose cache already holds both blocks: identical
// totals per message, split differently. This is the invariance the stored
// counts rely on -- 5,651 and 41,934 against the cold 5,647 and 41,927.
const firstCallWarmStream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":2,"output_tokens":9,"cache_read_input_tokens":4772,"cache_creation_input_tokens":879}}}
{"type":"user","subtype":"tool_result"}
{"type":"assistant","message":{"id":"msg_2","usage":{"input_tokens":3,"output_tokens":5,"cache_read_input_tokens":40227,"cache_creation_input_tokens":1707}}}
{"type":"result","is_error":false,"subtype":"success","result":"ok","num_turns":3,"usage":{"input_tokens":5,"output_tokens":14,"cache_read_input_tokens":44999,"cache_creation_input_tokens":2586},"total_cost_usd":0.0437}`

// A turn that answered without touching a tool: num_turns 1, one assistant
// message, and nothing to price as a first call.
const firstCallNoToolStream = `{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":2,"output_tokens":6,"cache_read_input_tokens":0,"cache_creation_input_tokens":5647}}}
{"type":"result","is_error":false,"subtype":"success","result":"ok","num_turns":1,"usage":{"input_tokens":2,"output_tokens":6,"cache_read_input_tokens":0,"cache_creation_input_tokens":5647},"total_cost_usd":0.06}`

// toolSurface is the request shape every probe below shares: a surface that
// can actually call something, which is what FirstCall requires.
func toolSurface(role Role) FloorRequest {
	return FloorRequest{Role: role, Builtins: []string{"Read", "Glob"}}
}

// The measurement this probe exists for: two quantities off ONE run, message
// by message. A floor built on the first alone described 12% of what a step
// pays to get started, and that is the defect that shipped on 2026-08-15.
func TestAFirstCallProbeSplitsThePrefixFromTheBlockThatFollowsIt(t *testing.T) {
	client, _ := floorClient(t, firstCallStream)
	m, err := client.FirstCall(context.Background(), toolSurface(RoleExplore))
	if err != nil {
		t.Fatalf("FirstCall: %v", err)
	}
	if m.PrefixTokens != 5647 {
		t.Errorf("PrefixTokens = %d, want 5647 -- the first message alone, not the total",
			m.PrefixTokens)
	}
	if m.FirstCallTokens != 41927 {
		t.Errorf("FirstCallTokens = %d, want 41927 -- the message that arrives with the "+
			"first tool result", m.FirstCallTokens)
	}
	if !m.Cold {
		t.Error("Cold = false, want true: this prefix was written, not read")
	}
	if m.USD != 0.4935 {
		t.Errorf("USD = %v, want the receipt's own 0.4935", m.USD)
	}
}

// Warm or cold, the per-message totals are the same to within a few tokens,
// and it is the totals that get stored. A probe that recorded the write half
// would answer a different number every hour for the same machine.
func TestAFirstCallProbeReadsTheSameTotalsWarm(t *testing.T) {
	client, _ := floorClient(t, firstCallWarmStream)
	m, err := client.FirstCall(context.Background(), toolSurface(RoleExplore))
	if err != nil {
		t.Fatalf("FirstCall: %v", err)
	}
	if m.PrefixTokens != 5651 {
		t.Errorf("PrefixTokens = %d, want 5651 (4772 read + 879 written)", m.PrefixTokens)
	}
	if m.FirstCallTokens != 41934 {
		t.Errorf("FirstCallTokens = %d, want 41934 (40227 read + 1707 written)",
			m.FirstCallTokens)
	}
	if m.Cold {
		t.Error("Cold = true, want false: the prefix came back read")
	}
}

// The refusal that keeps this from degrading into the probe it replaced: no
// tool call, no first-call figure, and answering with the prefix alone is
// exactly the measurement that was already proven wrong.
func TestAFirstCallProbeThatNeverCalledAToolIsRefused(t *testing.T) {
	client, _ := floorClient(t, firstCallNoToolStream)
	_, err := client.FirstCall(context.Background(), toolSurface(RoleExplore))
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("FirstCall: kind = %v, want invalid_input", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "without calling a tool") {
		t.Errorf("the refusal does not say what was missing: %v", err)
	}
}

// `plan` is this shape: no builtins, no --mcp-config, no first tool call it
// could ever make. Refused rather than measured as a prefix probe, so a row
// never carries a first-call figure nobody took.
func TestAFirstCallProbeOnASurfaceWithNoToolsIsRefused(t *testing.T) {
	client, _ := floorClient(t, firstCallStream)
	_, err := client.FirstCall(context.Background(), FloorRequest{Role: RolePlan})
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("FirstCall: kind = %v, want invalid_input", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "no tools at all") {
		t.Errorf("the refusal does not name the surface: %v", err)
	}
}

// The probe streams so it can read usage per message, and it must still pass
// the prompt on the command line: --input-format stream-json would take the
// prompt off argv and the turn would start with nothing asked of it.
func TestAFirstCallProbeStreamsWithoutTakingThePromptOffArgv(t *testing.T) {
	client, argvPath := floorClient(t, firstCallStream)
	if _, err := client.FirstCall(context.Background(), toolSurface(RoleExplore)); err != nil {
		t.Fatalf("FirstCall: %v", err)
	}
	argv := floorArgv(t, argvPath)
	if got, ok := flagValue(argv, "--output-format"); !ok || got != "stream-json" {
		t.Errorf("--output-format = %q, ok=%v, want stream-json", got, ok)
	}
	if !slices.Contains(argv, "--verbose") {
		t.Errorf("argv carries no --verbose, which this CLI rejects the stream without: %v", argv)
	}
	if slices.Contains(argv, "--input-format") {
		t.Errorf("argv carries --input-format, which would take the prompt off the command "+
			"line: %v", argv)
	}
	if !slices.Contains(argv, firstCallPrompt) {
		t.Errorf("the prompt is not on argv, so the turn was asked nothing: %v", argv)
	}
}

// VersionToken is exported so cmd/atenea/floor.go can trim the running
// CLI's own --version banner the same way a stored Measurement.CLIVersion
// was trimmed -- see the function's own doc for the 2026-08-14 false-stale
// measurement that made comparing an untrimmed banner against a trimmed
// stored token the bug.
func TestVersionToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		banner string
		want   string
	}{
		{"real banner shape", "2.1.232 (Claude Code)", "2.1.232"},
		{"already trimmed", "2.1.232", "2.1.232"},
		{"empty", "", ""},
	} {
		if got := VersionToken(tc.banner); got != tc.want {
			t.Errorf("%s: VersionToken(%q) = %q, want %q", tc.name, tc.banner, got, tc.want)
		}
	}
}
