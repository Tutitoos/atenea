package model

import (
	"context"
	"encoding/json"
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
