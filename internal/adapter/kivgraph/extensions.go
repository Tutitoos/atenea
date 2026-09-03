package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func (r *Runner) runSource(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	repo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	// Round-trip only the already validated record list, not arbitrary arguments.
	raw, err := json.Marshal(req.Payload["symbols"])
	if err != nil {
		return nil, nil, err
	}
	var rows []struct {
		Path          string `json:"path"`
		QualifiedName string `json:"qualified_name"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) < 1 || len(rows) > 20 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "symbol.source requires 1 to 20 symbols")
	}
	args := make([]any, 0, len(rows))
	for _, row := range rows {
		if row.Path == "" || strings.TrimSpace(row.QualifiedName) == "" {
			return nil, nil, contract.Fail(contract.FailureInvalidInput, "source requires path and qualified_name")
		}
		if _, err := within(root, row.Path); err != nil {
			return nil, nil, err
		}
		if r.isSensitive(row.Path) {
			return nil, nil, contract.Fail(contract.FailurePermissionDenied, "source path is sensitive")
		}
		args = append(args, map[string]any{"repository": repo, "path": row.Path, "qualified_name": row.QualifiedName})
	}
	lines, _ := intAt(req.Payload, "context_lines")
	if lines < 0 || lines > 100 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "context_lines must be between 0 and 100")
	}
	text, err := sess.Call(ctx, "get_source", map[string]any{"symbols": args, "context_lines": lines})
	if err != nil {
		return nil, nil, err
	}
	if len(text) > 300000 {
		return nil, nil, fmt.Errorf("get_source exceeded its bounded response")
	}
	result := map[string]any{"source": text}
	return result, []string{"Source bytes come from current files; source reanchoring does not establish graph edges."}, req.Capability.ValidateOutput(result)
}

func (r *Runner) runRepositories(ctx context.Context, sess Session, req contract.RunRequest) (map[string]any, []string, error) {
	args := map[string]any{"limit": 50}
	if limit, ok := intAt(req.Payload, "limit"); ok {
		if limit < 1 || limit > 500 {
			return nil, nil, contract.Fail(contract.FailureInvalidInput, "repository limit must be between 1 and 500")
		}
		args["limit"] = limit
	}
	if cursor, ok := stringAt(req.Payload, "cursor"); ok {
		args["cursor"] = cursor
	}
	text, err := sess.Call(ctx, "list_repositories", args)
	if err != nil {
		return nil, nil, err
	}
	notes, err := queryMetadata("list_repositories", text, false)
	if err != nil {
		return nil, nil, err
	}
	var answer struct {
		Results []struct {
			Name          string `json:"name"`
			Path          string `json:"path"`
			Derived       bool   `json:"derived"`
			IndexedCommit string `json:"indexed_commit"`
			CurrentCommit string `json:"current_commit"`
			Moved         bool   `json:"moved"`
		} `json:"results"`
		Truncated  bool   `json:"truncated"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, err
	}
	if answer.Results == nil {
		return nil, nil, fmt.Errorf("list_repositories omitted results")
	}
	rows := make([]any, 0, len(answer.Results))
	for _, row := range answer.Results {
		if row.Name == "" || row.Path == "" {
			return nil, nil, fmt.Errorf("invalid graph repository")
		}
		rows = append(rows, map[string]any{"name": row.Name, "path": row.Path, "derived": row.Derived, "indexed_commit": row.IndexedCommit, "current_commit": row.CurrentCommit, "moved": row.Moved})
	}
	result := map[string]any{"repositories": rows, "truncated": answer.Truncated}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	return result, notes, req.Capability.ValidateOutput(result)
}

func (r *Runner) runSymbolImpact(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	repo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	file, _ := stringAt(req.Payload, "file")
	line, _ := intAt(req.Payload, "line")
	if file == "" || line < 1 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "symbol.impact requires file and positive line")
	}
	if _, err := within(root, file); err != nil {
		return nil, nil, err
	}
	name, _ := stringAt(req.Payload, "name")
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilitySymbolImpact, repo, file, line, name)
	if err != nil {
		return nil, nil, err
	}
	qualified := decl.QualifiedName
	if qualified == "" {
		qualified = decl.Name
	}
	args := map[string]any{"repository": repo, "path": file, "qualified_name": qualified, "depth": 3, "max_nodes": 5000, "limit": 50, "view": "full", "response_format": "detailed"}
	for _, bound := range []struct {
		name     string
		min, max int
	}{{"depth", 0, 5}, {"max_nodes", 1, 25000}, {"limit", 1, 500}} {
		if value, ok := intAt(req.Payload, bound.name); ok {
			if value < bound.min || value > bound.max {
				return nil, nil, contract.Fail(contract.FailureInvalidInput, "%s outside bounds", bound.name)
			}
			args[bound.name] = value
		}
	}
	for _, key := range []string{"cursor", "confidence", "edge_kinds", "include_derived"} {
		if value, ok := req.Payload[key]; ok {
			args[key] = value
		}
	}
	text, err := sess.Call(ctx, toolBlast, args)
	if err != nil {
		return nil, nil, err
	}
	meta, err := queryMetadata(toolBlast, text, true)
	if err != nil {
		return nil, nil, err
	}
	var answer struct {
		Results    *blastRadius `json:"results"`
		Truncated  bool         `json:"truncated"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, err
	}
	if answer.Results == nil {
		return nil, nil, fmt.Errorf("blast radius omitted results")
	}
	rows := make([]any, 0, len(answer.Results.Symbols))
	for _, row := range answer.Results.Symbols {
		if row.Repository == "" || row.FilePath == "" || row.StartLine < 1 || row.Depth < 1 || row.QualifiedName == "" || row.Kind == "" {
			return nil, nil, fmt.Errorf("invalid affected declaration")
		}
		rows = append(rows, map[string]any{"repository": row.Repository, "path": row.FilePath, "line": row.StartLine, "name": row.QualifiedName, "kind": row.Kind, "depth": row.Depth})
	}
	result := map[string]any{"symbols": rows, "truncated": answer.Truncated || answer.Results.TraversalTruncated}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	return result, append(notes, meta...), req.Capability.ValidateOutput(result)
}
