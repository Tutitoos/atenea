package codebasememory

// symbol.calls: from a position, the identifier there, and from that
// identifier, codebase-memory's own call graph walked in whichever direction
// the caller asked for.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// callsAsk is symbol.calls' payload, read once.
type callsAsk struct {
	file      string
	line      int
	column    int
	direction string // "incoming", "outgoing" or "both"
	scope     []string
	depth     int
	snippet   bool
	lines     int
}

func readCallsAsk(payload map[string]any) (callsAsk, error) {
	out := callsAsk{depth: defaultDepth, lines: defaultSnippetLines}

	out.file = strings.TrimSpace(stringAt(payload, "file"))
	if out.file == "" {
		return callsAsk{}, contract.Fail(contract.FailureInvalidInput, "symbol.calls: file is required")
	}
	line, ok := intAt(payload, "line")
	if !ok || line < 1 {
		return callsAsk{}, contract.Fail(contract.FailureInvalidInput, "symbol.calls: line must be a positive integer")
	}
	out.line = line
	column, ok := intAt(payload, "column")
	if !ok || column < 1 {
		return callsAsk{}, contract.Fail(contract.FailureInvalidInput, "symbol.calls: column must be a positive integer")
	}
	out.column = column

	out.direction = strings.ToLower(strings.TrimSpace(stringAt(payload, "direction")))
	switch out.direction {
	case "incoming", "outgoing", "both":
	default:
		return callsAsk{}, contract.Fail(contract.FailureInvalidInput,
			"symbol.calls: direction must be \"incoming\", \"outgoing\" or \"both\", got %q", out.direction)
	}

	out.scope = stringsAt(payload, "scope")

	if depth, ok := intAt(payload, "depth"); ok {
		if depth < 1 {
			return callsAsk{}, contract.Fail(contract.FailureInvalidInput,
				"symbol.calls: depth must be above 0, got %d", depth)
		}
		out.depth = depth
	}
	out.snippet = boolAt(payload, "include_snippet")
	if n, ok := intAt(payload, "snippet_lines"); ok {
		if n < 1 {
			return callsAsk{}, contract.Fail(contract.FailureInvalidInput,
				"symbol.calls: snippet_lines must be above 0, got %d", n)
		}
		out.lines = n
	}
	return out, nil
}

// traceHop is one entry of a trace_path answer, in codebase-memory-mcp's own
// words.
type traceHop struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Hop           int    `json:"hop"`
}

// traceCallsResponse is trace_path's answer in mode "calls".
type traceCallsResponse struct {
	Callers []traceHop `json:"callers"`
	Callees []traceHop `json:"callees"`
}

// callHit is one hop, tagged with which direction it was found walking --
// information trace_path's own per-direction arrays carry positionally and
// this adapter has to keep explicit once both arrays are merged.
type callHit struct {
	hop       traceHop
	direction string
}

func (r *Runner) runSymbolCalls(ctx context.Context, req contract.RunRequest, root string) (contract.Outcome, error) {
	started := time.Now()
	ask, err := readCallsAsk(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}
	weight := &meter{}

	name, err := r.identifierAt(root, ask.file, ask.line, ask.column)
	if err != nil {
		return contract.Outcome{}, err
	}

	traceDirection := map[string]string{"incoming": "inbound", "outgoing": "outbound", "both": "both"}[ask.direction]
	raw, err := r.invoke(ctx, "trace_path", map[string]any{
		"project":       root,
		"function_name": name,
		"mode":          "calls",
		"direction":     traceDirection,
		"depth":         ask.depth,
	}, weight)
	if err != nil {
		return contract.Outcome{}, err
	}
	var resp traceCallsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"codebase-memory: could not read trace_path's answer: %v", err).WithRaw(string(raw))
	}

	var hits []callHit
	if ask.direction == "incoming" || ask.direction == "both" {
		for _, h := range resp.Callers {
			hits = append(hits, callHit{h, "incoming"})
		}
	}
	if ask.direction == "outgoing" || ask.direction == "both" {
		for _, h := range resp.Callees {
			hits = append(hits, callHit{h, "outgoing"})
		}
	}

	// One round trip resolves every hop's file and line, rather than one per
	// hop: a call graph walked four deep can easily name a few dozen symbols.
	qns := make([]string, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		if h.hop.QualifiedName == "" {
			continue
		}
		if _, ok := seen[h.hop.QualifiedName]; ok {
			continue
		}
		seen[h.hop.QualifiedName] = struct{}{}
		qns = append(qns, h.hop.QualifiedName)
	}
	locations, err := r.locate(ctx, root, qns, weight)
	if err != nil {
		return contract.Outcome{}, err
	}

	type record struct {
		path, name, direction, snippet string
		line, depth                    int
		hasSnippet                     bool
	}
	var recs []record
	for _, h := range hits {
		loc, ok := locations[h.hop.QualifiedName]
		if !ok {
			// codebase-memory found a call to this symbol but has no
			// definition site for it in this repository's own graph -- a
			// call into a dependency, say. path and line are both required
			// fields, so the hop is dropped rather than reported half
			// answered.
			continue
		}
		if !inScope(loc.FilePath, ask.scope) {
			continue
		}
		rec := record{path: loc.FilePath, line: loc.StartLine, name: h.hop.Name, direction: h.direction, depth: h.hop.Hop}
		if ask.snippet {
			if snippet, err := r.snippetFor(root, loc.FilePath, loc.StartLine, ask.lines); err == nil {
				rec.snippet, rec.hasSnippet = snippet, true
			}
		}
		recs = append(recs, rec)
	}

	// "Every call found, nearest hop first" -- trace_path answers each
	// direction in its own hop order, but concatenating incoming and
	// outgoing does not leave the merged list sorted, so it is sorted here.
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].depth != recs[j].depth {
			return recs[i].depth < recs[j].depth
		}
		if recs[i].path != recs[j].path {
			return recs[i].path < recs[j].path
		}
		return recs[i].name < recs[j].name
	})

	calls := make([]any, len(recs))
	for i, rec := range recs {
		m := map[string]any{
			"path":      rec.path,
			"line":      rec.line,
			"name":      rec.name,
			"direction": rec.direction,
			"depth":     rec.depth,
		}
		if rec.hasSnippet {
			m["snippet"] = rec.snippet
		}
		calls[i] = m
	}

	result := map[string]any{"calls": calls}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}
	return contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		Spent:   contract.Sample{Duration: time.Since(started), PeakRSS: weight.peak},
	}, nil
}
