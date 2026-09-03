package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
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
	snippets := []any{}
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
		if row.Repository != repo || row.QualifiedName == "" || row.FilePath == "" || row.Kind == "" || row.StartLine < 1 {
			return nil, nil, fmt.Errorf("invalid or foreign context declaration")
		}
		if _, err := within(root, row.FilePath); err != nil {
			return nil, nil, err
		}
		selector := map[string]any{"repository": repo, "path": row.FilePath, "qualified_name": row.QualifiedName}
		detail, err := sess.Call(ctx, toolGet, selector)
		if err != nil {
			return nil, nil, err
		}
		var card getSymbolAnswer
		if err := json.Unmarshal([]byte(detail), &card); err != nil {
			return nil, nil, err
		}
		if card.Results == nil || card.Results.FilePath != row.FilePath {
			return nil, nil, fmt.Errorf("context symbol identity changed")
		}
		symbols = append(symbols, map[string]any{"name": row.QualifiedName, "path": row.FilePath, "line": row.StartLine, "kind": row.Kind})
		if boolAt(req.Payload, "include_snippet") {
			if r.isSensitive(row.FilePath) {
				return nil, nil, contract.Fail(contract.FailurePermissionDenied, "context source is sensitive")
			}
			source, err := sess.Call(ctx, "get_source", map[string]any{"symbols": []any{selector}})
			if err != nil {
				return nil, nil, err
			}
			if len(source) > 300000 {
				return nil, nil, fmt.Errorf("context source exceeded its byte limit")
			}
			lines := strings.Split(source, "\n")
			if len(lines) > snippetLines {
				source = strings.Join(lines[:snippetLines], "\n") + "\n[Source block truncated by Atenea]"
			}
			snippets = append(snippets, map[string]any{"name": row.QualifiedName, "path": row.FilePath, "line": row.StartLine, "code": source})
		}
	}
	result := map[string]any{"symbols": symbols}
	if boolAt(req.Payload, "include_snippet") {
		result["snippets"] = snippets
	}
	// No empty tests/related lists: absence of this evidence is not a claim that
	// no tests or related declarations exist.
	notes = append(notes, "Context ranks lexical candidates, not semantic edges; test coverage is not established.")
	return result, notes, req.Capability.ValidateOutput(result)
}
