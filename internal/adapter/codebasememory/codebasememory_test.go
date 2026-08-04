package codebasememory

// The adapter's whole job is translation, so most of these tests are about
// the two directions of it: what a payload decodes into, and what one of
// codebase-memory-mcp's or git's own failures becomes. The full multi-call
// orchestration -- trace_path, query_graph and git diff chained together
// against a real graph -- is exercised end to end from cmd against this very
// repository; here the shapes and the wiring around them are pinned down.

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// --- construction ------------------------------------------------------

func TestNewFillsInDefaults(t *testing.T) {
	runner, err := New(Options{Implementations: []string{ImplCalls}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runner.binary != DefaultBinary {
		t.Errorf("binary = %q, want %q", runner.binary, DefaultBinary)
	}
	if runner.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", runner.timeout, DefaultTimeout)
	}
}

func TestNewRejectsANegativeTimeout(t *testing.T) {
	_, err := New(Options{Timeout: -time.Second})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
}

func TestNewRejectsABrokenSensitivePattern(t *testing.T) {
	_, err := New(Options{Sensitive: []string{"["}})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

func TestTheAdapterAnnouncesWhoItIsAndWhatItServes(t *testing.T) {
	runner := newTestRunner(t)
	if runner.ID() != "codebase-memory" {
		t.Errorf("ID = %q, want codebase-memory", runner.ID())
	}
	if !runner.Serves(ImplCalls) || !runner.Serves(ImplImpact) {
		t.Errorf("Serves is wrong: %v", runner.Implementations())
	}
	if runner.Serves("codebase-memory.search") {
		t.Error("this adapter must not claim code.search: three other providers already answer it")
	}
	want := []string{ImplCalls, ImplImpact}
	slices.Sort(want)
	if got := runner.Implementations(); !slices.Equal(got, want) {
		t.Errorf("Implementations = %v, want %v", got, want)
	}
}

func TestSensitiveListIsSorted(t *testing.T) {
	runner, err := New(Options{Sensitive: []string{"*.pem", ".env", "id_rsa"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := []string{".env", "*.pem", "id_rsa"}
	slices.Sort(want)
	if got := runner.Sensitive(); !slices.Equal(got, want) {
		t.Errorf("Sensitive = %v, want %v", got, want)
	}
}

func TestIsSensitiveMatchesBareNameAndRepositoryRelativePath(t *testing.T) {
	runner := newTestRunner(t)
	cases := map[string]bool{
		".env":                 true,
		"nested/.env":          true,
		"config/secret.pem":    true,
		"id_rsa":               true,
		"src/id_rsa":           true,
		"main.go":              false,
		"secret.pem.bak":       false,
		"credentials.json.txt": false,
	}
	for relative, want := range cases {
		if got := runner.isSensitive(relative); got != want {
			t.Errorf("isSensitive(%q) = %v, want %v", relative, got, want)
		}
	}
}

// --- payload readers -----------------------------------------------------

func TestPayloadReadersAcceptTheDecodedJSONShape(t *testing.T) {
	payload := map[string]any{
		"file":   "main.go",
		"strict": true,
		"scope":  []any{"internal", "pkg"},
		"depth":  float64(3), // every JSON number decodes as float64
	}
	if got := stringAt(payload, "file"); got != "main.go" {
		t.Errorf("stringAt = %q", got)
	}
	if got := stringAt(payload, "missing"); got != "" {
		t.Errorf("stringAt on a missing key = %q, want empty", got)
	}
	if got := stringAt(payload, "strict"); got != "" {
		t.Errorf("stringAt on a bool value = %q, want empty rather than a panic", got)
	}
	if !boolAt(payload, "strict") {
		t.Error("boolAt = false, want true")
	}
	if boolAt(payload, "missing") {
		t.Error("boolAt on a missing key, want false")
	}
	if got := stringsAt(payload, "scope"); !slices.Equal(got, []string{"internal", "pkg"}) {
		t.Errorf("stringsAt = %v", got)
	}
	if got := stringsAt(payload, "missing"); got != nil {
		t.Errorf("stringsAt on a missing key = %v, want nil", got)
	}
	if got, ok := intAt(payload, "depth"); !ok || got != 3 {
		t.Errorf("intAt(float64) = %v, %v, want 3, true", got, ok)
	}
	if _, ok := intAt(payload, "file"); ok {
		t.Error("intAt on a string value claimed success")
	}
	// A caller that hands over a native []string rather than a JSON-decoded
	// []any -- true of every unit test in this package -- must work too.
	if got := stringsAt(map[string]any{"scope": []string{"a", "b"}}, "scope"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("stringsAt([]string) = %v", got)
	}
	// A mixed array drops whatever is not a string rather than erroring: the
	// field is declared string_list, so anything else in it is malformed
	// input the reader is not the layer responsible for rejecting.
	if got := stringsAt(map[string]any{"scope": []any{"a", 1, "b"}}, "scope"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("stringsAt(mixed) = %v", got)
	}
}

// --- error mapping ---------------------------------------------------------

func TestFailureForSortsCodebaseMemoryErrorsIntoBins(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   contract.FailureKind
	}{
		{
			name:   "an unindexed repository is unavailable, not a bad request",
			stderr: `{"error":"Project not found: /repo","hint":"run index_repository first"}`,
			want:   contract.FailureUnavailable,
		},
		{
			name:   "a plain not-found with no project in the message is FailureNotFound",
			stderr: `{"error":"function not found"}`,
			want:   contract.FailureNotFound,
		},
		{
			name:   "permission denied",
			stderr: `{"error":"permission denied opening the graph database"}`,
			want:   contract.FailurePermissionDenied,
		},
		{
			name:   "eacces reads the same as the English phrase",
			stderr: `{"error":"open /var/lib/x: EACCES"}`,
			want:   contract.FailurePermissionDenied,
		},
		{
			name:   "a timed out query",
			stderr: `{"error":"query timed out after 30s"}`,
			want:   contract.FailureTimeout,
		},
		{
			name:   "a validation message from the CLI's own flag parsing",
			stderr: `{"error":"project is required"}`,
			want:   contract.FailureInvalidInput,
		},
		{
			name:   "an unrecognized message defaults to unavailable",
			stderr: `{"error":"panic: runtime error: index out of range"}`,
			want:   contract.FailureUnavailable,
		},
		{
			name:   "log lines before the JSON object are skipped, not treated as the message",
			stderr: "level=info msg=mem.init budget_mb=27801\n" + `{"error":"invalid project path"}`,
			want:   contract.FailureInvalidInput,
		},
		{
			name:   "no JSON at all still produces a failure instead of a panic",
			stderr: "codebase-memory-mcp: segmentation fault",
			want:   contract.FailureUnavailable,
		},
		{
			name:   "empty stderr does not crash the classifier",
			stderr: "",
			want:   contract.FailureUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := failureFor("query_graph", []byte(tc.stderr))
			if got := contract.KindOf(err); got != tc.want {
				t.Errorf("%q -> %v, want %v (err = %v)", tc.stderr, got, tc.want, err)
			}
		})
	}
}

func TestFailureForKeepsTheHintBesideTheMessage(t *testing.T) {
	err := failureFor("query_graph", []byte(`{"error":"project not found","hint":"run index_repository first"}`))
	if !strings.Contains(err.Error(), "run index_repository first") {
		t.Errorf("error dropped the hint: %v", err)
	}
}

func TestGitFailureForSortsGitErrorsIntoBins(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   contract.FailureKind
	}{
		{"unknown revision names a bad baseline", "fatal: unknown revision or path not in the working tree.", contract.FailureInvalidInput},
		{"bad revision", "fatal: bad revision 'nowhere~1'", contract.FailureInvalidInput},
		{"ambiguous argument", "fatal: ambiguous argument 'HEAD~999': unknown revision or path not in the working tree.", contract.FailureInvalidInput},
		{"not a git repository at all", "fatal: not a git repository (or any of the parent directories): .git", contract.FailureInvalidInput},
		{"an unrecognized git failure defaults to unavailable", "fatal: unable to read tree", contract.FailureUnavailable},
		{"empty stderr does not crash the classifier", "", contract.FailureUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := gitFailureFor(tc.stderr)
			if got := contract.KindOf(err); got != tc.want {
				t.Errorf("%q -> %v, want %v (err = %v)", tc.stderr, got, tc.want, err)
			}
		})
	}
}

// --- graph plumbing ---------------------------------------------------

func TestLineNumberAcceptsBothQuotedAndBareNumbers(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{"a bare JSON number", float64(42), 42},
		{"a number the graph quoted as a string property", "42", 42},
		{"whitespace around a quoted number is trimmed", " 42 ", 42},
		{"a non-numeric string is not a line", "abc", 0},
		{"nil is not a line", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lineNumber(tc.in); got != tc.want {
				t.Errorf("lineNumber(%#v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestCypherStringEscapesQuotes(t *testing.T) {
	if got := cypherString(`O'Brien`); got != `'O\'Brien'` {
		t.Errorf("cypherString = %s", got)
	}
	if got := cypherString("plain"); got != "'plain'" {
		t.Errorf("cypherString = %s", got)
	}
}

func TestInScopeMatchesPathPrefixesNotSubstrings(t *testing.T) {
	scope := []string{"internal", "pkg/contract"}
	cases := map[string]bool{
		"internal/core/core.go":        true,
		"internal2/core/core.go":       false, // a name prefix is not a path prefix
		"pkg/contract/capability.go":   true,
		"pkg/contract2/capability.go":  false,
		"pkg/contractor/capability.go": false,
		"pkg/other/capability.go":      false,
		"internal":                     true, // the scope entry itself
		"cmd/atenea/main.go":           false,
	}
	for path, want := range cases {
		if got := inScope(path, scope); got != want {
			t.Errorf("inScope(%q, %v) = %v, want %v", path, scope, got, want)
		}
	}
	if !inScope("anywhere/at/all.go", nil) {
		t.Error("an empty scope must mean everywhere")
	}
}

// --- Run's own guards -------------------------------------------------

func TestRunRejectsAnImplementationItDoesNotServe(t *testing.T) {
	payload := map[string]any{"file": "main.go", "line": 1, "column": 1, "direction": "both"}
	req := request(t, symbolCallsCapability(), "unknown.impl", payload)
	_, err := newTestRunner(t).Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
}

func TestRunRejectsAnImplementationItDoesNotServeForCodeImpact(t *testing.T) {
	payload := map[string]any{"baseline": "HEAD~1", "scope": []string{"internal"}, "depth": 2}
	req := request(t, codeImpactCapability(), "unknown.impl", payload)
	_, err := newTestRunner(t).Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
}

func TestRunRejectsACapabilityItHasNoCodeFor(t *testing.T) {
	capability := symbolCallsCapability()
	capability.ID = "code.search"
	capability.Inputs = []contract.Field{{Name: "query", Type: contract.TypeString, Required: true}}
	req := contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: "codebase-memory.search", Provider: "codebase-memory", Capability: "code.search"},
		Repository:     contract.NewRepository("current", t.TempDir(), []string{"go"}, contract.ScaleSmall, nil),
		Payload:        map[string]any{"query": "x"},
		Permission:     contract.Permission{Task: "probe", Effects: []contract.Effect{contract.EffectRead}},
	}
	runner, err := New(Options{Implementations: []string{"codebase-memory.search"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", got, err)
	}
}

// --- invoke's and git's own subprocess plumbing ----------------------

// A fake codebase-memory-mcp: $2 is the tool name ("cli" is $1), stdin is
// ignored, and the response is looked up by tool name. Any tool with no
// entry exits non-zero with a canned error envelope, the same shape the
// real CLI uses.
func fakeCodebaseMemory(t *testing.T, responses map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary below is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codebase-memory-mcp")
	var script strings.Builder
	script.WriteString("#!/bin/sh\ncat >/dev/null\ncase \"$2\" in\n")
	for tool, body := range responses {
		script.WriteString(tool + ") cat <<'EOF'\n" + body + "\nEOF\nexit 0 ;;\n")
	}
	script.WriteString(`*) echo '{"error":"unknown tool"}' >&2; exit 1 ;;` + "\nesac\n")
	if err := os.WriteFile(path, []byte(script.String()), 0o700); err != nil {
		t.Fatalf("writing the fake binary: %v", err)
	}
	return path
}

func TestInvokeRoutesANonZeroExitThroughFailureFor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codebase-memory-mcp")
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"error\":\"project not found: /repo\"}' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake binary: %v", err)
	}
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.invoke(context.Background(), "query_graph", map[string]any{"project": "/repo"}, &meter{})
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
	if !strings.Contains(err.Error(), "project not found") {
		t.Errorf("the real message was not carried through: %v", err)
	}
}

func TestInvokeReportsAMissingBinaryAsUnavailable(t *testing.T) {
	runner, err := New(Options{Binary: filepath.Join(t.TempDir(), "does-not-exist"), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.invoke(context.Background(), "query_graph", map[string]any{}, &meter{})
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
}

func TestInvokeReportsASuccessfulCallVerbatim(t *testing.T) {
	path := fakeCodebaseMemory(t, map[string]string{"query_graph": `{"total":0,"columns":[],"rows":[]}`})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	weight := &meter{}
	raw, err := runner.invoke(context.Background(), "query_graph", map[string]any{}, weight)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(string(raw), `"total":0`) {
		t.Errorf("raw = %s", raw)
	}
	// procstat measures a real child process, so a successful run must have
	// something to report -- a literal zero would read as "never measured".
	if weight.peak <= 0 {
		t.Errorf("peak RSS = %d, want a positive reading from the child process", weight.peak)
	}
}

func TestGitRoutesANonZeroExitThroughGitFailureFor(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := initGitRepo(t)
	runner := newTestRunner(t)
	_, err := runner.git(context.Background(), root, []string{"diff", "--unified=0", "not-a-real-revision"}, &meter{})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
}

func TestGitReportsAMissingRepositoryAsInvalidInput(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	runner := newTestRunner(t)
	_, err := runner.git(context.Background(), t.TempDir(), []string{"diff", "--unified=0", "HEAD"}, &meter{})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input for a directory that is not a git repository (err = %v)", got, err)
	}
}

// --- freshnessNotice ------------------------------------------------------

func TestFreshnessNoticeIsEmptyWhenTheIndexMatchesAndTheTreeIsClean(t *testing.T) {
	root := initGitRepo(t)
	path := fakeCodebaseMemory(t, map[string]string{
		"index_status": `{"git":{"is_git":true,"head_sha":"abc111","base_sha":"abc111"}}`,
	})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := runner.freshnessNotice(context.Background(), root); got != "" {
		t.Errorf("freshnessNotice = %q, want empty: HEAD matches the index and the tree is clean", got)
	}
}

func TestFreshnessNoticeFlagsAMovedHead(t *testing.T) {
	root := initGitRepo(t)
	path := fakeCodebaseMemory(t, map[string]string{
		"index_status": `{"git":{"is_git":true,"head_sha":"abc111","base_sha":"def222"}}`,
	})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := runner.freshnessNotice(context.Background(), root)
	if !strings.Contains(got, "HEAD has moved") {
		t.Errorf("freshnessNotice = %q, want it to mention HEAD having moved", got)
	}
	if strings.Contains(got, "uncommitted") {
		t.Errorf("freshnessNotice = %q, the tree is clean, this must not mention it", got)
	}
}

func TestFreshnessNoticeFlagsADirtyTree(t *testing.T) {
	root := initGitRepo(t)
	writeFile(t, root, "untracked.go", "package x\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"index_status": `{"git":{"is_git":true,"head_sha":"abc111","base_sha":"abc111"}}`,
	})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := runner.freshnessNotice(context.Background(), root)
	if !strings.Contains(got, "uncommitted changes") {
		t.Errorf("freshnessNotice = %q, want it to mention the uncommitted file", got)
	}
	if strings.Contains(got, "HEAD has moved") {
		t.Errorf("freshnessNotice = %q, HEAD did not move, this must not claim it did", got)
	}
}

func TestFreshnessNoticeFlagsBothAtOnce(t *testing.T) {
	root := initGitRepo(t)
	writeFile(t, root, "untracked.go", "package x\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"index_status": `{"git":{"is_git":true,"head_sha":"abc111","base_sha":"def222"}}`,
	})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := runner.freshnessNotice(context.Background(), root)
	if !strings.Contains(got, "HEAD has moved") || !strings.Contains(got, "uncommitted changes") {
		t.Errorf("freshnessNotice = %q, want both the moved HEAD and the dirty tree named", got)
	}
}

// A freshness check that cannot get an answer is not a reason to invent one.
// index_status erroring is treated exactly like index_status saying nothing
// was wrong: both report no notice, because a caller cannot act on the
// difference between "checked and clean" and "could not check".
func TestFreshnessNoticeIsEmptyWhenIndexStatusFails(t *testing.T) {
	root := initGitRepo(t)
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"total":0,"columns":[],"rows":[]}`, // anything but index_status
	})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := runner.freshnessNotice(context.Background(), root); got != "" {
		t.Errorf("freshnessNotice = %q, want empty when index_status itself errors", got)
	}
}

// A repository with nothing to compare against -- not a git repository at
// all -- has no basis for a staleness claim either way.
func TestFreshnessNoticeIsEmptyForANonGitRepository(t *testing.T) {
	root := t.TempDir()
	path := fakeCodebaseMemory(t, map[string]string{
		"index_status": `{"git":{"is_git":false,"head_sha":"","base_sha":""}}`,
	})
	runner, err := New(Options{Binary: path, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := runner.freshnessNotice(context.Background(), root); got != "" {
		t.Errorf("freshnessNotice = %q, want empty: nothing here is a git repository", got)
	}
}

// --- helpers ------------------------------------------------------------

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := New(Options{
		Implementations: []string{ImplCalls, ImplImpact},
		Sensitive:       []string{".env", "*.pem", "id_rsa"},
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

// symbolCallsCapability mirrors the shipped capability, because the adapter
// has to produce something that passes the declared output schema, not
// something that merely looks right.
func symbolCallsCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilitySymbolCalls,
		Version: contract.Version{Major: 1},
		Summary: "From a symbol, walk the call graph in a chosen direction.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true},
			{Name: "line", Type: contract.TypeInt, Required: true},
			{Name: "column", Type: contract.TypeInt, Required: true},
			{Name: "name", Type: contract.TypeString},
			{Name: "direction", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "depth", Type: contract.TypeInt},
			{Name: "include_snippet", Type: contract.TypeBool},
			{Name: "snippet_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{{
			Name: "calls", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "name", Type: contract.TypeString, Required: true},
				{Name: "direction", Type: contract.TypeString, Required: true},
				{Name: "depth", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			},
		}},
	}
}

// codeImpactCapability mirrors the shipped capability, the same reason
// symbolCallsCapability does.
func codeImpactCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityCodeImpact,
		Version: contract.Version{Major: 1},
		Summary: "From a change, find what it reaches.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "baseline", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "depth", Type: contract.TypeInt},
			{Name: "include_snippet", Type: contract.TypeBool},
			{Name: "snippet_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{
			{Name: "changed_files", Type: contract.TypeStringList, Required: true},
			{
				Name: "affected_symbols", Type: contract.TypeRecordList, Required: true,
				Fields: []contract.Field{
					{Name: "path", Type: contract.TypeString, Required: true},
					{Name: "line", Type: contract.TypeInt, Required: true},
					{Name: "name", Type: contract.TypeString, Required: true},
					{Name: "kind", Type: contract.TypeString},
					{Name: "depth", Type: contract.TypeInt, Required: true},
					{Name: "snippet", Type: contract.TypeString},
				},
			},
		},
	}
}

func request(t *testing.T, capability contract.Capability, implementationID string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return requestAt(t, t.TempDir(), capability, implementationID, payload)
}

func requestAt(t *testing.T, root string, capability contract.Capability, implementationID string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: implementationID, Provider: "codebase-memory", Capability: capability.ID},
		Repository:     contract.NewRepository("current", root, []string{"go"}, contract.ScaleSmall, nil),
		Payload:        payload,
		Permission:     contract.Permission{Task: "probe", Effects: []contract.Effect{contract.EffectRead}},
	}
}

// initGitRepo creates a fresh, committed repository in a temp dir, so a test
// can diff a real one without any state left over from another test.
func initGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := osexec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	return root
}
