package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func contextIntentAnswer(truncated bool, nextCursor string, symbols []map[string]any) string {
	body, err := json.Marshal(map[string]any{
		"truncated": truncated, "next_cursor": nextCursor,
		"coverage":     map[string]any{"exact": len(symbols), "candidate": 0, "unresolved_related": 0, "package_level": 0},
		"completeness": map[string]any{"verdict": "COMPLETE"},
		"results":      map[string]any{"symbols": symbols},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func contextSymbols(repository string, count int) []map[string]any {
	rows := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		rows = append(rows, map[string]any{
			"name": fmt.Sprintf("Symbol%d", index), "qualified_name": fmt.Sprintf("pkg.Symbol%d", index),
			"kind": "function", "repository": repository, "file_path": fmt.Sprintf("src/file%d.go", index),
			"start_line": index + 1, "end_line": index + 3, "terms": 1, "match": "lexical",
		})
	}
	return rows
}

func contextRequest(t *testing.T, repo contract.Repository, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability: contract.Capability{
			ID: CapabilityContext, Version: contract.Version{Major: 1}, Summary: "context test",
			Effects: []contract.Effect{contract.EffectRead},
			Inputs: []contract.Field{
				{Name: "task", Type: contract.TypeString, Required: true},
				{Name: "limit", Type: contract.TypeInt}, {Name: "include_snippet", Type: contract.TypeBool},
				{Name: "snippet_lines", Type: contract.TypeInt}, {Name: "cursor", Type: contract.TypeString},
			},
			Outputs: []contract.Field{
				{Name: "symbols", Type: contract.TypeRecordList, Required: true, Fields: []contract.Field{
					{Name: "name", Type: contract.TypeString, Required: true}, {Name: "kind", Type: contract.TypeString, Required: true},
					{Name: "path", Type: contract.TypeString, Required: true}, {Name: "line", Type: contract.TypeInt, Required: true},
				}},
				{Name: "snippets", Type: contract.TypeRecordList, Fields: []contract.Field{
					{Name: "name", Type: contract.TypeString, Required: true}, {Name: "path", Type: contract.TypeString, Required: true},
					{Name: "line", Type: contract.TypeInt, Required: true}, {Name: "code", Type: contract.TypeString, Required: true},
				}},
				{Name: "truncated", Type: contract.TypeBool}, {Name: "next_cursor", Type: contract.TypeString},
				{Name: "source_trimmed", Type: contract.TypeInt},
			},
		},
		Implementation: contract.Implementation{ID: ImplContext, Provider: "kivgraph", Capability: CapabilityContext},
		Repository:     repo, Payload: payload,
		Permission: contract.Permission{Task: "context test", Effects: []contract.Effect{contract.EffectRead}},
	}
}

func TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S01", "S02")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	rows := contextSymbols("current", 20)
	if active {
		if fixture.Check != "go_context" && fixture.Check != "typescript_context" {
			t.Fatalf("unexpected check %q", fixture.Check)
		}
		if !strings.Contains(string(fixture.Bytes), "MemoryRepository") || !strings.Contains(string(fixture.Bytes), "Repository") {
			t.Fatal("sealed context fixture lacks its contract identity")
		}
		ext := ".go"
		if fixture.Language == "typescript" {
			ext = ".ts"
		}
		rows[0]["name"], rows[0]["qualified_name"], rows[0]["file_path"] = "MemoryRepository", "fixture.MemoryRepository", "src/repository"+ext
	}
	fake.on(toolIntent, contextIntentAnswer(false, "", rows), false)

	out, err := newTestRunner(t, sess).Run(context.Background(), contextRequest(t, repo, map[string]any{
		"task": "find all handlers", "limit": 20,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Fixture measurement: the former path paid graph_status + find_by_intent
	// + one get_symbol per row (22 calls); the grouped path pays two calls.
	before := 2 + len(rows)
	after := len(fake.callsTo(toolStatus)) + len(fake.callsTo(toolIntent)) + len(fake.callsTo(toolGet))
	t.Logf("code.context fixture calls: before=%d after=%d", before, after)
	if before != 22 || after != 2 {
		t.Fatalf("grouped call count = %d, want 2 (status + intent); calls=%#v", after, fake.calls)
	}
	if got := len(fake.callsTo(toolGet)); got != 0 {
		t.Fatalf("get_symbol called %d time(s), want none", got)
	}
	if got := len(out.Result["symbols"].([]any)); got != 20 {
		t.Fatalf("symbols = %d, want 20", got)
	}
	if active {
		first := out.Result["symbols"].([]any)[0].(map[string]any)
		if first["name"] != "fixture.MemoryRepository" {
			t.Fatalf("sealed fixture identity did not cross code.context: %#v", first)
		}
	}
	last := out.Result["symbols"].([]any)[19].(map[string]any)
	if last["name"] != "pkg.Symbol19" || last["path"] != "src/file19.go" || last["line"] != 20 {
		t.Fatalf("last identity = %#v, want the find_by_intent identity", last)
	}
}

func TestContextGroupsSourceAndDeclaresPartialBodies(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	rows := contextSymbols("current", 20)
	fake.on(toolIntent, contextIntentAnswer(false, "", rows), false)
	source := "snapshot 1 2 bodies context 0 trimmed 3 at the 262144 byte ceiling\n" +
		"@ current src/file0.go:1-3 function pkg.Symbol0\nfirst\nsecond\nthird\n" +
		"! current src/file1.go pkg.Symbol1 — provider unavailable\n"
	fake.on("get_source", source, false)

	out, err := newTestRunner(t, sess).Run(context.Background(), contextRequest(t, repo, map[string]any{
		"task": "find all handlers", "limit": 20, "include_snippet": true, "snippet_lines": 2,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(fake.callsTo(toolStatus)) + len(fake.callsTo(toolIntent)) + len(fake.callsTo("get_source")); got != 3 {
		t.Fatalf("grouped call count = %d, want 3 (status + intent + one source); calls=%#v", got, fake.calls)
	}
	getSourceCalls := fake.callsTo("get_source")
	if len(getSourceCalls) != 1 {
		t.Fatalf("get_source calls = %d, want one", len(getSourceCalls))
	}
	selectors, ok := getSourceCalls[0]["symbols"].([]any)
	if !ok || len(selectors) != 20 {
		t.Fatalf("source payload = %#v, want 20 selectors", getSourceCalls[0]["symbols"])
	}
	for index, selector := range selectors {
		row := selector.(map[string]any)
		if row["repository"] != "current" || row["path"] != fmt.Sprintf("src/file%d.go", index) || row["qualified_name"] != fmt.Sprintf("pkg.Symbol%d", index) {
			t.Fatalf("selector[%d] = %#v, identity was not preserved", index, row)
		}
	}
	snippets := out.Result["snippets"].([]any)
	if len(snippets) != 20 {
		t.Fatalf("snippets = %d, want one attributable record per result", len(snippets))
	}
	first := snippets[0].(map[string]any)["code"].(string)
	if !strings.Contains(first, "first\nsecond") || !strings.Contains(first, "truncated by Atenea") {
		t.Fatalf("truncated first snippet = %q", first)
	}
	providerUnavailable := snippets[1].(map[string]any)["code"].(string)
	if !strings.Contains(providerUnavailable, "Source unavailable") || !strings.Contains(providerUnavailable, "provider unavailable") || strings.Contains(providerUnavailable, "does not exist") {
		t.Fatalf("provider unavailable snippet = %q", providerUnavailable)
	}
	missing := snippets[2].(map[string]any)["code"].(string)
	if !strings.Contains(missing, "Source unavailable") || !strings.Contains(missing, "existence is not inferred") {
		t.Fatalf("unattributed snippet = %q", missing)
	}
	if out.Result["source_trimmed"] != 3 || !hasNote(out, "262144-byte ceiling") || !strings.Contains(missing, "source availability is unknown") {
		t.Fatalf("trimmed source evidence = result=%#v discoveries=%#v snippet=%q", out.Result, out.Discoveries, missing)
	}
}

func TestContextCarriesPartialIntentWarningWithoutClaimingAbsence(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolIntent, contextIntentAnswer(true, "intent-next", contextSymbols("current", 1)), false)
	out, err := newTestRunner(t, sess).Run(context.Background(), contextRequest(t, repo, map[string]any{"task": "find handler"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasNote(out, "partial") || !hasNote(out, "absence conclusions") {
		t.Fatalf("partial warning missing: %#v", out.Discoveries)
	}
	if out.Result["truncated"] != true || out.Result["next_cursor"] != "intent-next" {
		t.Fatalf("partial page metadata = %#v, want truncated=true and next_cursor", out.Result)
	}
	if len(fake.callsTo("get_source")) != 0 {
		t.Fatalf("get_source called without include_snippet: %#v", fake.callsTo("get_source"))
	}
}

func TestContextPassesCursorToTheSecondIntentPage(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolIntent, contextIntentAnswer(false, "", contextSymbols("current", 1)), false)
	out, err := newTestRunner(t, sess).Run(context.Background(), contextRequest(t, repo, map[string]any{
		"task": "find handler", "cursor": "intent-next",
	}))
	if err != nil {
		t.Fatalf("Run second page: %v", err)
	}
	calls := fake.callsTo(toolIntent)
	if len(calls) != 1 || calls[0]["cursor"] != "intent-next" {
		t.Fatalf("find_by_intent cursor = %#v, want intent-next", calls)
	}
	if out.Result["truncated"] != false {
		t.Fatalf("second page metadata = %#v, want truncated=false", out.Result)
	}
	if _, present := out.Result["next_cursor"]; present {
		t.Fatalf("second page unexpectedly returned next_cursor: %#v", out.Result)
	}
}

func TestContextDoesNotAskSourceForAnEmptyIntentPage(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolIntent, contextIntentAnswer(false, "", nil), false)
	out, err := newTestRunner(t, sess).Run(context.Background(), contextRequest(t, repo, map[string]any{
		"task": "find absent handler", "include_snippet": true,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.callsTo("get_source")) != 0 {
		t.Fatalf("get_source called for an empty selector list: %#v", fake.callsTo("get_source"))
	}
	if snippets, ok := out.Result["snippets"].([]any); !ok || len(snippets) != 0 {
		t.Fatalf("snippets = %#v, want an empty list", out.Result["snippets"])
	}
}

func TestContextSourceConsumesDeclaredRangeBeforeAdversarialHeaderLines(t *testing.T) {
	row := intentSymbol{Repository: "current", FilePath: "src/file.go", QualifiedName: "pkg.Real", StartLine: 10, EndLine: 12, Kind: "function"}
	item := contextRow{row: row, selector: map[string]any{"repository": "current", "path": row.FilePath, "qualified_name": row.QualifiedName}}
	otherRow := intentSymbol{Repository: "current", FilePath: "src/other.go", QualifiedName: "pkg.Other", StartLine: 1, EndLine: 1, Kind: "function"}
	otherItem := contextRow{row: otherRow, selector: map[string]any{"repository": "current", "path": otherRow.FilePath, "qualified_name": otherRow.QualifiedName}}
	source := "snapshot 1 2 bodies context 0\n" +
		"@ current src/file.go:10-12 function pkg.Real\n" +
		"first\n@ current src/fake.go:1-1 function pkg.Fake\n! current src/fake.go pkg.Fake — adversarial body text\n" +
		"! current src/other.go pkg.Other — omitted\n"
	parsed := parseContextSourceForRows(source, []contextRow{item, otherItem})
	block, found := parsed.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
	if !found || !block.available {
		t.Fatalf("real block = %#v, found=%t", block, found)
	}
	if !strings.Contains(block.body, "@ current src/fake.go:1-1 function pkg.Fake") || !strings.Contains(block.body, "! current src/fake.go pkg.Fake") {
		t.Fatalf("adversarial lines were split from body: %q", block.body)
	}
	other, found := parsed.blocks[contextSelectorKey("current", "src/other.go", "pkg.Other")]
	if !found || other.available || !strings.Contains(other.reason, "omitted") {
		t.Fatalf("following unavailable block = %#v, found=%t", other, found)
	}
}

func TestContextSourceRejectsAnEnormousRangeWithoutPanicking(t *testing.T) {
	row := intentSymbol{Repository: "current", FilePath: "src/file.go", QualifiedName: "pkg.Real", StartLine: 1, EndLine: int(^uint(0) >> 1), Kind: "function"}
	item := contextRow{row: row, selector: map[string]any{"repository": "current", "path": row.FilePath, "qualified_name": row.QualifiedName}}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("parseContextSourceForRows panicked on MaxInt end_line: %v", recovered)
		}
	}()
	parsed := parseContextSourceForRows("@ current src/file.go:1-9223372036854775807 function pkg.Real\nonly line\n", []contextRow{item})
	block, found := parsed.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
	if !found || block.available || !strings.Contains(block.reason, "declared source line") {
		t.Fatalf("enormous range = %#v, found=%t; want bounded unknown availability", block, found)
	}
}

func TestContextSourceKeepsFollowingHeaderWhenBodyIsShort(t *testing.T) {
	first := intentSymbol{Repository: "current", FilePath: "src/first.go", QualifiedName: "pkg.First", StartLine: 1, EndLine: 4, Kind: "function"}
	second := intentSymbol{Repository: "current", FilePath: "src/second.go", QualifiedName: "pkg.Second", StartLine: 1, EndLine: 1, Kind: "function"}
	rows := []contextRow{
		{row: first, selector: map[string]any{"repository": "current", "path": first.FilePath, "qualified_name": first.QualifiedName}},
		{row: second, selector: map[string]any{"repository": "current", "path": second.FilePath, "qualified_name": second.QualifiedName}},
	}
	source := "@ current src/first.go:1-4 function pkg.First\nfirst line\n" +
		"! current src/second.go pkg.Second — provider omitted\n"
	parsed := parseContextSourceForRows(source, rows)
	firstBlock, firstFound := parsed.blocks[contextSelectorKey("current", first.FilePath, first.QualifiedName)]
	if !firstFound || firstBlock.available || !strings.Contains(firstBlock.reason, "ambiguous source framing") {
		t.Fatalf("short first body = %#v, found=%t; want ambiguous unknown", firstBlock, firstFound)
	}
	secondBlock, secondFound := parsed.blocks[contextSelectorKey("current", second.FilePath, second.QualifiedName)]
	if !secondFound || secondBlock.available || !strings.Contains(secondBlock.reason, "ambiguous source framing") {
		t.Fatalf("following header = %#v, found=%t; short body consumed it", secondBlock, secondFound)
	}
}

func TestContextSourceDoesNotAttributeExpectedHeaderInsideAnotherBody(t *testing.T) {
	first := intentSymbol{Repository: "current", FilePath: "src/first.go", QualifiedName: "pkg.First", StartLine: 1, EndLine: 3, Kind: "function"}
	second := intentSymbol{Repository: "current", FilePath: "src/second.go", QualifiedName: "pkg.Second", StartLine: 1, EndLine: 1, Kind: "function"}
	rows := []contextRow{
		{row: first, selector: map[string]any{"repository": "current", "path": first.FilePath, "qualified_name": first.QualifiedName}},
		{row: second, selector: map[string]any{"repository": "current", "path": second.FilePath, "qualified_name": second.QualifiedName}},
	}
	source := "@ current src/first.go:1-3 function pkg.First\n" +
		"one\n@ current src/second.go:1-1 function pkg.Second\nsecond\n"
	parsed := parseContextSourceForRows(source, rows)
	firstBlock, firstFound := parsed.blocks[contextSelectorKey("current", first.FilePath, first.QualifiedName)]
	secondBlock, secondFound := parsed.blocks[contextSelectorKey("current", second.FilePath, second.QualifiedName)]
	if !firstFound || firstBlock.available || !strings.Contains(firstBlock.reason, "ambiguous source framing") {
		t.Fatalf("first block = %#v, found=%t; want unknown framing", firstBlock, firstFound)
	}
	if !secondFound || secondBlock.available {
		t.Fatalf("second block = %#v, found=%t; expected header inside first body must never be attributed", secondBlock, secondFound)
	}
}

func TestContextSourceShortBodyWithoutFollowingHeaderIsUnknown(t *testing.T) {
	row := intentSymbol{Repository: "current", FilePath: "src/file.go", QualifiedName: "pkg.Real", StartLine: 1, EndLine: 3, Kind: "function"}
	item := contextRow{row: row, selector: map[string]any{"repository": "current", "path": row.FilePath, "qualified_name": row.QualifiedName}}
	parsed := parseContextSourceForRows("@ current src/file.go:1-3 function pkg.Real\none\ntwo\n", []contextRow{item})
	block, found := parsed.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
	if !found || block.available || !strings.Contains(block.reason, "only 2 of 3") {
		t.Fatalf("short body = %#v, found=%t; want explicit unknown availability", block, found)
	}
}

func TestContextSourcePreservesLegitimateFinalEmptyLine(t *testing.T) {
	row := intentSymbol{Repository: "current", FilePath: "src/file.go", QualifiedName: "pkg.Real", StartLine: 1, EndLine: 3, Kind: "function"}
	item := contextRow{row: row, selector: map[string]any{"repository": "current", "path": row.FilePath, "qualified_name": row.QualifiedName}}
	// The final two newlines are: one real empty source line and one renderer
	// separator. TrimSuffix must remove only the latter.
	parsed := parseContextSourceForRows("@ current src/file.go:1-3 function pkg.Real\none\ntwo\n\n", []contextRow{item})
	block, found := parsed.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
	if !found || !block.available || block.body != "one\ntwo\n" {
		t.Fatalf("final empty source line = %#v, found=%t; want the real trailing newline preserved", block, found)
	}
}

func TestContextSourceRangeMismatchDoesNotInventAttribution(t *testing.T) {
	first := intentSymbol{Repository: "current", FilePath: "src/first.go", QualifiedName: "pkg.First", StartLine: 1, EndLine: 3, Kind: "function"}
	second := intentSymbol{Repository: "current", FilePath: "src/second.go", QualifiedName: "pkg.Second", StartLine: 1, EndLine: 1, Kind: "function"}
	rows := []contextRow{
		{row: first, selector: map[string]any{"repository": "current", "path": first.FilePath, "qualified_name": first.QualifiedName}},
		{row: second, selector: map[string]any{"repository": "current", "path": second.FilePath, "qualified_name": second.QualifiedName}},
	}
	// A's shortened provider range would otherwise make the following line
	// look like B's source and attribute FAKE_FROM_A to the wrong declaration.
	source := "@ current src/first.go:1-1 function pkg.First\none\nFAKE_FROM_A\n" +
		"@ current src/second.go:1-1 function pkg.Second\nsecond\n"
	parsed := parseContextSourceForRows(source, rows)
	firstBlock := parsed.blocks[contextSelectorKey("current", first.FilePath, first.QualifiedName)]
	secondBlock := parsed.blocks[contextSelectorKey("current", second.FilePath, second.QualifiedName)]
	if firstBlock.available || !strings.Contains(firstBlock.reason, "source range mismatch") {
		t.Fatalf("first mismatched block = %#v; want unavailable mismatch", firstBlock)
	}
	if secondBlock.available || strings.Contains(secondBlock.body, "FAKE_FROM_A") {
		t.Fatalf("second block = %#v; mismatched range must not attribute A's text to B", secondBlock)
	}
}

func TestContextSourceAcceptsCoherentReanchoring(t *testing.T) {
	row := intentSymbol{Repository: "current", FilePath: "src/file.go", QualifiedName: "pkg.Real", StartLine: 10, EndLine: 12, Kind: "function"}
	item := contextRow{row: row, selector: map[string]any{"repository": "current", "path": row.FilePath, "qualified_name": row.QualifiedName}}
	parsed := parseContextSourceForRows("@ current src/file.go:13-15 function pkg.Real [file changed, re-anchored +3]\none\ntwo\nthree\n", []contextRow{item})
	block, found := parsed.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
	if !found || !block.available || block.body != "one\ntwo\nthree" {
		t.Fatalf("coherent reanchor = %#v, found=%t; want attributed body", block, found)
	}
	zero := parseContextSourceForRows("@ current src/file.go:10-12 function pkg.Real [file changed, re-anchored +0]\none\ntwo\nthree\n", []contextRow{item})
	zeroBlock, zeroFound := zero.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
	if !zeroFound || !zeroBlock.available || zeroBlock.body != "one\ntwo\nthree" {
		t.Fatalf("zero reanchor = %#v, found=%t; explicit +0 with unchanged range is valid", zeroBlock, zeroFound)
	}
}

func TestContextSourceRejectsIncoherentReanchoring(t *testing.T) {
	row := intentSymbol{Repository: "current", FilePath: "src/file.go", QualifiedName: "pkg.Real", StartLine: 10, EndLine: 12, Kind: "function"}
	item := contextRow{row: row, selector: map[string]any{"repository": "current", "path": row.FilePath, "qualified_name": row.QualifiedName}}
	for _, header := range []string{
		"@ current src/file.go:13-15 function pkg.Real [file changed, re-anchored +2]",
		"@ current src/file.go:13-15 function pkg.Real [file changed, re-anchored nope]",
		"@ current src/file.go:10-12 function pkg.Real [file changed, re-anchored 0]",
	} {
		parsed := parseContextSourceForRows(header+"\none\ntwo\nthree\n", []contextRow{item})
		block, found := parsed.blocks[contextSelectorKey("current", row.FilePath, row.QualifiedName)]
		if !found || block.available || !strings.Contains(block.reason, "re-anchoring") {
			t.Fatalf("incoherent reanchor header %q = %#v, found=%t; want unavailable", header, block, found)
		}
	}
}
