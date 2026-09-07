package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

var (
	implementationLanguages   = map[string]struct{}{"go": {}, "typescript": {}}
	implementationEdgeKinds   = map[string]struct{}{"IMPLEMENTS": {}, "OVERRIDES": {}}
	implementationDetections  = map[string]struct{}{"declared": {}, "structural": {}, "typed": {}, "textual": {}, "candidate": {}, "unresolved": {}}
	implementationConfidences = map[string]struct{}{
		"EXACT_TYPECHECKED": {}, "EXACT_DECLARATION_MAPPED": {}, "EXACT_PACKAGE_MAPPED": {}, "STRUCTURAL_CERTAIN": {},
		"CANDIDATE": {}, "UNRESOLVED": {},
	}
	implementationProvenances = map[string]struct{}{
		"TYPESCRIPT_CHECKER": {}, "TYPESCRIPT_IMPL_DECLARED": {}, "TYPESCRIPT_IMPL_STRUCTURAL": {},
		"TYPESCRIPT_MODULE_RESOLUTION": {}, "TYPESCRIPT_DECLARATION_MAP": {}, "TYPESCRIPT_PROJECT_REFERENCE": {},
		"GO_TYPES_DEF": {}, "GO_TYPES_USE": {}, "GO_TYPES_SELECTION": {}, "GO_AST_CALL": {},
		"GO_AST_CALLBACK": {}, "GO_OBJECT_PATH": {}, "TREE_SITTER_SYNTAX": {}, "PACKAGE_MANIFEST": {},
	}
)

// implementationLanguage is deliberately called before opening a Kivgraph
// session. Unsupported languages must not trigger graph_status or a provider
// identity probe.
func implementationLanguage(payload map[string]any) (string, error) {
	file, ok := stringAt(payload, "file")
	if !ok || strings.TrimSpace(file) == "" {
		return "", contract.Fail(contract.FailureInvalidInput, "symbol.implementations requires file")
	}
	inferred := ""
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go":
		inferred = "go"
	case ".ts", ".tsx", ".mts", ".cts":
		inferred = "typescript"
	default:
		return "", contract.Fail(contract.FailureInvalidInput, "symbol.implementations supports only Go and TypeScript source files")
	}
	language, present := stringAt(payload, "language")
	if present {
		if _, known := implementationLanguages[language]; !known {
			return "", contract.Fail(contract.FailureInvalidInput, "symbol.implementations language %q is unsupported", language)
		}
		if inferred != "" && inferred != language {
			return "", contract.Fail(contract.FailureInvalidInput, "symbol.implementations language %q does not match %s", language, file)
		}
		return language, nil
	}
	return inferred, nil
}

func closedImplementationValue(kind, value string, allowed map[string]struct{}) error {
	if _, ok := allowed[value]; !ok {
		return fmt.Errorf("find_implementations returned invalid %s %q", kind, value)
	}
	return nil
}

func isExactImplementationConfidence(confidence string) bool {
	_, ok := map[string]struct{}{
		"EXACT_TYPECHECKED": {}, "EXACT_DECLARATION_MAPPED": {},
		"EXACT_PACKAGE_MAPPED": {}, "STRUCTURAL_CERTAIN": {},
	}[confidence]
	return ok
}

func validImplementationEvidence(language, provenance, detection, confidence, edgeKind string) bool {
	if !isExactImplementationConfidence(confidence) || (edgeKind != "IMPLEMENTS" && edgeKind != "OVERRIDES") {
		return false
	}
	switch language {
	case "go":
		return confidence == "EXACT_TYPECHECKED" && ((provenance == "GO_TYPES_USE" && detection == "structural") ||
			(provenance == "GO_OBJECT_PATH" && detection == "typed"))
	case "typescript":
		return isExactImplementationConfidence(confidence) &&
			((provenance == "TYPESCRIPT_IMPL_DECLARED" && detection == "declared") ||
				(provenance == "TYPESCRIPT_IMPL_STRUCTURAL" && detection == "structural"))
	default:
		return false
	}
}

func canonicalImplementationPath(root, name string) (string, error) {
	resolved, err := within(root, name)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if canonical, evalErr := filepath.EvalSymlinks(rootAbs); evalErr == nil {
		rootAbs = canonical
	}
	relative, err := filepath.Rel(rootAbs, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", contract.Fail(contract.FailureInvalidInput, "implementation path %q is outside repository", name)
	}
	return filepath.ToSlash(filepath.Clean(relative)), nil
}

func (r *Runner) runImplementations(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	repo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	language, err := implementationLanguage(req.Payload)
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
	if detection != "" {
		if err := closedImplementationValue("detection", detection, map[string]struct{}{"declared": {}, "structural": {}}); err != nil {
			return nil, nil, contract.Fail(contract.FailureInvalidInput, "%s", err)
		}
	}
	scope := scopeEntries(req.Payload)
	for _, prefix := range scope {
		if _, err := within(root, prefix); err != nil {
			return nil, nil, err
		}
	}
	name, _ := stringAt(req.Payload, "name")
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilityImplementations, repo, file, line, name, declarationOptions{strict: true, expectedSnapshot: status.SnapshotID})
	if err != nil {
		return nil, nil, err
	}
	qualified := decl.QualifiedName
	if qualified == "" {
		qualified = decl.Name
	}
	args := map[string]any{"repository": repo, "path": file, "qualified_name": qualified, "repo": repo, "limit": limit, "language": language}
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
		SnapshotID int    `json:"snapshot_id"`
		Total      int    `json:"total"`
		Returned   *int   `json:"returned"`
		Truncated  bool   `json:"truncated"`
		NextCursor string `json:"next_cursor"`
		Coverage   *struct {
			Exact             int `json:"exact"`
			Candidate         int `json:"candidate"`
			UnresolvedRelated int `json:"unresolved_related"`
			PackageLevel      int `json:"package_level"`
		} `json:"coverage"`
		Completeness *struct {
			Verdict string `json:"verdict"`
		} `json:"completeness"`
		Results *struct {
			Implementations []struct {
				Repository    string `json:"repository"`
				File          string `json:"file_path"`
				Path          string `json:"path"`
				Line          int    `json:"start_line"`
				LineAlias     int    `json:"line"`
				QualifiedName string `json:"qualified_name"`
				StableKey     string `json:"stable_key"`
				Confidence    string `json:"confidence"`
				Provenance    string `json:"provenance"`
				Detection     string `json:"detection"`
				Language      string `json:"language"`
				EdgeKind      string `json:"edge_kind"`
				Relation      string `json:"relation"`
			} `json:"implementations"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, err
	}
	if answer.Results == nil || answer.Results.Implementations == nil || answer.Completeness == nil || answer.Coverage == nil || answer.Returned == nil || answer.SnapshotID < 1 || answer.Total < 0 {
		return nil, nil, fmt.Errorf("find_implementations omitted generation, results, coverage or total")
	}
	if answer.SnapshotID != status.SnapshotID {
		return nil, nil, fmt.Errorf("find_implementations generation %d differs from graph generation %d", answer.SnapshotID, status.SnapshotID)
	}
	verdict := answer.Completeness.Verdict
	if verdict != "COMPLETE" && verdict != "LOWER_BOUND" {
		return nil, nil, fmt.Errorf("find_implementations returned invalid completeness %q", verdict)
	}
	if len(answer.Results.Implementations) > limit || answer.Returned == nil || *answer.Returned < 0 || *answer.Returned != len(answer.Results.Implementations) || (!answer.Truncated && answer.NextCursor != "") || (answer.Truncated && answer.NextCursor == "") {
		return nil, nil, fmt.Errorf("find_implementations returned incoherent pagination")
	}
	cursorValue, _ := stringAt(req.Payload, "cursor")
	continuation := cursorValue != ""
	if (!continuation && !answer.Truncated && answer.Total != *answer.Returned) || answer.Total < *answer.Returned {
		return nil, nil, fmt.Errorf("find_implementations total is smaller than the returned page")
	}
	if answer.Coverage.Exact < 0 || answer.Coverage.Candidate < 0 || answer.Coverage.UnresolvedRelated < 0 || answer.Coverage.PackageLevel < 0 {
		return nil, nil, fmt.Errorf("find_implementations returned negative coverage")
	}
	meta, err := queryMetadata(toolImplementations, text, true)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, meta...)
	publicVerdict := verdict
	if verdict == "COMPLETE" && answer.Coverage.UnresolvedRelated > 0 {
		publicVerdict = "LOWER_BOUND"
		notes = append(notes, "Kivgraph coverage contains unresolved_related evidence; absence is not established")
	}
	rows := make([]any, 0, len(answer.Results.Implementations))
	coverage := map[string]int{"exact": answer.Coverage.Exact, "candidate": answer.Coverage.Candidate, "unresolved_related": answer.Coverage.UnresolvedRelated, "package_level": answer.Coverage.PackageLevel}
	for _, row := range answer.Results.Implementations {
		if row.File == "" {
			row.File = row.Path
		}
		if row.Line < 1 {
			row.Line = row.LineAlias
		}
		if row.EdgeKind == "" {
			row.EdgeKind = row.Relation
		}
		if row.Repository != repo || row.Line < 1 || row.File == "" || row.QualifiedName == "" || row.StableKey == "" || row.Confidence == "" || row.Provenance == "" || row.Detection == "" || row.Language == "" || row.EdgeKind == "" {
			return nil, nil, fmt.Errorf("find_implementations returned an invalid canonical location")
		}
		relative, err := canonicalImplementationPath(root, row.File)
		if err != nil {
			return nil, nil, err
		}
		if !inScope(relative, scope) {
			return nil, nil, contract.Fail(contract.FailureInvalidInput, "implementation path %q is outside requested scope", relative)
		}
		for kind, value := range map[string]string{"language": row.Language, "edge_kind": row.EdgeKind, "confidence": row.Confidence, "provenance": row.Provenance, "detection": row.Detection} {
			var allowed map[string]struct{}
			switch kind {
			case "language":
				allowed = implementationLanguages
			case "edge_kind":
				allowed = implementationEdgeKinds
			case "confidence":
				allowed = implementationConfidences
			case "provenance":
				allowed = implementationProvenances
			case "detection":
				allowed = implementationDetections
			}
			if err := closedImplementationValue(kind, value, allowed); err != nil {
				return nil, nil, err
			}
		}
		if row.Language != language || !validImplementationEvidence(row.Language, row.Provenance, row.Detection, row.Confidence, row.EdgeKind) {
			return nil, nil, fmt.Errorf("find_implementations returned contradictory semantic evidence")
		}
		record := map[string]any{"path": relative, "line": row.Line, "repository": row.Repository, "qualified_name": row.QualifiedName, "stable_key": row.StableKey, "confidence": row.Confidence, "provenance": row.Provenance, "detection": row.Detection, "resolution": "exact", "language": row.Language, "edge_kind": row.EdgeKind}
		if boolAt(req.Payload, "include_snippet") {
			snippet, err := r.snippetAt(root, relative, row.Line, snippetWindow(req.Payload))
			if err != nil {
				return nil, nil, err
			}
			record["snippet"] = snippet
		}
		rows = append(rows, record)
	}
	if coverage["exact"] != answer.Total {
		return nil, nil, fmt.Errorf("find_implementations coverage exact=%d differs from total=%d", coverage["exact"], answer.Total)
	}
	coverageRecord := map[string]any{"exact": coverage["exact"], "candidate": coverage["candidate"], "unresolved_related": coverage["unresolved_related"], "package_level": coverage["package_level"]}
	result := map[string]any{"locations": rows, "truncated": answer.Truncated, "snapshot_id": answer.SnapshotID, "total": answer.Total, "completeness": publicVerdict, "coverage_notes": notes, "coverage": coverageRecord}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	return result, notes, req.Capability.ValidateOutput(result)
}
