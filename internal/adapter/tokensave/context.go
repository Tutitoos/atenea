package tokensave

// code.context: the cold-start capability, and the only one here whose far
// side does not answer JSON.
//
// Every other tool this adapter calls returns a JSON document that
// json.Unmarshal decodes. tokensave_context returns a MARKDOWN REPORT --
// measured against v7.9.0, not assumed -- with a fixed set of "### " sections
// and one row shape per section. So this file parses text, which is a thing
// the rest of this package deliberately never does, and the parser is written
// against the real output rather than a guess:
//
//	## Code Context
//	**Query:** <the task>
//
//	### Entry Points
//	- **readAppealId** (function) - modules/appeals-module/src/shared/review-action.ts:22
//	  `function readAppealId(args: ArgsHandler): string | undefined`
//	  Lee la id de la apelación de los argumentos del `custom_id`.
//
//	### Related Symbols
//	- path/to/file.ts: run:31
//	- path/to/other.go: TestOne:136, newFixture:102        <- several per file
//
//	### Code
//	_No code blocks extracted._                            <- or:
//	#### readAppealId (path/to/file.ts:22)
//	```typescript
//	…
//	```
//
//	### Extension Points                                   <- mode = plan only
//	_No public traits/interfaces found in context._
//
//	### Test Coverage                                      <- mode = plan only
//	- path/to/thing.test.ts
//
//	seen_node_ids: ["function:7b06…"]
//
// Three consequences of that shape, each a decision rather than an accident:
//
//   - A section that is present but empty says so in prose, wrapped in
//     underscores ("_No code blocks extracted._"). Those lines are recognised
//     and skipped, never parsed as a row and never reported as a result.
//   - "No results" does not exist on this wire. tokensave ranks with BM25 and
//     always returns its best matches, so a nonsense task comes back with
//     irrelevant rows rather than none -- measured. Nothing here treats a
//     small answer as a failure, and the empty-GRAPH guard (checkGraphReady)
//     stays the only thing that refuses.
//   - Extension Points is declared by the tool and was only ever observed
//     EMPTY on this corpus. Its populated row shape is therefore unknown, so
//     no output field is declared for it and no parser invents one. The day it
//     is seen filled, it gets a decoder measured against that output.
//
// # Why the answer stops at the repository
//
// tokensave_context is workspace-wide by nature: a task like "how appeals are
// stored" legitimately answers with rows in modules/, webs/ and services/ at
// once. contract.RunRequest carries exactly ONE Repository, so this adapter
// cannot name any other one -- there is no catalog on this side of the wire to
// look an arbitrary path up in. Emitting a foreign path anyway would put a
// path relative to some other root in the same field as repository-relative
// ones, and a caller acting on it would open the wrong file.
//
// So foreign rows are dropped and the count travels back as a discovery, the
// same rule the package doc states for symbol.calls. The whole-workspace view
// is reachable without breaking that rule: declare the root itself as a
// [[repository]] and ask about it, because prefixFor already treats an empty
// prefix as "the repository IS the root" and then every row is inside it.

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// defaultContextSymbols mirrors tokensave's own documented default for
// max_nodes. It is restated here rather than left unset so the declared
// answer size is a decision this file can be read for, not one that changes
// under it when the far side changes its mind.
const defaultContextSymbols = 20

// The context modes the capability declares. They are tokensave's own two and
// they mean different questions, not different verbosity: plan additionally
// reports the tests that cover what it found.
const (
	modeExplore = "explore"
	modePlan    = "plan"
)

// Section headings on tokensave's report, verbatim.
const (
	headingEntryPoints = "### Entry Points"
	headingRelated     = "### Related Symbols"
	headingCode        = "### Code"
	headingExtension   = "### Extension Points"
	headingTests       = "### Test Coverage"
)

var (
	// - **name** (kind) - path/to/file.ts:22
	entryRowRe = regexp.MustCompile(`^- \*\*(.+?)\*\* \(([^)]+)\) - (.+):(\d+)$`)
	// - path/to/file.ts: name:31, other:44
	relatedRowRe = regexp.MustCompile(`^- (.+?): (.+)$`)
	// #### name (path/to/file.ts:22)
	codeHeaderRe = regexp.MustCompile(`^#### (.+) \((.+):(\d+)\)$`)
	// - path/to/file.test.ts
	testRowRe = regexp.MustCompile(`^- (\S+)$`)
)

// contextSymbol is one row of Entry Points: a declaration the task points at,
// with the signature and docstring tokensave attaches to it when it has them.
type contextSymbol struct {
	name      string
	kind      string
	path      string
	line      int
	signature string
	summary   string
}

// contextRelated is one row of Related Symbols. Several symbols share a file
// on this wire, so one source line can produce several of these.
type contextRelated struct {
	path string
	name string
	line int
}

// contextSnippet is one block of the Code section.
type contextSnippet struct {
	name string
	path string
	line int
	code string
}

// contextReport is tokensave_context's whole report, parsed.
type contextReport struct {
	symbols  []contextSymbol
	related  []contextRelated
	snippets []contextSnippet
	tests    []string
}

// runContext answers code.context.
//
// The declared inputs are atenea's own vocabulary and are translated on the
// way out: limit -> max_nodes, include_snippet -> include_code, snippet_lines
// -> max_code_lines, scope -> path_include. keywords and mode pass through
// under their own names because tokensave already calls them that and they
// mean the same thing.
//
// exclude_node_ids and the report's own seen_node_ids are deliberately not
// declared on either side. They are tokensave node ids: a capability whose
// input only one provider could ever produce is that provider's passthrough
// wearing a funnel costume -- the same reason symbol.consumers refuses to take
// a kivgraph stable_key.
func (r *Runner) runContext(ctx context.Context, sess *mcpstdio.Session, prefix string,
	req contract.RunRequest) (map[string]any, []string, error) {

	task, _ := req.Payload["task"].(string)
	if strings.TrimSpace(task) == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "task is required and must not be empty")
	}
	mode, err := contextMode(req.Payload)
	if err != nil {
		return nil, nil, err
	}
	limit, err := intAt(req.Payload, "limit", defaultContextSymbols)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"limit must be above 0, got %d", limit)
	}
	wantSnippets := boolAt(req.Payload, "include_snippet")

	args := map[string]any{
		"task":      task,
		"max_nodes": limit,
		"mode":      mode,
	}
	if wantSnippets {
		args["include_code"] = true
		if lines, err := intAt(req.Payload, "snippet_lines", 0); err != nil {
			return nil, nil, err
		} else if lines > 0 {
			args["max_code_lines"] = lines
		}
	}
	if words := stringsAt(req.Payload, "keywords"); len(words) > 0 {
		args["keywords"] = words
	}

	// scope narrows by path substring on tokensave's side. Entries are
	// repository-relative, so each is prefixed into a root-relative one --
	// otherwise "src" would match src/ in every repository under this root.
	//
	// With no explicit scope an INDIVIDUAL repository still supplies its own
	// prefix. Asking the whole workspace and filtering afterwards is not
	// equivalent: foreign rows consume max_nodes before local ones are even
	// ranked (measured: 147 discarded rows and only two weak local symbols
	// for an apela.gg UI task). The umbrella root has an empty prefix and
	// therefore correctly leaves path_include absent for a global search.
	var include []string
	if scope := stringsAt(req.Payload, "scope"); len(scope) > 0 {
		include = make([]string, 0, len(scope))
		for _, entry := range scope {
			clean := strings.TrimSpace(entry)
			if clean == "" {
				continue
			}
			rooted, err := toRoot(prefix, clean, req.Repository.ID)
			if err != nil {
				return nil, nil, err
			}
			include = append(include, rooted)
		}
	} else if prefix != "" {
		include = []string{prefix}
	}
	if len(include) > 0 {
		args["path_include"] = include
	}

	text, err := sess.Call(ctx, toolContext, args)
	if err != nil {
		return nil, nil, err
	}
	report := parseContextReport(string(payloadOf(text)))

	var notes []string
	foreign := 0
	secret := 0

	symbols := make([]any, 0, len(report.symbols))
	for _, item := range report.symbols {
		relative, inside := toRepository(prefix, item.path)
		if !inside {
			foreign++
			continue
		}
		record := map[string]any{
			"name": item.name,
			"kind": item.kind,
			"path": relative,
			"line": item.line,
		}
		if item.signature != "" {
			record["signature"] = item.signature
		}
		if item.summary != "" {
			record["summary"] = item.summary
		}
		symbols = append(symbols, record)
	}

	related := make([]any, 0, len(report.related))
	for _, item := range report.related {
		relative, inside := toRepository(prefix, item.path)
		if !inside {
			foreign++
			continue
		}
		related = append(related, map[string]any{
			"path": relative,
			"name": item.name,
			"line": item.line,
		})
	}

	snippets := make([]any, 0, len(report.snippets))
	for _, item := range report.snippets {
		relative, inside := toRepository(prefix, item.path)
		if !inside {
			foreign++
			continue
		}
		// The Code section is the one place this wire carries file CONTENTS,
		// so it is the one place a sensitive file could leak through a tool
		// that never opened it here. Dropped and counted, never truncated
		// into something that looks harmless.
		if r.isSensitive(relative) {
			secret++
			continue
		}
		snippets = append(snippets, map[string]any{
			"name": item.name,
			"path": relative,
			"line": item.line,
			"code": item.code,
		})
	}

	tests := make([]any, 0, len(report.tests))
	for _, item := range report.tests {
		relative, inside := toRepository(prefix, item)
		if !inside {
			foreign++
			continue
		}
		tests = append(tests, relative)
	}

	result := map[string]any{"symbols": symbols}
	if len(related) > 0 {
		result["related"] = related
	}
	if len(snippets) > 0 {
		result["snippets"] = snippets
	}
	if len(tests) > 0 {
		result["tests"] = tests
	}

	notes = append(notes, fmt.Sprintf("tokensave: %q points at %d symbol(s) in %s (mode %s)",
		task, len(symbols), req.Repository.ID, mode))
	if foreign > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d row(s) live outside %s and are not reported: this output's paths are relative to it, "+
				"and this wire carries one repository at a time", foreign, req.Repository.ID))
	}
	if secret > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d snippet(s) came from a file matching a sensitive pattern and were dropped", secret))
	}
	if wantSnippets && len(snippets) == 0 && secret == 0 {
		notes = append(notes, "snippets were asked for and tokensave extracted none for this task")
	}
	return result, notes, nil
}

// contextMode reads the declared mode, defaulting to explore. An unknown value
// is refused rather than silently explored: plan asks a different question and
// a caller who typed it wrong would get an answer to neither.
func contextMode(payload map[string]any) (string, error) {
	raw, ok := payload["mode"]
	if !ok || raw == nil {
		return modeExplore, nil
	}
	value, isText := raw.(string)
	if !isText {
		return "", contract.Fail(contract.FailureInvalidInput, "mode must be a string, got %T", raw)
	}
	switch value {
	case "":
		return modeExplore, nil
	case modeExplore, modePlan:
		return value, nil
	}
	return "", contract.Fail(contract.FailureInvalidInput,
		"mode %q: want %q or %q", value, modeExplore, modePlan)
}

// parseContextReport reads tokensave's markdown report.
//
// It never fails: a section it does not recognise, or a row that does not
// match its own shape, is skipped rather than turned into an error. The
// capability's promise is "the symbols this task lives in", and a report whose
// Test Coverage section changed format must still answer with the entry points
// it did parse. What cannot happen is a fabricated row: every field comes from
// a line that matched, or is absent.
func parseContextReport(report string) contextReport {
	var out contextReport
	section := ""
	var pending *contextSymbol
	var code *contextSnippet
	var fenced []string
	inFence := false

	flushSymbol := func() {
		if pending != nil {
			out.symbols = append(out.symbols, *pending)
			pending = nil
		}
	}
	flushCode := func() {
		if code != nil {
			code.code = strings.Join(fenced, "\n")
			out.snippets = append(out.snippets, *code)
			code = nil
		}
		fenced = nil
		inFence = false
	}

	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimRight(raw, "\r")

		// A fence belongs to the Code section and swallows everything until
		// it closes -- including lines that would otherwise look like rows.
		if inFence {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				flushCode()
				continue
			}
			fenced = append(fenced, line)
			continue
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") && !strings.HasPrefix(trimmed, "#### ") {
			flushSymbol()
			flushCode()
			section = trimmed
			continue
		}
		// An empty section states so in prose between underscores. Skipped
		// here so it can never be read as content.
		if strings.HasPrefix(trimmed, "_") && strings.HasSuffix(trimmed, "_") {
			continue
		}
		if trimmed == "" {
			continue
		}

		switch section {
		case headingEntryPoints:
			if m := entryRowRe.FindStringSubmatch(trimmed); m != nil {
				flushSymbol()
				line, err := strconv.Atoi(m[4])
				if err != nil {
					continue
				}
				pending = &contextSymbol{name: m[1], kind: m[2], path: m[3], line: line}
				continue
			}
			// Continuation lines belong to the row above: the first one
			// wrapped in backticks is the signature, anything else is the
			// docstring. Without a row above there is nothing to attach them
			// to, so they are dropped rather than guessed at.
			if pending == nil || !strings.HasPrefix(line, "  ") {
				continue
			}
			if strings.HasPrefix(trimmed, "`") && pending.signature == "" {
				pending.signature = strings.Trim(trimmed, "`")
				continue
			}
			if pending.summary == "" {
				pending.summary = trimmed
			}

		case headingRelated:
			m := relatedRowRe.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			file := strings.TrimSpace(m[1])
			for _, item := range strings.Split(m[2], ",") {
				item = strings.TrimSpace(item)
				at := strings.LastIndex(item, ":")
				if at <= 0 {
					continue
				}
				number, err := strconv.Atoi(item[at+1:])
				if err != nil {
					continue
				}
				out.related = append(out.related, contextRelated{
					path: file, name: item[:at], line: number,
				})
			}

		case headingCode:
			if m := codeHeaderRe.FindStringSubmatch(trimmed); m != nil {
				flushCode()
				number, err := strconv.Atoi(m[3])
				if err != nil {
					continue
				}
				code = &contextSnippet{name: m[1], path: m[2], line: number}
				continue
			}
			if strings.HasPrefix(trimmed, "```") && code != nil {
				inFence = true
				fenced = nil
			}

		case headingTests:
			if m := testRowRe.FindStringSubmatch(trimmed); m != nil {
				out.tests = append(out.tests, path.Clean(filepath.ToSlash(m[1])))
			}

		case headingExtension:
			// Only ever observed empty on this corpus, so there is nothing
			// measured to decode and no output field declared for it. See the
			// file comment.
			continue
		}
	}
	flushSymbol()
	flushCode()
	return out
}

// boolAt reads an optional boolean. Absent is false: every boolean on this
// capability is an intent flag, and no intent was expressed.
func boolAt(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

// stringsAt reads a string_list after JSON decoding. Contract validation has
// already rejected non-string members; accepting []string as well keeps unit
// tests and in-process callers equivalent to JSON-RPC callers ([]any).
func stringsAt(payload map[string]any, key string) []string {
	switch value := payload[key].(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
