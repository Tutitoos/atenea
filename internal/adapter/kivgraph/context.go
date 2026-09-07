package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func (r *Runner) runContext(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	task, _ := stringAt(req.Payload, "task")
	if strings.TrimSpace(task) == "" || utf8.RuneCountInString(task) > maximumIntentChars {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "Kivgraph context requires a task of 1 to 400 characters")
	}
	limit := 20
	if value, ok := intAt(req.Payload, "limit"); ok {
		limit = value
	}
	if limit < 1 || limit > 20 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "Kivgraph context limit must be between 1 and 20")
	}
	repo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	scopes, _ := stringListAt(req.Payload, "scope")
	if len(scopes) > 1 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "Kivgraph context accepts one common-parent scope")
	}
	args := map[string]any{"intent": task, "repo": repo, "limit": limit, "view": "full", "response_format": "detailed"}
	if len(scopes) == 1 {
		if _, err := within(root, scopes[0]); err != nil {
			return nil, nil, err
		}
		args["path_prefix"] = scopes[0]
	}
	if keys, ok := stringListAt(req.Payload, "keywords"); ok {
		if len(keys) > maximumIntentKeywords {
			return nil, nil, contract.Fail(contract.FailureInvalidInput, "context accepts at most 16 keywords")
		}
		args["keywords"] = keys
	}
	if cursor, ok := stringAt(req.Payload, "cursor"); ok && strings.TrimSpace(cursor) != "" {
		args["cursor"] = cursor
	}
	text, err := sess.Call(ctx, toolIntent, args)
	if err != nil {
		return nil, nil, err
	}
	notes, err := queryMetadata(toolIntent, text, false)
	if err != nil {
		return nil, nil, err
	}
	var answer intentAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, err
	}
	if answer.Results == nil {
		return nil, nil, fmt.Errorf("intent response omitted results")
	}
	symbols := make([]any, 0, len(answer.Results.Symbols))
	rows := make([]contextRow, 0, len(answer.Results.Symbols))
	snippets := []any{}
	sourceTrimmed := 0
	var sourceCeiling int
	snippetLines := 40
	if value, ok := intAt(req.Payload, "snippet_lines"); ok {
		snippetLines = value
	}
	if snippetLines < 1 || snippetLines > 200 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "snippet_lines must be between 1 and 200")
	}
	if len(answer.Results.Symbols) > limit {
		return nil, nil, fmt.Errorf("intent response exceeded requested limit")
	}
	for _, row := range answer.Results.Symbols {
		if row.Repository != repo || row.QualifiedName == "" || row.FilePath == "" || row.Kind == "" || row.StartLine < 1 || row.EndLine < row.StartLine {
			return nil, nil, fmt.Errorf("invalid or foreign context declaration")
		}
		if _, err := within(root, row.FilePath); err != nil {
			return nil, nil, err
		}
		selector := map[string]any{"repository": repo, "path": row.FilePath, "qualified_name": row.QualifiedName}
		symbols = append(symbols, map[string]any{"name": row.QualifiedName, "path": row.FilePath, "line": row.StartLine, "kind": row.Kind})
		rows = append(rows, contextRow{row: row, selector: selector})
	}
	if boolAt(req.Payload, "include_snippet") {
		selectors := make([]any, 0, len(rows))
		for _, item := range rows {
			if r.isSensitive(item.row.FilePath) {
				return nil, nil, contract.Fail(contract.FailurePermissionDenied, "context source is sensitive")
			}
			selectors = append(selectors, item.selector)
		}
		// get_source rejects an empty selector list. An empty intent page is
		// still a valid bounded answer, so preserve its empty snippet list
		// without issuing a provider call that could not add evidence.
		if len(selectors) > 0 {
			// find_by_intent already carries the validated identity and range. One
			// bounded source request keeps every returned block attributable while
			// avoiding one get_symbol and one get_source round trip per result.
			source, err := sess.Call(ctx, "get_source", map[string]any{"symbols": selectors})
			if err != nil {
				return nil, nil, err
			}
			if len(source) > 300000 {
				return nil, nil, fmt.Errorf("context source exceeded its byte limit")
			}
			parsed := parseContextSourceForRows(source, rows)
			sourceTrimmed = parsed.trimmed
			sourceCeiling = parsed.ceiling
			blocks := parsed.blocks
			for _, item := range rows {
				block, found := blocks[contextSelectorKey(repo, item.row.FilePath, item.row.QualifiedName)]
				snippets = append(snippets, renderContextSnippet(item.row, block, found, snippetLines, sourceTrimmed, sourceCeiling))
			}
		}
	}
	result := map[string]any{"symbols": symbols, "truncated": answer.Truncated}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	if sourceTrimmed > 0 {
		result["source_trimmed"] = sourceTrimmed
	}
	if boolAt(req.Payload, "include_snippet") {
		result["snippets"] = snippets
	}
	// No empty tests/related lists: absence of this evidence is not a claim that
	// no tests or related declarations exist.
	notes = append(notes, "Context ranks lexical candidates, not semantic edges; test coverage is not established.")
	if sourceTrimmed > 0 {
		notes = append(notes, fmt.Sprintf("Kivgraph get_source omitted %d body(ies) at its 262144-byte ceiling; omitted source is unavailable, not evidence of absence.", sourceTrimmed))
	}
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf("Kivgraph intent results are partial; continue with cursor %q before drawing absence conclusions.", answer.NextCursor))
	}
	return result, notes, req.Capability.ValidateOutput(result)
}

// contextRow is the identity find_by_intent already proved. It is kept next
// to the selector so a grouped get_source response can be joined back to the
// exact result without trusting display order.
type contextRow struct {
	row      intentSymbol
	selector map[string]any
}

type contextSourceBlock struct {
	available bool
	body      string
	reason    string
}

type contextSourceHeader struct {
	repository      string
	file            string
	qualifiedName   string
	available       bool
	reason          string
	startLine       int
	endLine         int
	reanchorPresent bool
	reanchorValid   bool
	reanchorShift   int
}

type contextSourceParse struct {
	blocks  map[string]contextSourceBlock
	trimmed int
	ceiling int
}

func contextSelectorKey(repository, file, qualifiedName string) string {
	return repository + "\x00" + file + "\x00" + qualifiedName
}

// parseContextSourceForRows decodes Kivgraph's measured text envelope. An @
// header opens a source body and a ! header records a provider-declared
// unavailable body. For @ bodies, the inclusive header range is consumed
// exactly; this is what prevents an adversarial source line beginning with
// "@ " or "! " from being parsed as a second block.
func parseContextSourceForRows(source string, expected []contextRow) contextSourceParse {
	parsed := contextSourceParse{blocks: make(map[string]contextSourceBlock), ceiling: 262144}
	expectedRanges := make(map[string][2]int, len(expected))
	expectedOrder := make([]string, 0, len(expected))
	for _, row := range expected {
		key := contextSelectorKey(row.row.Repository, row.row.FilePath, row.row.QualifiedName)
		expectedRanges[key] = [2]int{row.row.StartLine, row.row.EndLine}
		expectedOrder = append(expectedOrder, key)
	}
	// renderSourceText appends exactly one separator newline after every body.
	// Remove that framing newline once, retaining an additional newline that is
	// part of the last body's code.
	source = strings.TrimSuffix(source, "\n")
	lines := strings.Split(source, "\n")
sourceLines:
	for index := 0; index < len(lines); {
		if trimmed, ceiling, ok := parseContextSourceSummary(lines[index]); ok {
			parsed.trimmed = trimmed
			if ceiling > 0 {
				parsed.ceiling = ceiling
			}
			index++
			continue
		}
		header, ok := parseContextSourceHeaderDetails(lines[index])
		if !ok {
			index++
			continue
		}
		key := contextSelectorKey(header.repository, header.file, header.qualifiedName)
		want, expected := expectedRanges[key]
		if len(expectedRanges) > 0 && !expected {
			// A source line can resemble a header. Only a declaration returned
			// by find_by_intent is allowed to open a block in this composition.
			index++
			continue
		}
		if !header.available {
			parsed.blocks[key] = contextSourceBlock{available: false, reason: header.reason}
			index++
			continue
		}
		startLine, endLine := header.startLine, header.endLine
		if startLine < 1 || endLine < startLine {
			if expected {
				startLine, endLine = want[0], want[1]
			} else {
				index++
				continue
			}
		}
		if expected {
			expectedStart, expectedEnd := want[0], want[1]
			if header.reanchorPresent {
				shiftedStart, startOK := addContextLineOffset(expectedStart, header.reanchorShift)
				shiftedEnd, endOK := addContextLineOffset(expectedEnd, header.reanchorShift)
				if !header.reanchorValid || !startOK || !endOK || startLine != shiftedStart || endLine != shiftedEnd {
					markContextSourceUnknown(parsed.blocks, expectedOrder, key, fmt.Sprintf("invalid source re-anchoring for %q: provider range %d-%d does not match the validated range %d-%d", key, startLine, endLine, expectedStart, expectedEnd))
					index = len(lines)
					continue sourceLines
				}
			} else if startLine != expectedStart || endLine != expectedEnd {
				markContextSourceUnknown(parsed.blocks, expectedOrder, key, fmt.Sprintf("source range mismatch for %q: provider returned %d-%d, validated range is %d-%d", key, startLine, endLine, expectedStart, expectedEnd))
				index = len(lines)
				continue sourceLines
			}
		}
		// endLine-startLine+1 is safe after the ordering check, but adding it
		// to an input-derived index is not safe until it fits in the remaining
		// source. This also bounds work for a hostile MaxInt end_line.
		bodyLines := endLine - startLine + 1
		bodyStart := index + 1
		remaining := len(lines) - bodyStart
		if remaining < 0 {
			remaining = 0
		}
		bodyCount := bodyLines
		if bodyCount > remaining {
			// The declared body cannot fit. A following expected header inside
			// these bytes makes the framing ambiguous, so do not cut there and
			// accidentally attribute the following body to this selector.
			for bodyIndex := bodyStart; bodyIndex < len(lines); bodyIndex++ {
				candidate, candidateOK := parseContextSourceHeaderDetails(lines[bodyIndex])
				if !candidateOK {
					continue
				}
				candidateKey := contextSelectorKey(candidate.repository, candidate.file, candidate.qualifiedName)
				if candidateKey != key {
					if _, candidateExpected := expectedRanges[candidateKey]; candidateExpected {
						markContextSourceUnknown(parsed.blocks, expectedOrder, key, fmt.Sprintf("ambiguous source framing: expected selector %q appeared inside a body declaring %d line(s)", candidateKey, bodyLines))
						index = len(lines)
						continue sourceLines
					}
				}
			}
			markContextSourceUnknown(parsed.blocks, expectedOrder, key, fmt.Sprintf("provider returned only %d of %d declared source line(s); source availability is unknown", remaining, bodyLines))
			index = len(lines)
			continue
		}
		bodyEnd := bodyStart + bodyCount
		if len(expectedRanges) > 0 {
			for bodyIndex := bodyStart; bodyIndex < bodyEnd; bodyIndex++ {
				candidate, candidateOK := parseContextSourceHeaderDetails(lines[bodyIndex])
				if !candidateOK {
					continue
				}
				candidateKey := contextSelectorKey(candidate.repository, candidate.file, candidate.qualifiedName)
				if candidateKey != key {
					if _, candidateExpected := expectedRanges[candidateKey]; candidateExpected {
						markContextSourceUnknown(parsed.blocks, expectedOrder, key, fmt.Sprintf("ambiguous source framing: expected selector %q appeared inside a body declaring %d line(s)", candidateKey, bodyLines))
						index = len(lines)
						continue sourceLines
					}
				}
			}
		}
		if bodyEnd <= bodyStart {
			markContextSourceUnknown(parsed.blocks, expectedOrder, key, "provider returned an empty attributable body")
			index = bodyEnd
			continue
		}
		parsed.blocks[key] = contextSourceBlock{available: true, body: strings.Join(lines[bodyStart:bodyEnd], "\n")}
		index = bodyEnd
	}
	return parsed
}

func markContextSourceUnknown(blocks map[string]contextSourceBlock, order []string, currentKey, reason string) {
	start := 0
	for index, key := range order {
		if key == currentKey {
			start = index
			break
		}
	}
	for _, key := range order[start:] {
		blocks[key] = contextSourceBlock{available: false, reason: reason}
	}
}

func addContextLineOffset(line, offset int) (int, bool) {
	if offset > 0 && line > int(^uint(0)>>1)-offset {
		return 0, false
	}
	if offset < 0 && line < -int(^uint(0)>>1)-1-offset {
		return 0, false
	}
	shifted := line + offset
	return shifted, shifted >= 1
}

func parseContextSourceHeaderDetails(line string) (contextSourceHeader, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 2 || (trimmed[0] != '@' && trimmed[0] != '!') || trimmed[1] != ' ' {
		return contextSourceHeader{}, false
	}
	fields := strings.Fields(trimmed[2:])
	if len(fields) == 0 {
		return contextSourceHeader{}, false
	}
	// Profile-aware get_source prefixes the repository with `[profile]`. The
	// context path requests one repository/profile, but accepting the prefix
	// keeps this parser safe across the two measured response forms.
	start := 0
	if strings.HasPrefix(fields[0], "[") && strings.HasSuffix(fields[0], "]") {
		start = 1
	}
	if trimmed[0] == '@' {
		if len(fields) < start+4 {
			return contextSourceHeader{}, false
		}
		rangeField := fields[start+1]
		colon := strings.LastIndexByte(rangeField, ':')
		if colon <= 0 || colon == len(rangeField)-1 {
			return contextSourceHeader{}, false
		}
		rangeParts := strings.SplitN(rangeField[colon+1:], "-", 2)
		if len(rangeParts) != 2 {
			return contextSourceHeader{}, false
		}
		startLine, startErr := strconv.Atoi(rangeParts[0])
		endLine, endErr := strconv.Atoi(rangeParts[1])
		if startErr != nil || endErr != nil {
			return contextSourceHeader{}, false
		}
		reanchorPresent, reanchorValid, reanchorShift := parseContextReanchorMarker(trimmed)
		return contextSourceHeader{
			repository: fields[start], file: rangeField[:colon], qualifiedName: fields[start+3], available: true,
			startLine: startLine, endLine: endLine, reanchorPresent: reanchorPresent,
			reanchorValid: reanchorValid, reanchorShift: reanchorShift,
		}, true
	}
	if len(fields) < start+3 {
		return contextSourceHeader{}, false
	}
	repository, file, qualifiedName := fields[start], fields[start+1], fields[start+2]
	if repository == "" || file == "" || qualifiedName == "" {
		return contextSourceHeader{}, false
	}
	reasonStart := start + 3
	reason := ""
	if reasonStart < len(fields) {
		reason = strings.Join(fields[reasonStart:], " ")
	}
	return contextSourceHeader{repository: repository, file: file, qualifiedName: qualifiedName, reason: reason}, true
}

func parseContextReanchorMarker(line string) (present, valid bool, shift int) {
	marker := "[file changed, re-anchored "
	if !strings.Contains(line, marker) {
		return false, false, 0
	}
	if strings.Count(line, marker) != 1 {
		return true, false, 0
	}
	start := strings.Index(line, marker) + len(marker)
	endOffset := strings.IndexByte(line[start:], ']')
	if endOffset < 0 {
		return true, false, 0
	}
	value := line[start : start+endOffset]
	if value == "" || (value[0] != '+' && value[0] != '-') {
		return true, false, 0
	}
	shift, err := strconv.Atoi(value)
	if err != nil {
		return true, false, 0
	}
	return true, true, shift
}

func parseContextSourceSummary(line string) (trimmed, ceiling int, ok bool) {
	marker := " trimmed "
	index := strings.Index(line, marker)
	if index < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(line[index+len(marker):])
	if len(fields) < 5 || fields[1] != "at" || fields[2] != "the" || fields[4] != "byte" {
		return 0, 0, false
	}
	trimmed, trimmedErr := strconv.Atoi(fields[0])
	ceiling, ceilingErr := strconv.Atoi(fields[3])
	if trimmedErr != nil || ceilingErr != nil || trimmed < 0 || ceiling <= 0 {
		return 0, 0, false
	}
	return trimmed, ceiling, true
}

func renderContextSnippet(row intentSymbol, block contextSourceBlock, found bool, maxLines, sourceTrimmed, sourceCeiling int) map[string]any {
	snippet := map[string]any{"name": row.QualifiedName, "path": row.FilePath, "line": row.StartLine}
	if !found || !block.available || strings.TrimSpace(block.body) == "" {
		reason := "provider returned no attributable body"
		if found && block.reason != "" {
			reason = block.reason
		} else if !found && sourceTrimmed > 0 {
			reason = fmt.Sprintf("provider response omitted this body after trimming %d body(ies) at the %d-byte ceiling; source availability is unknown", sourceTrimmed, sourceCeiling)
		}
		snippet["code"] = fmt.Sprintf("[Source unavailable: %s; declaration existence is not inferred.]", reason)
		return snippet
	}
	lines := strings.Split(block.body, "\n")
	if len(lines) > maxLines {
		snippet["code"] = strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n[Source block truncated by Atenea after %d lines; remaining source is unavailable in this bounded result.]", maxLines)
		return snippet
	}
	snippet["code"] = block.body
	return snippet
}
