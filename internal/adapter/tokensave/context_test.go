package tokensave

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const measuredContextReport = `## Code Context
**Query:** improve appeals upload UI

### Entry Points
- **uploadAttachmentGroups** (function) - webs/apela.gg/app/lib/attachments.server.ts:138
  ` + "`async function uploadAttachmentGroups(files: File[]): Promise<string[]>`" + `
  Sube varios grupos de ficheros y devuelve sus URLs.
- **ForeignHandler** (function) - services/api-db-go/internal/handlers/appeals.go:44
  ` + "`func ForeignHandler(ctx context.Context) error`" + `

### Related Symbols
- webs/apela.gg/app/routes/appeal.tsx: Appeal:51, submitAppeal:90
- services/api-db-go/internal/handlers/appeals.go: Store:80

### Code
#### uploadAttachmentGroups (webs/apela.gg/app/lib/attachments.server.ts:138)
` + "```typescript" + `
export async function uploadAttachmentGroups(files: File[]) {
	return upload(files);
}
` + "```" + `

### Extension Points
_No public traits/interfaces found in context._

### Test Coverage
- webs/apela.gg/app/lib/attachments.server.test.ts
- services/api-db-go/internal/handlers/appeals_test.go

seen_node_ids: ["function:one", "function:two"]


tokensave_metrics: before=1500 after=420 saved=1080`

func TestParseContextReportReadsEveryMeasuredSection(t *testing.T) {
	report := parseContextReport(measuredContextReport)
	if len(report.symbols) != 2 {
		t.Fatalf("symbols = %+v, want 2", report.symbols)
	}
	first := report.symbols[0]
	if first.name != "uploadAttachmentGroups" || first.kind != "function" ||
		first.path != "webs/apela.gg/app/lib/attachments.server.ts" || first.line != 138 {
		t.Errorf("first symbol = %+v", first)
	}
	if first.signature != "async function uploadAttachmentGroups(files: File[]): Promise<string[]>" {
		t.Errorf("signature = %q", first.signature)
	}
	if first.summary != "Sube varios grupos de ficheros y devuelve sus URLs." {
		t.Errorf("summary = %q", first.summary)
	}
	if len(report.related) != 3 {
		t.Fatalf("related = %+v, want 3 rows (two share one file)", report.related)
	}
	if got := report.related[1]; got.name != "submitAppeal" || got.line != 90 {
		t.Errorf("second related = %+v", got)
	}
	if len(report.snippets) != 1 || !strings.Contains(report.snippets[0].code, "return upload(files)") {
		t.Errorf("snippets = %+v", report.snippets)
	}
	if len(report.tests) != 2 {
		t.Errorf("tests = %+v, want 2", report.tests)
	}
}

func TestRunContextTranslatesInputsAndDropsForeignRowsOutLoud(t *testing.T) {
	root, _ := workspace(t)
	// This repository is webs/apela.gg in the report, not services/api from
	// workspace(), so make the fixture match the real prefix arrangement.
	repo := contract.NewRepository("apela.gg", root+"/webs/apela.gg", []string{"typescript"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolContext, measuredContextReport, false)
	runner := newTestRunner(t, root, sess)

	req := request(t, repo, CapabilityContext, map[string]any{
		"task":            "improve appeals upload UI",
		"mode":            "plan",
		"limit":           7,
		"keywords":        []any{"attachment", "upload"},
		"scope":           []any{"app"},
		"include_snippet": true,
		"snippet_lines":   8,
	})
	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := fake.callsTo(toolContext)
	if len(calls) != 1 {
		t.Fatalf("%s called %d times, want 1", toolContext, len(calls))
	}
	args := calls[0]
	if args["max_nodes"] != float64(7) && args["max_nodes"] != 7 {
		t.Errorf("max_nodes = %#v, want 7", args["max_nodes"])
	}
	if args["mode"] != "plan" || args["include_code"] != true {
		t.Errorf("mode/include_code = %#v/%#v", args["mode"], args["include_code"])
	}
	include := args["path_include"].([]any)
	if len(include) != 1 || include[0] != "webs/apela.gg/app" {
		t.Errorf("path_include = %#v, want the repository-rooted scope", include)
	}

	symbols := rows(t, out, "symbols")
	if len(symbols) != 1 {
		t.Fatalf("symbols = %#v, want only the apela.gg entry", symbols)
	}
	if symbols[0]["path"] != "app/lib/attachments.server.ts" || symbols[0]["line"] != 138 {
		t.Errorf("symbol = %#v", symbols[0])
	}
	related := rows(t, out, "related")
	if len(related) != 2 || related[0]["path"] != "app/routes/appeal.tsx" {
		t.Errorf("related = %#v", related)
	}
	tests := out.Result["tests"].([]any)
	if len(tests) != 1 || tests[0] != "app/lib/attachments.server.test.ts" {
		t.Errorf("tests = %#v", tests)
	}
	var said bool
	for _, discovery := range out.Discoveries {
		if strings.Contains(discovery.Note, "outside apela.gg") {
			said = true
		}
	}
	if !said {
		t.Errorf("foreign rows were dropped silently: %+v", out.Discoveries)
	}
}

func TestRunContextAtTheWorkspaceRootKeepsCrossRepositoryRows(t *testing.T) {
	root, _ := workspace(t)
	repo := contract.NewRepository("kena-workspace", root, nil, contract.ScaleLarge, contract.VCSUnspecified, nil)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolContext, measuredContextReport, false)
	runner := newTestRunner(t, root, sess)

	out, err := runner.Run(context.Background(), request(t, repo, CapabilityContext,
		map[string]any{"task": "improve appeals upload UI"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(rows(t, out, "symbols")); got != 2 {
		t.Errorf("symbols = %d, want both repositories at the umbrella root", got)
	}
	if got := len(rows(t, out, "related")); got != 3 {
		t.Errorf("related = %d, want all workspace rows", got)
	}
}

func TestRunContextDropsSensitiveSnippetsWithoutDroppingTheSymbol(t *testing.T) {
	root, repo := workspace(t)
	report := `## Code Context
**Query:** credentials

### Entry Points
- **TOKEN** (const) - services/api/.env:1

### Related Symbols

### Code
#### TOKEN (services/api/.env:1)
` + "```dotenv" + `
TOKEN=do-not-leak
` + "```"
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolContext, report, false)
	runner := newTestRunner(t, root, sess)

	out, err := runner.Run(context.Background(), request(t, repo, CapabilityContext,
		map[string]any{"task": "credentials", "include_snippet": true}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(rows(t, out, "symbols")); got != 1 {
		t.Errorf("symbols = %d, want the identity even though its source is sensitive", got)
	}
	if _, present := out.Result["snippets"]; present {
		t.Fatalf("sensitive snippet leaked: %#v", out.Result["snippets"])
	}
	if text := strings.Join(discoveryNotes(out), "\n"); !strings.Contains(text, "sensitive") {
		t.Errorf("sensitive drop was silent: %s", text)
	}
}

func TestRunContextRefusesUnknownModeBeforeCallingTheTool(t *testing.T) {
	root, repo := workspace(t)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	runner := newTestRunner(t, root, sess)

	_, err := runner.Run(context.Background(), request(t, repo, CapabilityContext,
		map[string]any{"task": "anything", "mode": "guess"}))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err=%v)", got, err)
	}
	if calls := fake.callsTo(toolContext); len(calls) != 0 {
		t.Errorf("%s called after invalid mode", toolContext)
	}
}

func discoveryNotes(out contract.Outcome) []string {
	notes := make([]string, 0, len(out.Discoveries))
	for _, discovery := range out.Discoveries {
		notes = append(notes, discovery.Note)
	}
	return notes
}

func TestRunContextDefaultsToTheRepositoryPrefixButLeavesTheUmbrellaGlobal(t *testing.T) {
	root, individual := workspace(t)
	umbrella := contract.NewRepository("kena-workspace", root, nil, contract.ScaleLarge, contract.VCSUnspecified, nil)
	for _, tc := range []struct {
		name string
		repo contract.Repository
		want string
	}{
		{name: "individual repo", repo: individual, want: "services/api"},
		{name: "umbrella root", repo: umbrella, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, sess := newFakeTokensave(t)
			fake.on(toolStatus, readyStatus, false)
			fake.on(toolContext, measuredContextReport, false)
			runner := newTestRunner(t, root, sess)
			if _, err := runner.Run(context.Background(), request(t, tc.repo, CapabilityContext,
				map[string]any{"task": "map this code"})); err != nil {
				t.Fatalf("Run: %v", err)
			}
			args := fake.callsTo(toolContext)[0]
			raw, sent := args["path_include"]
			if tc.want == "" {
				if sent {
					t.Errorf("path_include = %#v at the umbrella root, want absent", raw)
				}
				return
			}
			list, ok := raw.([]any)
			if !ok || len(list) != 1 || list[0] != tc.want {
				t.Errorf("path_include = %#v, want [%q]", raw, tc.want)
			}
		})
	}
}
