package kivgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	maximumIntentChars     = 400
	maximumIntentKeywords  = 16
	maximumIntentLimit     = 50
	maximumDependencyDepth = 5
	maximumDependencyNodes = 25_000
	maximumDependencyLimit = 500
)

type queryEnvelopeMetadata struct {
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor"`
	Coverage   struct {
		Exact             int `json:"exact"`
		Candidate         int `json:"candidate"`
		UnresolvedRelated int `json:"unresolved_related"`
		PackageLevel      int `json:"package_level"`
	} `json:"coverage"`
	Completeness *struct {
		Verdict             string      `json:"verdict"`
		BlindSpots          []blindSpot `json:"blind_spots"`
		MoreBlindSpots      int         `json:"more_blind_spots"`
		InvisibleScopes     []blindSpot `json:"invisible_scopes"`
		MoreInvisibleScopes int         `json:"more_invisible_scopes"`
		Fallback            *struct {
			Pattern string   `json:"pattern"`
			Paths   []string `json:"paths"`
		} `json:"fallback"`
	} `json:"completeness"`
	Guidance string `json:"guidance"`
}

type blindSpot struct {
	Reason           string `json:"reason"`
	Repository       string `json:"repository"`
	FilePath         string `json:"file_path"`
	StartLine        int    `json:"start_line"`
	RequestedSymbol  string `json:"requested_symbol"`
	RequestedPackage string `json:"requested_package"`
	Detail           string `json:"detail"`
}

// queryMetadata keeps Kivgraph's epistemic contract visible instead of
// flattening a bounded graph answer into an apparently complete list.
func queryMetadata(tool, text string, completenessRequired bool) ([]string, error) {
	var envelope queryEnvelopeMetadata
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return nil, fmt.Errorf("kivgraph %s: unreadable metadata: %w", tool, err)
	}
	if envelope.Truncated != (envelope.NextCursor != "") {
		return nil, fmt.Errorf("kivgraph %s: incoherent cursor (truncated=%t, next_cursor=%q)",
			tool, envelope.Truncated, envelope.NextCursor)
	}
	if envelope.Coverage.Exact < 0 || envelope.Coverage.Candidate < 0 ||
		envelope.Coverage.UnresolvedRelated < 0 || envelope.Coverage.PackageLevel < 0 {
		return nil, fmt.Errorf("kivgraph %s: coverage contains a negative count", tool)
	}

	var notes []string
	if envelope.Completeness == nil {
		if completenessRequired {
			return nil, fmt.Errorf("kivgraph %s: response has no completeness verdict", tool)
		}
	} else {
		complete := envelope.Completeness
		switch complete.Verdict {
		case "COMPLETE":
			if len(complete.BlindSpots) != 0 || complete.MoreBlindSpots != 0 ||
				len(complete.InvisibleScopes) != 0 || complete.MoreInvisibleScopes != 0 || complete.Fallback != nil {
				return nil, fmt.Errorf("kivgraph %s: COMPLETE response carries blind spots or a fallback", tool)
			}
			notes = append(notes, "Kivgraph completeness: COMPLETE; recorded index gaps cannot add to this answer")
		case "LOWER_BOUND":
			blind := len(complete.BlindSpots) + complete.MoreBlindSpots
			invisible := len(complete.InvisibleScopes) + complete.MoreInvisibleScopes
			if blind == 0 && invisible == 0 {
				return nil, fmt.Errorf("kivgraph %s: LOWER_BOUND response names no blind spot or invisible scope", tool)
			}
			note := fmt.Sprintf("Kivgraph completeness: LOWER_BOUND with %d blind spot(s) and %d invisible scope(s)", blind, invisible)
			if complete.Fallback != nil && complete.Fallback.Pattern != "" {
				note += fmt.Sprintf("; fallback %q", complete.Fallback.Pattern)
			}
			notes = append(notes, note)
		default:
			return nil, fmt.Errorf("kivgraph %s: unknown completeness verdict %q", tool, complete.Verdict)
		}
	}
	if strings.TrimSpace(envelope.Guidance) != "" {
		notes = append(notes, "Kivgraph guidance: "+strings.TrimSpace(envelope.Guidance))
	}
	return notes, nil
}

func outlineMetadata(text string) ([]string, error) {
	var page struct {
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		return nil, fmt.Errorf("kivgraph %s: unreadable page metadata: %w", toolOutline, err)
	}
	// Kivgraph deliberately omits completeness on a full, non-empty outline:
	// those rows claim declarations, not absence. Empty and partial outlines do
	// carry a verdict and are refused when it is missing.
	return queryMetadata(toolOutline, text, page.Total == 0 || page.Truncated)
}

type intentTerm struct {
	Term      string `json:"term"`
	Symbols   int    `json:"symbols"`
	Frequency string `json:"frequency"`
}

type intentSymbol struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Repository    string `json:"repository"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Terms         int    `json:"terms"`
	Match         string `json:"match"`
}

type intentAnswer struct {
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor"`
	Results    *struct {
		Terms     []intentTerm   `json:"terms"`
		Unmatched []string       `json:"unmatched_terms"`
		Symbols   []intentSymbol `json:"symbols"`
	} `json:"results"`
}

func (r *Runner) runIntentSearch(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	intent, ok := stringAt(req.Payload, "intent")
	if !ok || strings.TrimSpace(intent) == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.intent_search: intent is required")
	}
	if utf8.RuneCountInString(intent) > maximumIntentChars {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.intent_search: intent exceeds %d characters", maximumIntentChars)
	}
	keywords, ok := stringListAt(req.Payload, "keywords")
	if !ok && hasField(req.Payload, "keywords") {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.intent_search: keywords must be strings")
	}
	if len(keywords) > maximumIntentKeywords {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.intent_search: keywords exceeds %d entries", maximumIntentKeywords)
	}
	limit, hasLimit := intAt(req.Payload, "limit")
	if hasLimit && (limit < 1 || limit > maximumIntentLimit) {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.intent_search: limit must be between 1 and %d", maximumIntentLimit)
	}

	repository, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	args := map[string]any{"intent": intent, "repo": repository, "view": "full", "response_format": "detailed"}
	if len(keywords) > 0 {
		args["keywords"] = keywords
	}
	if prefix, present := stringAt(req.Payload, "path_prefix"); present {
		prefix = path.Clean(prefix)
		if _, err := within(root, prefix); err != nil {
			return nil, nil, err
		}
		args["path_prefix"] = prefix
	}
	for _, key := range []string{"kind", "cursor"} {
		if value, present := stringAt(req.Payload, key); present && value != "" {
			args[key] = value
		}
	}
	if hasLimit {
		args["limit"] = limit
	}

	text, err := sess.Call(ctx, toolIntent, args)
	if err != nil {
		return nil, nil, err
	}
	var answer intentAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil || answer.Results == nil {
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable or missing results", toolIntent)
	}
	notes, err := queryMetadata(toolIntent, text, false)
	if err != nil {
		return nil, nil, err
	}
	matches := make([]any, 0, len(answer.Results.Symbols))
	for rank, row := range answer.Results.Symbols {
		if row.Repository != repository {
			return nil, nil, fmt.Errorf("kivgraph %s returned repository %q while scoped to %q", toolIntent, row.Repository, repository)
		}
		if row.QualifiedName == "" || row.Kind == "" || row.FilePath == "" || row.StartLine <= 0 || row.EndLine < row.StartLine || row.Match == "" {
			return nil, nil, fmt.Errorf("kivgraph %s returned a malformed symbol at rank %d", toolIntent, rank+1)
		}
		name := row.Name
		if name == "" {
			name = simpleQualifiedName(row.QualifiedName)
		}
		matches = append(matches, map[string]any{
			"rank": rank + 1, "name": name, "qualified_name": row.QualifiedName,
			"kind": row.Kind, "path": row.FilePath, "line": row.StartLine,
			"end_line": row.EndLine, "match": row.Match, "terms": row.Terms,
		})
	}
	terms := make([]any, 0, len(answer.Results.Terms))
	for _, row := range answer.Results.Terms {
		terms = append(terms, map[string]any{"term": row.Term, "symbols": row.Symbols, "frequency": row.Frequency})
	}
	result := map[string]any{
		"matches": matches, "term_diagnostics": terms, "unmatched_terms": answer.Results.Unmatched,
		"truncated": answer.Truncated,
	}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}
	notes = append(notes, fmt.Sprintf("kivgraph ranked %d intent match(es) inside %s", len(matches), req.Repository.ID))
	return result, notes, nil
}

func simpleQualifiedName(qualified string) string {
	if at := strings.LastIndexAny(qualified, ".:#/"); at >= 0 && at+1 < len(qualified) {
		return qualified[at+1:]
	}
	return qualified
}

type dependencyNode struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Depth         int    `json:"depth"`
	Repository    string `json:"repository"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	ReachedFrom   string `json:"reached_from"`
	ViaKind       string `json:"via_kind"`
	ViaConfidence string `json:"via_confidence"`
	ViaProvenance string `json:"via_provenance"`
}

type dependencyAnswer struct {
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor"`
	Results    *struct {
		RootRepository     string           `json:"root_repository"`
		Reached            int              `json:"reached"`
		DeepestDepth       int              `json:"deepest_depth"`
		TraversalTruncated bool             `json:"traversal_truncated"`
		Nodes              []dependencyNode `json:"nodes"`
		WitnessTo          string           `json:"witness_to"`
		WitnessHops        int              `json:"witness_hops"`
	} `json:"results"`
}

func (r *Runner) runDependencies(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	file, ok := stringAt(req.Payload, "file")
	if !ok || file == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.dependencies: file is required")
	}
	line, ok := intAt(req.Payload, "line")
	if !ok || line < 1 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.dependencies: line must be positive")
	}
	name, _ := stringAt(req.Payload, "name")
	repository, _, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilityDependencies, repository, file, line, name)
	if err != nil {
		return nil, nil, err
	}
	qualified := decl.QualifiedName
	if qualified == "" {
		qualified = decl.Name
	}
	args := map[string]any{
		"qualified_name": qualified, "repository": repository, "path": file,
		"view": "full", "response_format": "detailed",
	}
	for _, bound := range []struct {
		name     string
		min, max int
	}{
		{"depth", 0, maximumDependencyDepth}, {"max_nodes", 1, maximumDependencyNodes}, {"limit", 1, maximumDependencyLimit},
	} {
		if value, present := intAt(req.Payload, bound.name); present {
			if value < bound.min || value > bound.max {
				return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.dependencies: %s must be between %d and %d", bound.name, bound.min, bound.max)
			}
			args[bound.name] = value
		}
	}
	for _, key := range []string{"to", "to_path", "confidence", "cursor"} {
		if value, present := stringAt(req.Payload, key); present && value != "" {
			args[key] = value
		}
	}
	if values, present := stringListAt(req.Payload, "edge_kinds"); present {
		args["edge_kinds"] = values
	} else if hasField(req.Payload, "edge_kinds") {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.dependencies: edge_kinds must be strings")
	}
	if hasField(req.Payload, "include_derived") {
		args["include_derived"] = boolAt(req.Payload, "include_derived")
	}
	if _, witness := args["to"]; witness {
		for _, forbidden := range []string{"limit", "cursor", "include_derived"} {
			if _, exists := args[forbidden]; exists {
				return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.dependencies: %s cannot be combined with to", forbidden)
			}
		}
	} else if _, pathOnly := args["to_path"]; pathOnly {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.dependencies: to_path requires to")
	}

	text, err := sess.Call(ctx, toolDependencies, args)
	if err != nil {
		return nil, nil, err
	}
	var answer dependencyAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil || answer.Results == nil {
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable or missing results", toolDependencies)
	}
	metaNotes, err := queryMetadata(toolDependencies, text, true)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, metaNotes...)
	nodes := make([]any, 0, len(answer.Results.Nodes))
	for index, row := range answer.Results.Nodes {
		if row.Name == "" || row.QualifiedName == "" || row.Kind == "" || row.Repository == "" || row.FilePath == "" || row.StartLine <= 0 || row.EndLine < row.StartLine || row.Depth < 0 {
			return nil, nil, fmt.Errorf("kivgraph %s returned a malformed node at index %d", toolDependencies, index)
		}
		nodes = append(nodes, map[string]any{
			"repository": repositoryNameFromKey(row.Repository), "name": row.Name,
			"qualified_name": row.QualifiedName, "kind": row.Kind, "path": row.FilePath,
			"line": row.StartLine, "end_line": row.EndLine, "depth": row.Depth,
			"reached_from": row.ReachedFrom, "via_kind": row.ViaKind,
			"via_confidence": row.ViaConfidence, "via_provenance": row.ViaProvenance,
		})
	}
	result := map[string]any{
		"dependencies": nodes, "reached": answer.Results.Reached,
		"deepest_depth": answer.Results.DeepestDepth,
		"truncated":     answer.Truncated || answer.Results.TraversalTruncated,
	}
	if answer.NextCursor != "" {
		result["next_cursor"] = answer.NextCursor
	}
	if answer.Results.WitnessTo != "" {
		result["witness_to"] = answer.Results.WitnessTo
		result["witness_hops"] = answer.Results.WitnessHops
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}
	notes = append(notes, fmt.Sprintf("kivgraph traced %d dependency node(s) from %s", len(nodes), qualified))
	return result, notes, nil
}

func stringListAt(payload map[string]any, key string) ([]string, bool) {
	if !hasField(payload, key) {
		return nil, false
	}
	switch values := payload[key].(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}
