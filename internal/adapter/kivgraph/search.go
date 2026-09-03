package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// runSearch preserves the neutral declaration-search contract. Scope is
// applied upstream, not by discarding rows from an already bounded page.
func (r *Runner) runSearch(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	query, ok := stringAt(req.Payload, "query")
	if !ok || strings.TrimSpace(query) == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "symbol.search requires a non-empty query")
	}
	limit := 50
	if value, present := intAt(req.Payload, "limit"); present {
		limit = value
	}
	if limit < 1 || limit > 200 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "symbol.search limit must be between 1 and 200")
	}
	scopes, valid := stringListAt(req.Payload, "scope")
	if hasField(req.Payload, "scope") && !valid || len(scopes) > 1 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "Kivgraph symbol.search accepts at most one scope; narrow to a common parent")
	}
	repository, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	args := map[string]any{"name": query, "mode": "substring", "repo": repository, "limit": limit, "view": "full", "response_format": "detailed"}
	for _, key := range []string{"mode", "kind", "cursor"} {
		if value, present := req.Payload[key]; present {
			args[key] = value
		}
	}
	if len(scopes) == 1 {
		if _, err := within(root, scopes[0]); err != nil {
			return nil, nil, err
		}
		args["path_prefix"] = scopes[0]
	}
	text, err := sess.Call(ctx, "find_symbol", args)
	if err != nil {
		return nil, nil, err
	}
	var answer struct {
		Results []struct {
			StableKey     string `json:"stable_key"`
			QualifiedName string `json:"qualified_name"`
			Kind          string `json:"kind"`
			Repository    string `json:"repository"`
			FilePath      string `json:"file_path"`
			StartLine     int    `json:"start_line"`
			EndLine       int    `json:"end_line"`
		} `json:"results"`
		Truncated  bool   `json:"truncated"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, err
	}
	if answer.Results == nil {
		return nil, nil, fmt.Errorf("find_symbol omitted its results")
	}
	// A complete non-empty declaration page may omit the verdict upstream.
	// It establishes those declarations, never an assertion of absence.
	notes, err := queryMetadata("find_symbol", text, len(answer.Results) == 0 || answer.Truncated)
	if err != nil {
		return nil, nil, err
	}
	matches := make([]any, 0, len(answer.Results))
	for i, row := range answer.Results {
		if row.Repository != repository || row.QualifiedName == "" || row.Kind == "" || row.FilePath == "" || row.StartLine < 1 || row.EndLine < row.StartLine {
			return nil, nil, fmt.Errorf("find_symbol returned an invalid or foreign declaration")
		}
		if _, err := within(root, row.FilePath); err != nil {
			return nil, nil, err
		}
		match := map[string]any{"name": row.QualifiedName, "kind": row.Kind, "path": row.FilePath, "line": row.StartLine, "end_line": row.EndLine, "rank": i + 1}
		if row.StableKey != "" {
			match["stable_key"] = row.StableKey
		}
		matches = append(matches, match)
	}
	result := map[string]any{"matches": matches, "truncated": answer.Truncated}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	return result, notes, req.Capability.ValidateOutput(result)
}
