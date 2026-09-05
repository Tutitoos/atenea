package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func (r *Runner) runImplementations(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	repo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	file, _ := stringAt(req.Payload, "file")
	line, _ := intAt(req.Payload, "line")
	column, _ := intAt(req.Payload, "column")
	if file == "" || line < 1 || column < 1 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "symbol.implementations requires file and positive line and column")
	}
	if _, err := within(root, file); err != nil {
		return nil, nil, err
	}
	limit := 50
	if value, ok := intAt(req.Payload, "limit"); ok {
		limit = value
	}
	if limit < 1 || limit > 500 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "implementation limit must be between 1 and 500")
	}
	detection, _ := stringAt(req.Payload, "detection")
	if detection != "" && detection != "declared" && detection != "structural" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "detection must be declared or structural")
	}
	scope := scopeEntries(req.Payload)
	for _, prefix := range scope {
		if _, err := within(root, prefix); err != nil {
			return nil, nil, err
		}
	}
	name, _ := stringAt(req.Payload, "name")
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilityImplementations, repo, file, line, name)
	if err != nil {
		return nil, nil, err
	}
	qualified := decl.QualifiedName
	if qualified == "" {
		qualified = decl.Name
	}
	args := map[string]any{"repository": repo, "path": file, "qualified_name": qualified, "repo": repo, "limit": limit}
	if len(scope) > 0 {
		args["paths"] = scope
	}
	if detection != "" {
		args["detection"] = detection
	}
	if cursor, ok := stringAt(req.Payload, "cursor"); ok {
		args["cursor"] = cursor
	}
	text, err := sess.Call(ctx, toolImplementations, args)
	if err != nil {
		return nil, nil, err
	}
	var answer struct {
		SnapshotID   int    `json:"snapshot_id"`
		Total        int    `json:"total"`
		Truncated    bool   `json:"truncated"`
		NextCursor   string `json:"next_cursor"`
		Completeness *struct {
			Verdict string `json:"verdict"`
		} `json:"completeness"`
		Results *struct {
			Implementations []struct {
				Repository    string `json:"repository"`
				File          string `json:"file_path"`
				Line          int    `json:"start_line"`
				QualifiedName string `json:"qualified_name"`
				StableKey     string `json:"stable_key"`
				Confidence    string `json:"confidence"`
				Provenance    string `json:"provenance"`
				Detection     string `json:"detection"`
			} `json:"implementations"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, err
	}
	if answer.Results == nil || answer.Results.Implementations == nil || answer.Completeness == nil || answer.SnapshotID < 1 || (answer.Completeness.Verdict != "COMPLETE" && answer.Completeness.Verdict != "LOWER_BOUND") {
		return nil, nil, fmt.Errorf("find_implementations omitted generation, results or coverage")
	}
	meta, err := queryMetadata(toolImplementations, text, true)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, meta...)
	rows := make([]any, 0, len(answer.Results.Implementations))
	for _, row := range answer.Results.Implementations {
		if row.Repository != repo || row.Line < 1 || row.File == "" || row.QualifiedName == "" || row.StableKey == "" || row.Confidence == "" || row.Provenance == "" {
			return nil, nil, fmt.Errorf("find_implementations returned an invalid canonical location")
		}
		if _, err := within(root, row.File); err != nil {
			return nil, nil, err
		}
		record := map[string]any{"path": row.File, "line": row.Line, "repository": row.Repository, "qualified_name": row.QualifiedName, "stable_key": row.StableKey, "confidence": row.Confidence, "provenance": row.Provenance, "detection": row.Detection}
		if boolAt(req.Payload, "include_snippet") {
			snippet, err := r.snippetAt(root, row.File, row.Line, snippetWindow(req.Payload))
			if err != nil {
				return nil, nil, err
			}
			record["snippet"] = snippet
		}
		rows = append(rows, record)
	}
	result := map[string]any{"locations": rows, "truncated": answer.Truncated, "snapshot_id": answer.SnapshotID, "total": answer.Total, "completeness": answer.Completeness.Verdict, "coverage_notes": notes}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	return result, notes, req.Capability.ValidateOutput(result)
}
