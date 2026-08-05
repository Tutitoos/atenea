package local_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/runner/local"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// codeSearch mirrors the shipped capability, because the stand-in has to
// produce something that passes the declared output schema, not something that
// merely looks right.
func codeSearch() contract.Capability {
	return contract.Capability{
		ID:      "code.search",
		Version: contract.Version{Major: 1},
		Summary: "Find literal text in a repository.",
		Effects: []contract.Effect{contract.EffectRead},
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
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "column", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			},
		}},
	}
}

func ripgrep() contract.Implementation {
	return contract.Implementation{ID: "ripgrep", Provider: "ripgrep", Capability: "code.search"}
}

// fixture writes a small repository and returns its path.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"README.md":             "The login page lives in web.\n",
		"web/login.ts":          "// login form\nexport function Login() {\n  return LOGIN;\n}\n",
		"api/auth.go":           "package auth\n\n// Login issues a token.\nfunc Login() {}\n",
		"api/unrelated.go":      "package auth\n\nfunc Logout() {}\n",
		".env":                  "SECRET_LOGIN_TOKEN=hunter2\n",
		"config/service.pem":    "-----BEGIN KEY-----\nlogin\n",
		"deep/nested/notes.txt": "login again\n",
		".git/refs/heads/main":  "login\n",
		"node_modules/dep/x.js": "login\n",
	}
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A binary file: text search inside one finds noise, never signal.
	if err := os.WriteFile(filepath.Join(root, "logo.png"),
		[]byte("\x89PNG\x00\x00login\x00"), 0o600); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	return root
}

func newRunner(t *testing.T) *local.Runner {
	t.Helper()
	runner, err := local.New(local.Options{
		Implementations: []string{"ripgrep"},
		Sensitive:       []string{".env", "*.pem"},
		SkipDirs:        []string{".git", "node_modules"},
	})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	return runner
}

func request(t *testing.T, root string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     codeSearch(),
		Implementation: ripgrep(),
		Repository:     contract.NewRepository("api", root, []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
		Payload:        payload,
		Permission:     contract.Permission{Task: "find login", Effects: []contract.Effect{contract.EffectRead}},
	}
}

// paths returns the repository-relative path of every match, so a test can
// assert on where the hits are rather than on how many there happen to be.
func paths(t *testing.T, outcome contract.Outcome) []string {
	t.Helper()
	raw, ok := outcome.Result["matches"].([]any)
	if !ok {
		t.Fatalf("matches missing from %v", outcome.Result)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		record, isRecord := item.(map[string]any)
		if !isRecord {
			t.Fatalf("match is not a record: %v", item)
		}
		out = append(out, record["path"].(string))
	}
	return out
}

func has(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestSearchFindsTextAndHonoursTheOutputSchema(t *testing.T) {
	root := fixture(t)
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "login"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", outcome.Verdict)
	}
	// Run validates its own output before returning; doing it again here is
	// what makes that guarantee a contract rather than an implementation habit.
	if err := codeSearch().ValidateOutput(outcome.Result); err != nil {
		t.Fatalf("output does not match the capability: %v", err)
	}
	found := paths(t, outcome)
	for _, want := range []string{"README.md", "web/login.ts", "api/auth.go", "deep/nested/notes.txt"} {
		if !has(found, want) {
			t.Errorf("%s has a hit and was not reported; got %v", want, found)
		}
	}
	if has(found, "api/unrelated.go") {
		t.Errorf("a file with no hit was reported: %v", found)
	}
	if outcome.Spent.Duration <= 0 {
		t.Error("exploring costs time and the measurement has to say so")
	}
}

// Skipping sensitive files is silent by design: no question, and nothing
// written down about the skip either.
func TestSensitiveFilesAreSkippedWithoutATrace(t *testing.T) {
	root := fixture(t)
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "login"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := paths(t, outcome)
	for _, secret := range []string{".env", "config/service.pem"} {
		if has(found, secret) {
			t.Errorf("%s carries secrets and was searched anyway: %v", secret, found)
		}
	}
	if len(outcome.Discoveries) != 0 {
		t.Errorf("skipping a sensitive file must not be written down, got %v", outcome.Discoveries)
	}
}

func TestSkippedDirectoriesAreNotDescendedInto(t *testing.T) {
	root := fixture(t)
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "login"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range paths(t, outcome) {
		if strings.HasPrefix(name, ".git/") || strings.HasPrefix(name, "node_modules/") {
			t.Errorf("descended into a skipped directory: %s", name)
		}
	}
}

func TestBinaryFilesAreNotSearched(t *testing.T) {
	root := fixture(t)
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "login"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if has(paths(t, outcome), "logo.png") {
		t.Error("a binary file was searched for text")
	}
}

func TestScopeNarrowsTheWalk(t *testing.T) {
	root := fixture(t)
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{
		"query": "login",
		"scope": []any{"api"},
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := paths(t, outcome)
	if len(found) == 0 {
		t.Fatal("the scope was narrowed to a directory that has a hit")
	}
	for _, name := range found {
		if !strings.HasPrefix(name, "api/") {
			t.Errorf("scope was api and %s came back", name)
		}
	}
}

// Reading outside the unit of work is outside the commission, whatever the
// path looks like.
func TestScopeCannotClimbOutOfTheRepository(t *testing.T) {
	root := fixture(t)
	_, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{
		"query": "login",
		"scope": []any{"../.."},
	}))
	if err == nil {
		t.Fatal("a scope leaving the repository has to be refused")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
}

func TestFileTypesRestrictTheSearch(t *testing.T) {
	root := fixture(t)
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{
		"query":      "login",
		"file_types": []any{"go"},
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := paths(t, outcome)
	if len(found) == 0 {
		t.Fatal("there is a Go file with a hit")
	}
	for _, name := range found {
		if filepath.Ext(name) != ".go" {
			t.Errorf("file_types was go and %s came back", name)
		}
	}
}

// match_case is declared as the INTENT to distinguish case, so leaving it out
// means the caller does not care.
func TestCaseIntentIsHonoured(t *testing.T) {
	root := fixture(t)
	runner := newRunner(t)

	insensitive, err := runner.Run(t.Context(), request(t, root, map[string]any{"query": "LOGIN"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(paths(t, insensitive)) == 0 {
		t.Fatal("without match_case, LOGIN has to find login")
	}

	sensitive, err := runner.Run(t.Context(), request(t, root, map[string]any{
		"query":      "LOGIN",
		"match_case": true,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := paths(t, sensitive)
	if len(found) != 1 || found[0] != "web/login.ts" {
		t.Fatalf("with match_case only the literal LOGIN matches, got %v", found)
	}
}

func TestWholeWordAndRegexIntents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("log\nlogin\nlogging\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	runner := newRunner(t)

	whole, err := runner.Run(t.Context(), request(t, root, map[string]any{
		"query":      "log",
		"whole_word": true,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(paths(t, whole)); got != 1 {
		t.Fatalf("whole_word matches = %d, want only the bare word", got)
	}

	pattern, err := runner.Run(t.Context(), request(t, root, map[string]any{
		"query": "log(in|ging)",
		"regex": true,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(paths(t, pattern)); got != 2 {
		t.Fatalf("regex matches = %d, want login and logging", got)
	}

	// Without the regex intent the same text is a literal, and there is no
	// line that contains those brackets.
	literal, err := runner.Run(t.Context(), request(t, root, map[string]any{"query": "log(in|ging)"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(paths(t, literal)); got != 0 {
		t.Fatalf("literal matches = %d, want none", got)
	}
}

func TestSnippetCarriesTheAskedForContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"),
		[]byte("one\ntwo\nlogin\nfour\nfive\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	runner := newRunner(t)

	for _, tc := range []struct{ lines, want int }{{0, 1}, {1, 3}, {2, 5}} {
		outcome, err := runner.Run(t.Context(), request(t, root, map[string]any{
			"query":         "login",
			"context_lines": tc.lines,
		}))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		matches := outcome.Result["matches"].([]any)
		snippet := matches[0].(map[string]any)["snippet"].(string)
		if got := len(strings.Split(snippet, "\n")); got != tc.want {
			t.Errorf("context_lines=%d gave %d snippet line(s), want %d", tc.lines, got, tc.want)
		}
	}
}

func TestMatchPositionIsOneBased(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("xxlogin\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "login"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	record := outcome.Result["matches"].([]any)[0].(map[string]any)
	if record["line"] != 1 {
		t.Errorf("line = %v, want 1", record["line"])
	}
	if record["column"] != 3 {
		t.Errorf("column = %v, want 3", record["column"])
	}
}

// A provider this runner cannot reach is not a crash: it is the bin that
// drives fallback.
func TestUnservedImplementationIsUnavailable(t *testing.T) {
	root := fixture(t)
	req := request(t, root, map[string]any{"query": "login"})
	req.Implementation = contract.Implementation{
		ID: "serena.search", Provider: "serena", Capability: "code.search",
	}
	_, err := newRunner(t).Run(t.Context(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
}

func TestUnknownCapabilityIsNotFound(t *testing.T) {
	root := fixture(t)
	req := request(t, root, map[string]any{"symbol": "Login"})
	req.Capability = contract.Capability{
		ID:      "symbol.definition",
		Version: contract.Version{Major: 1},
		Summary: "Resolve a definition.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs:  []contract.Field{{Name: "symbol", Type: contract.TypeString, Required: true}},
		Outputs: []contract.Field{{Name: "location", Type: contract.TypeString, Required: true}},
	}
	req.Implementation.Capability = "symbol.definition"
	_, err := newRunner(t).Run(t.Context(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

// The commission stamps what is allowed, and the runner obeys the stamp
// instead of deciding for itself.
func TestAnEffectOutsideTheCommissionIsRefused(t *testing.T) {
	root := fixture(t)
	req := request(t, root, map[string]any{"query": "login"})
	req.Permission = contract.Permission{Task: "find login"} // nothing granted
	_, err := newRunner(t).Run(t.Context(), req)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
}

// A failed attempt is still a measurement, and a measurement filed with no
// version cannot be told apart from the release before it. The case worth
// catching -- an upgrade that started failing -- is exactly the one where
// there is no successful outcome to carry the string.
func TestAFailedRunStillReportsWhoFailed(t *testing.T) {
	root := fixture(t)
	req := request(t, root, map[string]any{"query": "login"})
	req.Permission = contract.Permission{Task: "find login"} // nothing granted
	outcome, err := newRunner(t).Run(t.Context(), req)
	if err == nil {
		t.Fatal("the refusal stopped happening; this test is about the refusal")
	}
	if outcome.ToolVersion == "" {
		t.Error("a refused call was filed with no version at all")
	}
}

// A repository whose path cannot be read was never searched, and answering
// "nothing found" for it is a lie the measurement base would file as the
// cheapest, fastest call there is. One unreadable directory inside a real tree
// is still forgiven; the root is not.
func TestARepositoryThatCannotBeReadIsNotAnEmptyResult(t *testing.T) {
	req := request(t, filepath.Join(t.TempDir(), "nowhere"), map[string]any{"query": "login"})
	_, err := newRunner(t).Run(t.Context(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

// The forgiveness that stays: a directory inside the repository that cannot be
// listed is skipped, and the rest of the search still answers.
func TestAnUnreadableDirectoryInsideTheTreeIsSkipped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("login\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	outcome, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "login"}))
	if err != nil {
		t.Fatalf("one unreadable directory sank the whole search: %v", err)
	}
	matches, _ := outcome.Result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("%d matches, want the one outside the locked directory", len(matches))
	}
}

func TestPayloadIsCheckedAgainstTheDeclaredInputs(t *testing.T) {
	root := fixture(t)
	cases := map[string]map[string]any{
		"missing the query":  {},
		"an unknown field":   {"query": "login", "recursive": true},
		"the wrong type":     {"query": 7},
		"a negative context": {"query": "login", "context_lines": -1},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := newRunner(t).Run(t.Context(), request(t, root, payload))
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input", got)
			}
		})
	}
}

func TestCancellationStopsTheWalk(t *testing.T) {
	root := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newRunner(t).Run(ctx, request(t, root, map[string]any{"query": "login"}))
	if err == nil {
		t.Fatal("a canceled search has to stop")
	}
	if got := contract.KindOf(err); got != contract.FailureTimeout {
		t.Fatalf("kind = %v, want timeout", got)
	}
}

func TestRunnerDescribesItself(t *testing.T) {
	runner := newRunner(t)
	if runner.ID() != "local" {
		t.Errorf("ID = %q, want local", runner.ID())
	}
	if !runner.Serves("ripgrep") || runner.Serves("serena.search") {
		t.Errorf("Serves is wrong: %v", runner.Implementations())
	}
	if got := runner.Sensitive(); len(got) != 2 {
		t.Errorf("Sensitive = %v, want the two configured patterns", got)
	}
}

func TestBadSensitivePatternIsRefusedAtStartup(t *testing.T) {
	if _, err := local.New(local.Options{Sensitive: []string{"[unclosed"}}); err == nil {
		t.Fatal("a pattern that can never match has to be caught at startup")
	}
}
