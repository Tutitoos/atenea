package workspacecontext_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
	"github.com/Tutitoos/atenea/internal/workspacecontext"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func target(t *testing.T, id string) workspacecontext.Target {
	t.Helper()
	return workspacecontext.Target{ID: id, Root: t.TempDir(), Payload: map[string]any{"query": id}}
}

func readPermission() contract.Permission {
	return contract.Permission{Task: "workspace.context", Effects: []contract.Effect{contract.EffectRead}}
}

func runRequest(targets ...workspacecontext.Target) workspacecontext.Request {
	return workspacecontext.Request{Targets: targets, Permission: readPermission()}
}

func TestRunPreflightsEveryTargetBeforeDispatch(t *testing.T) {
	first, second := target(t, "one"), target(t, "two")
	var calls atomic.Int32
	coordinator, err := workspacecontext.New(workspacecontext.Config{MaxParallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Run(context.Background(), runRequest(first, second), func(got workspacecontext.Target) error {
		if got.ID == "two" {
			return errors.New("denied")
		}
		return nil
	}, func(context.Context, workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		calls.Add(1)
		return workspacecontext.ChildResult{}, nil
	})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d; authorization must precede dispatch", err, calls.Load())
	}
}

func TestRunRejectsDuplicateIDsAliasesAndForeignCursorsWithoutDispatch(t *testing.T) {
	base := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  workspacecontext.Request
	}{
		{name: "duplicate id", req: runRequest(workspacecontext.Target{ID: "same", Root: base}, workspacecontext.Target{ID: "same", Root: t.TempDir()})},
		{name: "physical alias", req: runRequest(workspacecontext.Target{ID: "one", Root: base}, workspacecontext.Target{ID: "two", Root: alias})},
		{name: "foreign cursor", req: workspacecontext.Request{Targets: []workspacecontext.Target{{ID: "one", Root: base}}, Continuations: []workspacecontext.Continuation{{RepositoryID: "other", Cursor: "c2"}}, Permission: readPermission()}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			_, err := workspacecontext.Run(context.Background(), test.req, nil, func(context.Context, workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
				calls.Add(1)
				return workspacecontext.ChildResult{}, nil
			})
			if err == nil || calls.Load() != 0 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestRunRejectsCommonAndConflictingCursorsBeforeActivityOrDispatch(t *testing.T) {
	one, two := target(t, "one"), target(t, "two")
	request := runRequest(one, two)
	request.Payload = map[string]any{"cursor": "common"}
	var activity, calls atomic.Int32
	_, err := workspacecontext.Run(context.Background(), request, nil, func(context.Context, workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		calls.Add(1)
		return workspacecontext.ChildResult{}, nil
	})
	if err == nil || calls.Load() != 0 || activity.Load() != 0 {
		t.Fatalf("common cursor was fanned out: err=%v calls=%d activity=%d", err, calls.Load(), activity.Load())
	}

	request = runRequest(target(t, "one"))
	request.Targets[0].Payload = map[string]any{"cursor": "payload"}
	request.Continuations = []workspacecontext.Continuation{{RepositoryID: "one", Cursor: "continuation"}}
	_, err = workspacecontext.Run(context.Background(), request, nil, func(context.Context, workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		calls.Add(1)
		return workspacecontext.ChildResult{}, nil
	})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("conflicting continuation was fanned out: err=%v calls=%d", err, calls.Load())
	}
}

func TestRunResolvesStandingBudgetOnceAndSharesIt(t *testing.T) {
	one, two := target(t, "one"), target(t, "two")
	coordinator, err := workspacecontext.New(workspacecontext.Config{MaxParallel: 2, StandingBudgetUSD: 4})
	if err != nil {
		t.Fatal(err)
	}
	var shares []float64
	var mu sync.Mutex
	_, err = coordinator.Run(context.Background(), runRequest(one, two), nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		mu.Lock()
		shares = append(shares, child.Permission.BudgetUSD)
		mu.Unlock()
		return workspacecontext.ChildResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 2 || shares[0] == 0 || shares[1] == 0 || shares[0]+shares[1] != 4 {
		t.Fatalf("standing budget shares = %v, want two positive shares totaling 4", shares)
	}
}

func TestRunSanitizesDiagnosticsAndDoesNotExposeRoots(t *testing.T) {
	targetOne := target(t, "one")
	result, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{Notices: []string{"token=secret-value"}}, errors.New("provider token=secret-value failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Repositories[0].Error, "secret-value") || strings.Contains(strings.Join(result.Repositories[0].Notices, " "), "secret-value") {
		t.Fatalf("diagnostic secret leaked: %+v", result.Repositories[0])
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), targetOne.Root) {
		t.Fatalf("physical root leaked in result: %s", encoded)
	}
}

func TestRunSanitizesNestedResultPathsWithoutDestroyingRelativePaths(t *testing.T) {
	root := t.TempDir()
	targetOne := workspacecontext.Target{ID: "api", Root: root}
	request := runRequest(targetOne)
	result, err := workspacecontext.Run(context.Background(), request, nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		cyclic := map[string]any{"self": nil}
		cyclic["self"] = cyclic
		return workspacecontext.ChildResult{
			Result: map[string]any{
				"absolute": filepath.Join(root, "internal", "api.go"),
				"nested":   []any{map[string]any{"message": "opened " + filepath.Join(root, "README.md")}, cyclic},
			},
			Evidence: []contract.QueryEvidence{{NextCursor: filepath.Join(root, "cursor-1"), Completeness: "complete"}},
			Notices:  []string{"checked " + filepath.Join(root, "go.mod")},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].NextCursor != filepath.Join(root, "cursor-1") || result.Repositories[0].Evidence[0].NextCursor != filepath.Join(root, "cursor-1") {
		t.Fatalf("opaque cursor changed: %+v", result.Repositories[0])
	}
	encoded, err := json.Marshal(result.Repositories[0].Result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, root) {
		t.Fatalf("nested physical root leaked: %s", text)
	}
	for _, want := range []string{"api/internal/api.go", "api/README.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("sanitized relative reference %q missing: %s", want, text)
		}
	}
	if !strings.Contains(strings.Join(result.Repositories[0].Notices, " "), "api/go.mod") {
		t.Fatalf("sanitized notice missing: %+v", result.Repositories[0].Notices)
	}
}

func TestRunPreservesOpaqueCursorsAcrossRowsAndNextDispatch(t *testing.T) {
	root := t.TempDir()
	firstCursor := "  opaque:" + filepath.Join(root, "cursor") + "?page=1  "
	secondCursor := "opaque:" + filepath.Join(root, "cursor") + "?page=2"
	firstTarget := workspacecontext.Target{ID: "api", Root: root, Cursor: firstCursor}
	var seen []string
	var mu sync.Mutex
	first, err := workspacecontext.Run(context.Background(), runRequest(firstTarget), nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		mu.Lock()
		seen = append(seen, child.Cursor)
		mu.Unlock()
		return workspacecontext.ChildResult{
			NextCursor: secondCursor,
			Evidence:   []contract.QueryEvidence{{NextCursor: secondCursor, Completeness: "lower_bound"}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Repositories[0].Cursor != firstCursor || first.Repositories[0].NextCursor != secondCursor || first.Repositories[0].Evidence[0].NextCursor != secondCursor {
		t.Fatalf("opaque cursor changed on output: %+v", first.Repositories[0])
	}
	secondTarget := workspacecontext.Target{ID: "api", Root: root}
	request := runRequest(secondTarget)
	request.Continuations = []workspacecontext.Continuation{{RepositoryID: "api", Cursor: secondCursor}}
	_, err = workspacecontext.Run(context.Background(), request, nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		mu.Lock()
		seen = append(seen, child.Cursor)
		mu.Unlock()
		return workspacecontext.ChildResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != firstCursor || seen[1] != secondCursor {
		t.Fatalf("opaque cursor changed before provider dispatch: %#v", seen)
	}
}

func TestRunContinuationComparesPayloadCursorByteExactly(t *testing.T) {
	root := t.TempDir()
	exact := "  opaque cursor  "
	targetOne := workspacecontext.Target{ID: "api", Root: root, Payload: map[string]any{"cursor": exact}}
	request := runRequest(targetOne)
	request.Continuations = []workspacecontext.Continuation{{RepositoryID: "api", Cursor: exact}}
	var seen string
	result, err := workspacecontext.Run(context.Background(), request, nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		seen = child.Cursor
		return workspacecontext.ChildResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Partial || seen != exact {
		t.Fatalf("exact opaque cursor was not preserved: result=%+v seen=%q", result, seen)
	}

	different := runRequest(workspacecontext.Target{ID: "api", Root: root, Payload: map[string]any{"cursor": exact}})
	different.Continuations = []workspacecontext.Continuation{{RepositoryID: "api", Cursor: strings.TrimSpace(exact)}}
	calls := 0
	if _, err := workspacecontext.Run(context.Background(), different, nil, func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		calls++
		return workspacecontext.ChildResult{}, nil
	}); err == nil || calls != 0 {
		t.Fatalf("different opaque cursor should fail before dispatch: err=%v calls=%d", err, calls)
	}
}

func TestRunBoundsOversizedCursorsWithoutDispatchOrPublication(t *testing.T) {
	root := t.TempDir()
	huge := strings.Repeat("c", 2<<20)
	inputCases := []workspacecontext.Request{
		runRequest(workspacecontext.Target{ID: "api", Root: root, Cursor: huge}),
		runRequest(workspacecontext.Target{ID: "api", Root: root, Payload: map[string]any{"cursor": huge}}),
	}
	continuation := runRequest(workspacecontext.Target{ID: "api", Root: root})
	continuation.Continuations = []workspacecontext.Continuation{{RepositoryID: "api", Cursor: huge}}
	inputCases = append(inputCases, continuation)
	for index, request := range inputCases {
		calls := 0
		if _, err := workspacecontext.Run(context.Background(), request, nil, func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
			calls++
			return workspacecontext.ChildResult{}, nil
		}); err == nil || calls != 0 {
			t.Fatalf("oversized input case %d was not rejected before dispatch: err=%v calls=%d", index, err, calls)
		}
	}

	targetOne := workspacecontext.Target{ID: "api", Root: root}
	first, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{
			NextCursor: huge,
			Evidence:   []contract.QueryEvidence{{NextCursor: huge, Completeness: "complete"}},
			Result:     map[string]any{"next_cursor": huge},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	row := first.Repositories[0]
	if row.NextCursor != "" || row.Evidence[0].NextCursor != "" || !row.Partial || !row.Truncated || row.Error == "" {
		t.Fatalf("oversized output cursor was published or not classified: %+v", row)
	}
	if !strings.Contains(strings.Join(row.Notices, " "), "cursor omitted") {
		t.Fatalf("oversized cursor diagnostic missing: %+v", row.Notices)
	}
	second, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{NextCursor: "small-cursor"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Repositories[0].NextCursor != "small-cursor" || second.Repositories[0].Partial || second.Repositories[0].Truncated {
		t.Fatalf("normal cursor after oversized output was not usable: %+v", second.Repositories[0])
	}
}

func TestRunMarksLongTextLossAsPartialAndTruncated(t *testing.T) {
	targetOne := target(t, "api")
	result, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{
			Result:  map[string]any{"text": strings.Repeat("x", 2<<20)},
			Notices: []string{strings.Repeat("notice ", 20000)},
		}, errors.New(strings.Repeat("error ", 20000))
	})
	if err != nil {
		t.Fatal(err)
	}
	row := result.Repositories[0]
	if !row.Partial || !row.Truncated || !strings.Contains(strings.Join(row.Notices, " "), "result sanitized") {
		t.Fatalf("long text loss must be visible: %+v", row)
	}
	if len(row.Error) > contract.MaxPersistedRaw+32 || !strings.HasSuffix(row.Error, "[TRUNCATED]") {
		t.Fatalf("error was not bounded safely: length=%d", len(row.Error))
	}
}

func TestRunSanitizerReportsDeterministicKeyCollisions(t *testing.T) {
	targetOne := target(t, "api")
	root := targetOne.Root
	collision := map[string]string{
		filepath.Join(root, "a.go"): "physical",
		"api/a.go":                  "alias",
		"token=uno":                 "first-secret",
		"token=dos":                 "second-secret",
	}
	dispatch := func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{Result: map[string]any{"collision": collision}}, nil
	}
	first, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	row := first.Repositories[0]
	if !row.Partial || !row.Truncated {
		t.Fatalf("key collision must make row incomplete: %+v", row)
	}
	encoded, err := json.Marshal(row.Result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, root) || strings.Contains(text, "uno") || strings.Contains(text, "dos") {
		t.Fatalf("collision leaked raw key material: %s", text)
	}
	if strings.Count(text, "[nested value omitted]") != 1 {
		t.Fatalf("expected one collision marker, got %s", text)
	}
	second, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, err := json.Marshal(second.Repositories[0].Result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(secondEncoded) {
		t.Fatal("collision handling is not deterministic")
	}
}

func TestRunSanitizerKeepsLiteralMarkerKeyDuringTruncation(t *testing.T) {
	targetOne := target(t, "api")
	root := targetOne.Root
	values := map[string]string{
		"[atenea sanitized]":        "legitimate",
		filepath.Join(root, "a.go"): "physical",
		"api/a.go":                  "alias",
	}
	for i := 0; i < 300; i++ {
		values[fmt.Sprintf("entry-%04d", i)] = fmt.Sprintf("value-%04d", i)
	}
	dispatch := func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{Result: map[string]any{"values": values}}, nil
	}
	result, err := workspacecontext.Run(context.Background(), runRequest(targetOne), nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	row := result.Repositories[0]
	if !row.Partial || !row.Truncated {
		t.Fatalf("truncation/collision must be visible: %+v", row)
	}
	valuesResult, ok := row.Result["values"].(map[string]any)
	if !ok {
		t.Fatalf("sanitized values type = %T", row.Result["values"])
	}
	if valuesResult["[atenea sanitized]"] != "legitimate" {
		t.Fatalf("literal marker key was overwritten: %#v", valuesResult)
	}
	if valuesResult["[atenea sanitized]-1"] != "[nested value omitted]" {
		t.Fatalf("truncation marker did not move to a free key: %#v", valuesResult)
	}
}

func TestRunReflectivelySanitizesTypedValuesCyclesAndLimits(t *testing.T) {
	type typedMap map[string]string
	type customString string
	type typedSlice []customString
	type typedCycle map[string]any

	root := t.TempDir()
	cycle := typedCycle{}
	cycle["self"] = cycle
	large := make(map[string]string, 5000)
	for i := 0; i < 5000; i++ {
		large[fmt.Sprintf("item-%04d", i)] = filepath.Join(root, "bulk", fmt.Sprintf("%04d.go", i))
	}
	targetOne := workspacecontext.Target{ID: "api", Root: root}
	request := runRequest(targetOne)
	dispatch := func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{Result: map[string]any{
			"typed_map": typedMap{
				"path":   filepath.Join(root, "internal", "api.go"),
				"secret": "token=typed-secret",
			},
			"typed_nested": map[string]typedMap{
				"files": {"readme": filepath.Join(root, "README.md")},
			},
			"typed_slice": typedSlice{customString(filepath.Join(root, "one.go")), "password=typed-password"},
			"cycle":       cycle,
			"unsupported": make(chan int),
			"large":       large,
		}}, nil
	}
	first, err := workspacecontext.Run(context.Background(), request, nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	row := first.Repositories[0]
	if !row.Partial || !row.Truncated {
		t.Fatalf("sanitizer loss must make row incomplete: %+v", row)
	}
	if !strings.Contains(strings.Join(row.Notices, " "), "result sanitized") {
		t.Fatalf("sanitizer loss reason missing: %+v", row.Notices)
	}
	second, err := workspacecontext.Run(context.Background(), request, nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("sanitization is not deterministic")
	}
	text := string(firstJSON)
	for _, forbidden := range []string{root, "typed-secret", "typed-password", "chan int"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe value %q leaked: %s", forbidden, text)
		}
	}
	for _, required := range []string{"api/internal/api.go", "api/README.md", "api/one.go", "[cyclic value omitted]", "[value omitted: unsupported type]", "[nested value omitted]"} {
		if !strings.Contains(text, required) {
			t.Fatalf("sanitized marker/reference %q missing", required)
		}
	}
}

func TestRunSanitizerBoundsHugeNestedMapsAndPreservesSibling(t *testing.T) {
	root := t.TempDir()
	huge := make(map[string]map[string]string, 512)
	for outer := 0; outer < 512; outer++ {
		inner := make(map[string]string, 512)
		for item := 0; item < 512; item++ {
			inner[fmt.Sprintf("item-%04d", item)] = filepath.Join(root, fmt.Sprintf("%04d.go", item))
		}
		huge[fmt.Sprintf("branch-%04d", outer)] = inner
	}
	targetOne := workspacecontext.Target{ID: "api", Root: root}
	request := runRequest(targetOne)
	dispatch := func(_ context.Context, _ workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		return workspacecontext.ChildResult{Result: map[string]any{
			"huge":  huge,
			"small": map[string]string{"path": filepath.Join(root, "small.go")},
		}}, nil
	}
	first, err := workspacecontext.Run(context.Background(), request, nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	row := first.Repositories[0]
	if !row.Partial || !row.Truncated {
		t.Fatalf("bounded result must remain incomplete: %+v", row)
	}
	if got := countSanitizedNodes(row.Result); got > 4096 {
		t.Fatalf("sanitized output has %d nodes, exceeds bound", got)
	}
	encoded, err := json.Marshal(row.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "api/small.go") {
		t.Fatalf("small sibling was lost: %s", encoded)
	}
	second, err := workspacecontext.Run(context.Background(), request, nil, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, err := json.Marshal(second.Repositories[0].Result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(secondEncoded) {
		t.Fatal("bounded map selection is not deterministic")
	}
}

func countSanitizedNodes(value any) int {
	switch item := value.(type) {
	case map[string]any:
		total := 1
		for _, child := range item {
			total += countSanitizedNodes(child)
		}
		return total
	case []any:
		total := 1
		for _, child := range item {
			total += countSanitizedNodes(child)
		}
		return total
	default:
		return 1
	}
}

func TestRunPreservesInputOrderAndSeparatesRepositories(t *testing.T) {
	fixture, fixtureActive, fixtureErr := acceptancefixture.Load("S04")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	ids := []string{"one", "two"}
	if fixtureActive {
		var body struct {
			Repositories []struct {
				Name string `json:"name"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal(fixture.Bytes, &body); err != nil {
			t.Fatal(err)
		}
		ids = ids[:0]
		for _, repo := range body.Repositories {
			ids = append(ids, repo.Name)
		}
		if len(ids) < 2 {
			t.Fatal("sealed workspace fixture needs multiple repositories")
		}
	}
	targets := make([]workspacecontext.Target, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, target(t, id))
	}
	var active, maxActive atomic.Int32
	var mu sync.Mutex
	seen := []string{}
	result, err := workspacecontext.Run(context.Background(), runRequest(targets...), nil, func(ctx context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		if child.Capability != workspacecontext.ChildCapability || child.Repository.ID == "" || len(child.Repository.Path) == 0 {
			t.Errorf("invalid child request: %+v", child)
		}
		current := active.Add(1)
		for {
			old := maxActive.Load()
			if current <= old || maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		defer active.Add(-1)
		mu.Lock()
		seen = append(seen, child.Repository.ID)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return workspacecontext.ChildResult{Provider: "p-" + child.Repository.ID, Result: map[string]any{"repo": child.Repository.ID}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != len(ids) {
		t.Fatalf("repository count = %d, want %d", len(result.Repositories), len(ids))
	}
	for i, id := range ids {
		if result.Repositories[i].ID != id || result.Repositories[i].Provider != "p-"+id {
			t.Fatalf("order/provider lost at %d: %+v", i, result.Repositories)
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("order lost: %+v", result.Repositories)
	}
	if maxActive.Load() < 1 {
		t.Fatalf("execution=%v max=%d", seen, maxActive.Load())
	}
}

func TestRunClassifiesPartialFailureTruncationAndContinuationPerRepository(t *testing.T) {
	one, two := target(t, "one"), target(t, "two")
	result, err := workspacecontext.Run(context.Background(), runRequest(one, two), nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		if child.Repository.ID == "one" {
			return workspacecontext.ChildResult{Evidence: []contract.QueryEvidence{{Completeness: "LOWER_BOUND", NextCursor: "one-next"}}, NextCursor: "one-next"}, nil
		}
		return workspacecontext.ChildResult{Result: map[string]any{"truncated": true}, Evidence: []contract.QueryEvidence{{Completeness: "complete"}}, Error: errors.New("provider failed")}, errors.New("provider failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || !result.Repositories[0].Partial || result.Repositories[0].NextCursor != "one-next" || result.Repositories[1].Error == "" || !result.Repositories[1].Partial || !result.Repositories[1].Truncated {
		t.Fatalf("partial classification = %+v", result)
	}
}

func TestRunUsesRepositoryBoundContinuation(t *testing.T) {
	one, two := target(t, "one"), target(t, "two")
	request := runRequest(one, two)
	request.Continuations = []workspacecontext.Continuation{{RepositoryID: "two", Cursor: "two-cursor"}}
	got := map[string]string{}
	var gotMu sync.Mutex
	result, err := workspacecontext.Run(context.Background(), request, nil, func(_ context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		gotMu.Lock()
		got[child.Repository.ID] = child.Cursor
		gotMu.Unlock()
		return workspacecontext.ChildResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Partial || got["one"] != "" || got["two"] != "two-cursor" {
		t.Fatalf("cursor crossed: result=%+v calls=%s", result, got)
	}
}

func TestRunCancellationLeavesUnstartedTargetsHonest(t *testing.T) {
	first, second := target(t, "one"), target(t, "two")
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	coordinator, err := workspacecontext.New(workspacecontext.Config{MaxParallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Run(ctx, runRequest(first, second), nil, func(ctx context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		calls.Add(1)
		cancel()
		<-ctx.Done()
		return workspacecontext.ChildResult{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || calls.Load() != 1 {
		t.Fatalf("cancellation result=%+v calls=%d", result, calls.Load())
	}
	for _, row := range result.Repositories {
		if row.Error == "" {
			t.Fatalf("row missing cancellation state: %+v", row)
		}
	}
}
