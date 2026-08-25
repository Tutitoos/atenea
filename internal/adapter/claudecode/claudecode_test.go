package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The adapter's whole job is translation, so these tests are about the two
// directions of it: what Claude Code is told, and what its answer becomes. The
// real binary is exercised end to end from cmd, where a machine with no login
// is a fact about the machine rather than a broken unit test.

// codeSearch mirrors the shipped capability, because the adapter has to produce
// something that passes the declared output schema, not something that merely
// looks right.
func codeSearch() contract.Capability {
	return contract.Capability{
		ID:        "code.search",
		Version:   contract.Version{Major: 1},
		Summary:   "Find literal text in a repository.",
		Semantics: "Flat text search.\nThe engine does not parse anything.",
		Effects:   []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "query", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "match_case", Type: contract.TypeBool},
			{Name: "regex", Type: contract.TypeBool},
			{Name: "whole_word", Type: contract.TypeBool},
			{Name: "file_types", Type: contract.TypeStringList},
			{Name: "context_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{{
			Name: "matches", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true, Summary: "Where it is."},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "column", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			},
		}},
	}
}

func implementation() contract.Implementation {
	return contract.Implementation{
		ID: "claude.search", Provider: "claude-code", Capability: "code.search",
	}
}

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := New(Options{
		Implementations: []string{"claude.search"},
		Sensitive:       []string{".env", "*.pem", "credentials.json"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

func TestClaudeRunnerIdentitySurfaceAndCapabilities(t *testing.T) {
	runner := newTestRunner(t)
	if runner.ID() != "claude-code" || runner.Surface() == "" {
		t.Fatalf("identity surface = %q", runner.Surface())
	}
	if got := runner.Capabilities(); len(got) != 1 || got[0] != CodeSearch {
		t.Fatalf("capabilities = %v", got)
	}
	if got := runner.Implementations(); len(got) != 1 || got[0] != "claude.search" {
		t.Fatalf("implementations = %v", got)
	}
}

func request(t *testing.T, payload map[string]any, effects ...contract.Effect) contract.RunRequest {
	t.Helper()
	repo := contract.NewRepository("current", t.TempDir(), []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	return contract.RunRequest{
		Capability:     codeSearch(),
		Implementation: implementation(),
		Repository:     repo,
		Payload:        payload,
		Permission: contract.Permission{
			Task:    "find it",
			Effects: append([]contract.Effect{contract.EffectRead}, effects...),
			// A funded commission, because that is the ordinary case and every
			// test below is about something else. The grant arrives on the
			// request now: this adapter holds no ceiling of its own. What
			// happens when it is empty has its own tests in money_test.go.
			BudgetUSD: 0.25,
		},
	}
}

func newSearch(t *testing.T, payload map[string]any) search {
	t.Helper()
	s, err := readSearch(payload)
	if err != nil {
		t.Fatalf("readSearch: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// What Claude Code is told
// ---------------------------------------------------------------------------

// Atenea is the single source of truth for what a capability means. A
// CLAUDE.md in the repository being searched must not be able to change it,
// and a headless turn has nobody to answer a permission prompt.
//
// --safe-mode also pays for itself. Every commission is a fresh session, and
// a session with customisations on loads them all again: measured on this
// machine at 68,754 characters of MCP tool schemas alone -- roughly 17,000
// tokens -- across five of the nine servers a normal chat connects, before a
// hook, a skill or a CLAUDE.md is counted. `claude --safe-mode mcp list`
// answers "No MCP servers configured", so a dispatch carries none of it.
func TestTheTurnIsPinnedDownBeforeItStarts(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "needle"})
	args, err := runner.args(req, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	joined := strings.Join(args, " ")

	for _, want := range []string{"--print", "--output-format stream-json", "--verbose", "--json-schema",
		"--safe-mode", "--no-session-persistence", "--max-budget-usd"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the command line is missing %q:\n%s", want, joined)
		}
	}
}

// Measured: a second run reusing a --session-id fails with a plain-text
// "already in use". Pinning one per chat would break on every commission after
// the first, so isolation is bought by not persisting at all.
func TestNoSessionIsEverPinned(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "needle"})
	args, err := runner.args(req, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	for _, arg := range args {
		if arg == "--session-id" || arg == "--resume" || arg == "--continue" {
			t.Fatalf("the turn reuses a far-side session through %q", arg)
		}
	}
}

// The permission is enforced, not requested: --tools decides which tools exist
// for the turn, so a read-only commission is handed a Claude Code that has no
// way to write.
func TestTheCommissionDecidesWhichToolsExist(t *testing.T) {
	cases := []struct {
		name    string
		effects []contract.Effect
		want    []string
		absent  []string
	}{
		{
			name: "reading only",
			want: []string{"Grep", "Glob", "Read"},
			// Reading is free, and everything heavier has to be granted. A
			// read-only turn that could still edit would make the whole
			// effects design decorative.
			absent: []string{"Edit", "Write", "WebFetch", "WebSearch", "Bash"},
		},
		{
			name:    "writing granted",
			effects: []contract.Effect{contract.EffectWrite},
			want:    []string{"Grep", "Edit", "Write"},
			absent:  []string{"WebFetch", "WebSearch", "Bash"},
		},
		{
			name:    "reaching outside granted",
			effects: []contract.Effect{contract.EffectExternal},
			want:    []string{"Grep", "WebFetch", "WebSearch"},
			// Writing and reaching outside are separate on purpose: one can be
			// undone and the other cannot.
			absent: []string{"Edit", "Write", "Bash"},
		},
	}
	runner := newTestRunner(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request(t, map[string]any{"query": "x"}, tc.effects...)
			args, err := runner.args(req, newSearch(t, req.Payload))
			if err != nil {
				t.Fatalf("args: %v", err)
			}
			list := flagValue(t, args, "--tools")
			allowed := strings.Split(list, ",")
			for _, want := range tc.want {
				if !contains(allowed, want) {
					t.Errorf("%s is missing from the turn's tools: %v", want, allowed)
				}
			}
			for _, never := range tc.absent {
				if contains(allowed, never) {
					t.Errorf("%s exists for a turn that was never granted it: %v", never, allowed)
				}
			}
			// Whatever exists must also be pre-approved, or a headless turn
			// stalls on a prompt nobody can answer.
			if got := flagValue(t, args, "--allowedTools"); got != list {
				t.Errorf("--allowedTools = %q, --tools = %q", got, list)
			}
		})
	}
}

// A far side that thinks answers in whatever it was asked for, so the asking
// has to be exact -- and it has to be the ANSWER shape. The schema itself is
// the contract's, tested there; what belongs here is that this adapter hands
// the CLI the outputs and not the inputs, encoded the one way the flag takes.
func TestTheSchemaFlagCarriesTheCapabilitysAnswerShape(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"}, contract.EffectRead)
	args, err := runner.args(req, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("args: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal([]byte(flagValue(t, args, "--json-schema")), &schema); err != nil {
		t.Fatalf("the flag did not carry valid JSON: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if _, isOutput := properties["matches"]; !isOutput {
		t.Fatalf("properties = %v, want the answer shape", properties)
	}
	if _, isInput := properties["query"]; isInput {
		t.Error("the far side was asked to fill in the inputs")
	}
	// A field's summary is the only place the capability says what it MEANS,
	// and it is the one thing a model reads that a parser would not.
	item, _ := properties["matches"].(map[string]any)["items"].(map[string]any)
	fields, _ := item["properties"].(map[string]any)
	if got, _ := fields["path"].(map[string]any); got["description"] != "Where it is." {
		t.Errorf("the field summary did not travel: %v", got)
	}
}

// The intent flags are declared as intent, and a model is the one far side
// that can act on them as written rather than through a flag.
func TestTheQueryAndItsIntentReachTheFarSideVerbatim(t *testing.T) {
	runner := newTestRunner(t)
	payload := map[string]any{
		"query": "func (r *Runner)", "regex": true, "match_case": true,
		"whole_word": true, "file_types": []any{"go"}, "scope": []any{"internal"},
	}
	req := request(t, payload)
	text := prompt(req, newSearch(t, payload), runner.sensitive)

	if !strings.Contains(text, "func (r *Runner)") {
		t.Error("the query did not reach the far side verbatim")
	}
	for _, want := range []string{
		"regular expression", "Case matters", "whole words", "go", "internal",
		".env", "Grep",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the prompt never mentions %q:\n%s", want, text)
		}
	}
	// The semantics are prose with newlines in the settings file; a prompt
	// that pasted them raw would break its own layout.
	if strings.Contains(text, "search.\nThe engine") {
		t.Error("the capability's semantics went in unflattened")
	}
}

func TestTheDefaultsSayLiteralAndCaseInsensitive(t *testing.T) {
	runner := newTestRunner(t)
	payload := map[string]any{"query": "needle"}
	req := request(t, payload)
	text := prompt(req, newSearch(t, payload), runner.sensitive)

	if !strings.Contains(text, "literal text, not a pattern") {
		t.Error("an unflagged query was not declared literal")
	}
	if !strings.Contains(text, "Ignore case") {
		t.Error("an unflagged query was not declared case-insensitive")
	}
	if !strings.Contains(text, "empty list") {
		t.Error("nothing tells the far side that no matches is a valid answer")
	}
}

// ---------------------------------------------------------------------------
// What comes back
// ---------------------------------------------------------------------------

// Measured on the real binary: a turn that failed to authenticate still
// reported subtype "success" while is_error was true. Reading the subtype
// would call that error a result.
func TestTheSubtypeIsNotTrustedOverIsError(t *testing.T) {
	out, err := parse([]byte(`{"is_error":true,"subtype":"success",
		"result":"Failed to authenticate: OAuth session expired and could not be refreshed"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !out.IsError {
		t.Fatal("is_error was not read")
	}
	if out.Subtype != "success" {
		t.Fatalf("the fixture stopped reproducing the trap: subtype = %q", out.Subtype)
	}
	if got := contract.KindOf(failureFor(out.Result, nil)); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable: a client nobody is logged into is unreachable", got)
	}
}

func TestEveryFailureIsSortedIntoTheSharedBins(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    contract.FailureKind
	}{
		{"expired login", "Failed to authenticate: OAuth session expired", contract.FailureUnavailable},
		{"never logged in", "You are not logged in", contract.FailureUnavailable},
		// Money is a permission: the ceiling is one Atenea set and passed
		// down, so running out of it is a refusal on this machine. The turn
		// ceiling next to it is not -- Atenea never grants turns, so that one
		// really is the far side giving up.
		//
		// All three ceiling strings are copied from a real envelope, because
		// the invented one that used to be here ("Reached max budget of
		// $0.25") was never a string the binary puts anywhere this adapter
		// looks. It passed while the live path was broken.
		{"spending ceiling", "Reached maximum budget ($0.25)", contract.FailurePermissionDenied},
		{"ceiling as a terminal reason", "budget_exhausted", contract.FailurePermissionDenied},
		{"ceiling as a subtype", "error_max_budget_usd", contract.FailurePermissionDenied},
		{"turn ceiling", "Stopped: error_max_turns", contract.FailureTimeout},
		{"refused an action", "Permission denied for tool Write", contract.FailurePermissionDenied},
		{"missing target", "no such file or directory", contract.FailureNotFound},
		{"anything else", "the wheels came off", contract.FailureUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := failureFor(tc.message, nil)
			if got := contract.KindOf(failure); got != tc.want {
				t.Errorf("%q -> %v, want %v", tc.message, got, tc.want)
			}
			// The untranslated text has to survive for whoever debugs later.
			if failure.Raw != tc.message {
				t.Errorf("the raw message was lost: %q", failure.Raw)
			}
			if strings.Contains(failure.Message, tc.message) {
				t.Errorf("the vendor text leaked into the message: %q", failure.Message)
			}
		})
	}
}

// A turn that fell over before printing an envelope says so in plain text.
// Treating that as JSON would report a crash as an unreadable answer.
func TestAPlainTextFailureIsStillSorted(t *testing.T) {
	_, err := parse([]byte("Error: Session ID 3f2504e0 is already in use.\n"))
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", contract.KindOf(err))
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) {
		t.Fatal("the parse failure did not come back as a Failure")
	}
	if !strings.Contains(failure.Raw, "already in use") {
		t.Errorf("the plain-text reason was dropped: %q", failure.Raw)
	}
}

func TestAnEmptyAnswerIsNotZeroMatches(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	_, _, err := runner.readAnswer(envelope{Result: "I could not do that"}, req.Repository.Path, newSearch(t, req.Payload))
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable: a turn with no structure did not search", got)
	}
}

func TestARefusedActionIsAPermissionFailure(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	out := envelope{
		StructuredOutput:  json.RawMessage(`{"matches":[]}`),
		PermissionDenials: []json.RawMessage{json.RawMessage(`{"tool":"Write"}`)},
	}
	_, _, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
}

// A thinking far side can be wrong in ways a tool cannot: it can open a file
// it was told to leave alone, or answer about one outside the repository.
// Trusting the instruction alone would make the security design advisory.
func TestTheAnswerIsCheckedAgainstWhatWasForbidden(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "secret"})
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"internal/core.go","line":10,"column":3,"snippet":"secret"},
		{"path":".env","line":1,"column":1,"snippet":"secret=hunter2"},
		{"path":"deploy/server.pem","line":2,"column":1,"snippet":"secret"},
		{"path":"../elsewhere/leak.go","line":4,"column":1,"snippet":"secret"},
		{"path":"/etc/shadow","line":1,"column":1,"snippet":"secret"}
	]}`)}

	result, _, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want only the one that was allowed: %v", len(matches), matches)
	}
	first, _ := matches[0].(map[string]any)
	if first["path"] != "internal/core.go" {
		t.Errorf("the surviving match is %v", first["path"])
	}
}

// The capability requires a column and the far side has no obligation to know
// one. Dropping the match would lose a real hit; inventing an offset would
// point at a place nobody measured.
func TestAMatchWithoutAColumnKeepsItsLine(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	out := envelope{StructuredOutput: json.RawMessage(
		`{"matches":[{"path":"a.go","line":7,"snippet":"x"}]}`)}

	result, _, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("the match was dropped over a missing column: %v", matches)
	}
	hit, _ := matches[0].(map[string]any)
	if hit["line"] != 7 || hit["column"] != 1 {
		t.Errorf("hit = %v, want line 7 column 1", hit)
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		t.Errorf("the repaired answer no longer matches the capability: %v", err)
	}
}

func TestANonsenseCoordinateIsDropped(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"a.go","line":0,"column":1},
		{"path":"b.go","line":-3,"column":1},
		{"path":"c.go","line":2.5,"column":1},
		{"path":"","line":4,"column":1},
		{"path":"d.go","line":9,"column":2}
	]}`)}

	result, _, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want only the well-formed one: %v", len(matches), matches)
	}
}

// The declared file types are an intent the far side may ignore. The answer is
// what has to honor them.
func TestAnAnswerOutsideTheRequestedTypesIsDropped(t *testing.T) {
	runner := newTestRunner(t)
	payload := map[string]any{"query": "x", "file_types": []any{"go"}}
	req := request(t, payload)
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"a.go","line":1,"column":1},
		{"path":"README.md","line":1,"column":1}
	]}`)}

	result, _, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want only the .go one: %v", len(matches), matches)
	}
}

func TestInScope(t *testing.T) {
	cases := []struct {
		name     string
		relative string
		scope    []string
		want     bool
	}{
		{"empty scope means everything", "cmd/atenea/main.go", nil, true},
		{"dot means everything", "cmd/atenea/main.go", []string{"."}, true},
		{"exact file match", "pkg/contract/version.go", []string{"pkg/contract/version.go"}, true},
		{"nested under a directory", "internal/adapter/omp/omp.go", []string{"internal/adapter"}, true},
		{"sibling directory is not a match", "internal/adapter2/other.go", []string{"internal/adapter"}, false},
		{"outside every entry", "cmd/atenea/main.go", []string{"internal/adapter"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inScope(tc.relative, tc.scope); got != tc.want {
				t.Errorf("inScope(%q, %v) = %v, want %v", tc.relative, tc.scope, got, tc.want)
			}
		})
	}
}

// Scope is a request-shaping constraint, not a secret: unlike a sensitive
// file, a stray hit is worth reporting rather than hiding, and a sibling
// directory that merely shares a prefix must never be mistaken for a match.
func TestAMatchOutsideTheRequestedScopeIsDroppedWithANotice(t *testing.T) {
	runner := newTestRunner(t)
	payload := map[string]any{"query": "x", "scope": []any{"internal/adapter"}}
	req := request(t, payload)
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"internal/adapter/claudecode.go","line":1,"column":1},
		{"path":"internal/adapter2/other.go","line":1,"column":1},
		{"path":"cmd/atenea/main.go","line":1,"column":1}
	]}`)}

	result, counts, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want only the one inside scope: %v", len(matches), matches)
	}
	first, _ := matches[0].(map[string]any)
	if first["path"] != "internal/adapter/claudecode.go" {
		t.Errorf("the surviving match is %v", first["path"])
	}
	if counts.outOfScope != 2 {
		t.Errorf("out of scope = %d, want 2: the count is what gets recorded, the sentence is built from it", counts.outOfScope)
	}
}

func TestNoScopeMeansNoNoticeAndNothingDropped(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"a.go","line":1,"column":1},
		{"path":"deep/nested/b.go","line":1,"column":1}
	]}`)}

	result, counts, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want both: an empty scope means the whole repository", len(matches))
	}
	if counts.outOfScope != 0 {
		t.Errorf("out of scope = %d, want 0", counts.outOfScope)
	}
}

func TestNoMatchesIsAnAnswerNotAFailure(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[]}`)}

	result, _, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		t.Errorf("an empty answer does not match the capability: %v", err)
	}
}

// Cache reads were paid for, even if cheaply. A baseline that ignored them
// would make a warm repository look free, and the selector learns from that
// baseline.
func TestEveryTokenPaidForIsCounted(t *testing.T) {
	got := usage{InputTokens: 10, OutputTokens: 3, CacheRead: 100, CacheWrite: 50}.total()
	if got != 163 {
		t.Errorf("total = %d, want 163", got)
	}
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

func TestAMissingBinaryIsUnavailableRatherThanFatal(t *testing.T) {
	runner, err := New(Options{
		Binary:          "claude-not-installed-here",
		Implementations: []string{"claude.search"},
	})
	if err != nil {
		t.Fatalf("New refused to build over a missing binary: %v", err)
	}
	req := request(t, map[string]any{"query": "x"})
	_, runErr := runner.Run(context.Background(), req)
	if got := contract.KindOf(runErr); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
}

// The permission gate moved to the core, one site on the seam every dispatch
// crosses (internal/core/commission.go). It is not this adapter's decision and
// never should have been five adapters' decision.

func TestAnImplementationThisAdapterDoesNotServeIsUnavailable(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	req.Implementation.ID = "ripgrep"

	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
}

func TestABrokenSensitivePatternIsRefusedAtBuildTime(t *testing.T) {
	if _, err := New(Options{Sensitive: []string{"[3"}}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	if _, err := New(Options{Timeout: -time.Second}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

func TestAnEmptyQueryIsRefusedBeforeTheTurnStarts(t *testing.T) {
	if _, err := readSearch(map[string]any{"query": "   "}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

func TestAStreamEventKeepsTypeAndEnvelopeFields(t *testing.T) {
	event, ok, err := parseStreamLine(`{"type":"result","total_cost_usd":1.25}`)
	if err != nil || !ok {
		t.Fatalf("parseStreamLine() = %#v, %v, %v; want a valid event", event, ok, err)
	}
	if event.Type != "result" || event.Envelope.Type != "result" {
		t.Fatalf("types = %q and %q, want result", event.Type, event.Envelope.Type)
	}
	if event.Envelope.TotalCostUSD != 1.25 || !event.costSeen {
		t.Fatalf("cost = %v, seen = %v; want 1.25, true", event.Envelope.TotalCostUSD, event.costSeen)
	}
}

// The only thing about this adapter that cannot be checked by reasoning: that
// the flags it builds still exist. A renamed flag is refused by argument
// parsing before the turn starts, in plain text on stderr with no envelope --
// which is exactly what this asserts is NOT happening.
//
// It is opt-in, and the reason is the sentence the old version of this comment
// got wrong: "no login is needed" is true of the failure it looks for and false
// of the machine it ran on. On a developer's Mac with Claude Code logged in,
// this started a real, billable turn -- on every `go test ./...`, and on every
// `git push`, because the pre-push hook runs the suite. It also had no deadline,
// so a client that accepted the connection and went quiet hung the package
// until the go test timeout.
//
// So: the same shape opencode's real-provider smoke already uses. An explicit
// variable, and a deadline.
func TestTheRealClientStillAcceptsEveryFlagWeBuild(t *testing.T) {
	if os.Getenv(claudeSmokeEnv) != "1" {
		t.Skipf("set %s=1 to spend a real turn checking the installed client's flags", claudeSmokeEnv)
	}
	requireClaude(t)
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "needle"})
	args, err := runner.args(req, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("args: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), claudeSmokeTimeout)
	defer cancel()
	cmd := osexec.CommandContext(ctx, DefaultBinary, args...)
	cmd.Dir = t.TempDir()
	stdout, _ := cmd.Output()
	if ctx.Err() != nil {
		t.Fatalf("the installed client did not answer within %s", claudeSmokeTimeout)
	}

	// The exit code is not the claim -- a machine with an expired login exits
	// non-zero and is still fine. The claim is that the client understood
	// what it was asked and answered in its own format.
	if _, err := parse(stdout); err != nil && !hasStreamResult(stdout) {
		t.Fatalf("the installed client did not accept this command line: %v\nargs: %v", err, args)
	}
}

// claudeSmokeEnv opts into the one test here that starts a real turn against
// the installed client, and can therefore be billed.
const claudeSmokeEnv = "ATENEA_CLAUDECODE_SMOKE"

// claudeSmokeTimeout bounds it. A turn that has not printed its envelope by now
// is not going to, and an unbounded exec in a test suite is a hang waiting for
// a bad day.
const claudeSmokeTimeout = 90 * time.Second

func hasStreamResult(stdout []byte) bool {
	for _, line := range strings.Split(string(stdout), "\n") {
		event, ok, err := parseStreamLine(strings.TrimSpace(line))
		if err == nil && ok && (event.Type == "result" || event.Type == "") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("%s is not on the command line: %v", flag, args)
	return ""
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// requireClaude skips a test that needs the real client.
func requireClaude(t *testing.T) {
	t.Helper()
	if _, err := osexec.LookPath(DefaultBinary); err != nil {
		t.Skipf("claude is not installed on this machine: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The paths that come back
// ---------------------------------------------------------------------------

// The far side is asked for repository-relative paths and answers with
// absolute ones often enough to matter. Every one of those used to be dropped
// without a word -- not counted out of scope, not reported -- so a turn that
// found five matches answered VerdictOK with an empty list and the caller had
// no way to tell that from "nothing here".
func TestAnAbsoluteMatchInsideTheRepositoryIsResolvedNotDiscarded(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	root := req.Repository.Path
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":` + quoted(filepath.Join(root, "cmd", "main.go")) + `,"line":3,"column":1}
	]}`)}

	result, counts, err := runner.readAnswer(out, root, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches = %v, want the absolute hit resolved against the root", matches)
	}
	if got := matches[0].(map[string]any)["path"]; got != "cmd/main.go" {
		t.Fatalf("path = %v, want it made relative to the repository", got)
	}
	if counts.malformed != 0 {
		t.Fatalf("malformed = %d, want none", counts.malformed)
	}
}

// An absolute path that lands outside the repository is still refused -- and
// now it is counted, so the caller is told matches went missing instead of
// reading an empty list as an answer.
func TestAMatchThisAdapterCannotPlaceIsCountedAndReported(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"/etc/passwd","line":1,"column":1},
		{"path":"../sibling/other.go","line":1,"column":1},
		{"path":"a.go","line":0,"column":1}
	]}`)}

	result, counts, err := runner.readAnswer(out, req.Repository.Path, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	if matches, _ := result["matches"].([]any); len(matches) != 0 {
		t.Fatalf("matches = %v, want none", matches)
	}
	if counts.malformed != 3 {
		t.Fatalf("malformed = %d, want 3: an outside path and a nonsense line both count", counts.malformed)
	}
	if counts.outOfScope != 0 {
		t.Fatalf("outOfScope = %d, want 0: none of these fell outside a scope the caller set", counts.outOfScope)
	}
}

// Lexical containment cannot see a symlink. A directory inside the repository
// that points outside it produces a path that never climbs out of anything and
// still names a file the repository does not contain.
func TestAMatchReachedThroughASymlinkOutOfTheRepositoryIsRefused(t *testing.T) {
	runner := newTestRunner(t)
	req := request(t, map[string]any{"query": "x"})
	root := req.Repository.Path
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "vendor")); err != nil {
		t.Skipf("this filesystem cannot make the symlink the check exists for: %v", err)
	}
	out := envelope{StructuredOutput: json.RawMessage(`{"matches":[
		{"path":"vendor/secret.go","line":1,"column":1}
	]}`)}

	result, counts, err := runner.readAnswer(out, root, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("readAnswer: %v", err)
	}
	if matches, _ := result["matches"].([]any); len(matches) != 0 {
		t.Fatalf("matches = %v, want none: the path leaves the repository once the link is resolved", matches)
	}
	if counts.malformed != 1 {
		t.Fatalf("malformed = %d, want 1", counts.malformed)
	}
}

// quoted renders a path as a JSON string, which is the only safe way to put a
// Windows-or-Unix filesystem path inside a fixture.
func quoted(t string) string {
	encoded, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// A single line over the buffer limit used to throw the whole turn away --
// including a `result` event already read, already paid for -- and report the
// provider as unreachable. What arrived is kept; what did not is said out loud.
func TestALineTooLongKeepsTheAnswerThatAlreadyArrived(t *testing.T) {
	previous := maxStreamLine
	maxStreamLine = 8192
	t.Cleanup(func() { maxStreamLine = previous })

	final := `{"type":"result","is_error":false,"subtype":"success",` +
		`"structured_output":{"matches":[{"path":"cmd/main.go","line":3,"column":1}]},` +
		`"usage":{"input_tokens":10,"output_tokens":2},"total_cost_usd":0.01,"num_turns":2}`
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '" + final + "'\n" +
		// The oversized line arrives after the answer, which is the case that
		// matters: everything the caller asked for is already in hand.
		"awk 'BEGIN{s=\"\";while(length(s)<20000)s=s \"x\";print s}'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	runner, err := New(Options{Binary: path, Implementations: []string{"claude.search"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := runner.Run(context.Background(), request(t, map[string]any{"query": "TODO"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	matches, _ := out.Result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches = %v, want the answer that arrived before the stream broke", out.Result["matches"])
	}
	var said bool
	for _, notice := range out.Notices {
		if strings.Contains(notice, "cut short") {
			said = true
		}
	}
	if !said {
		t.Fatalf("notices = %v, want one saying the stream was cut short", out.Notices)
	}
}
