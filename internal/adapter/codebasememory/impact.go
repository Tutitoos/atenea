package codebasememory

// code.impact: a baseline turned into a git diff, the diff's hunks turned
// into the symbols they actually touched, and each of those walked through
// the same call graph symbol.calls walks -- once per directly changed
// symbol instead of once by hand for each.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// impactAsk is code.impact's payload, read once.
type impactAsk struct {
	baseline string
	scope    []string
	depth    int
	snippet  bool
	lines    int
}

func readImpactAsk(payload map[string]any) (impactAsk, error) {
	out := impactAsk{depth: defaultDepth, lines: defaultSnippetLines}

	out.baseline = strings.TrimSpace(stringAt(payload, "baseline"))
	if out.baseline == "" {
		return impactAsk{}, contract.Fail(contract.FailureInvalidInput, "code.impact: baseline is required")
	}
	if strings.HasPrefix(out.baseline, "-") {
		// git would read this as an option rather than a revision, and no
		// legitimate ref, tag or commit starts with a dash -- git itself
		// refuses to create one for the same reason.
		return impactAsk{}, contract.Fail(contract.FailureInvalidInput,
			"code.impact: baseline %q looks like an option, not a revision", out.baseline)
	}

	out.scope = stringsAt(payload, "scope")

	if depth, ok := intAt(payload, "depth"); ok {
		if depth < 0 {
			return impactAsk{}, contract.Fail(contract.FailureInvalidInput,
				"code.impact: depth must not be negative, got %d", depth)
		}
		out.depth = depth
	}
	out.snippet = boolAt(payload, "include_snippet")
	if n, ok := intAt(payload, "snippet_lines"); ok {
		if n < 1 {
			return impactAsk{}, contract.Fail(contract.FailureInvalidInput,
				"code.impact: snippet_lines must be above 0, got %d", n)
		}
		out.lines = n
	}
	return out, nil
}

// hunk is one span of the CURRENT tree's lines a diff says differs from the
// baseline, for one file. Current-tree lines, not the baseline's own, because
// the graph is built from what is on disk now: comparing against the
// baseline's own numbering would intersect two files that no longer agree on
// what line anything is at.
type hunk struct {
	file       string
	start, end int // inclusive
}

// hunkHeader matches a unified diff's own "@@ -old +new @@" line. Only the
// new side is kept; see hunk's own doc comment for why.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// diff runs git and returns every changed file together with the current-
// tree line ranges the baseline disagrees with, both in the order git
// itself printed them.
//
// --unified=0 asks for zero context lines, so every hunk is exactly the
// lines that differ and nothing more -- which is what this needs, because
// nothing here is for a human to read: the ranges exist only to be
// intersected with the graph's own start_line/end_line.
func (r *Runner) diff(ctx context.Context, root, baseline string, scope []string, weight *meter) ([]string, []hunk, error) {
	args := []string{"diff", "--unified=0", "--no-color", baseline}
	if len(scope) > 0 {
		args = append(args, "--")
		args = append(args, scope...)
	}
	stdout, err := r.git(ctx, root, args, weight)
	if err != nil {
		return nil, nil, err
	}
	files, hunks := parseHunks(stdout)
	return files, hunks, nil
}

// parseHunks turns raw `git diff --unified=0` output into the changed file
// list and the current-tree line ranges the baseline disagrees with. Split
// out of diff on its own so the parsing rules can be pinned down without a
// git process behind them.
func parseHunks(gitOutput string) ([]string, []hunk) {
	var files []string
	var hunks []hunk
	seen := make(map[string]struct{})
	remember := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			files = append(files, name)
		}
	}
	current := ""
	// pendingOld carries the name from a "--- " line forward to the "+++ "
	// line that always immediately follows it in real diff output. It is
	// only ever used when that "+++ " side turns out to be /dev/null: a
	// deleted file's old name is the only name it ever had, and otherwise
	// the file simply would not appear in changed_files at all.
	pendingOld := ""
	for _, line := range strings.Split(gitOutput, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			pendingOld = strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/")
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(line, "+++ ")
			if p == "/dev/null" {
				// The file was deleted: nothing in the current tree to
				// attach a line range to, so hunks under this header are
				// skipped below by current staying empty. It is still a
				// file the baseline no longer matches, so it is remembered
				// under the only name it had -- the "--- " side.
				remember(pendingOld)
				current = ""
				continue
			}
			current = strings.TrimPrefix(p, "b/")
			remember(current)
		case current != "" && strings.HasPrefix(line, "@@ "):
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				// A pure deletion adds nothing to the current file; the
				// closest still-current line is where the deleted text used
				// to sit, so that single line is the range -- there is
				// nothing narrower to report.
				hunks = append(hunks, hunk{file: current, start: start, end: start})
				continue
			}
			hunks = append(hunks, hunk{file: current, start: start, end: start + count - 1})
		}
	}
	sort.Strings(files)
	return files, hunks
}

// symbolRange is one symbol's own span in the graph, as recorded against the
// current tree.
type symbolRange struct {
	qualifiedName, name, kind string
	start, end                int
}

// symbolsIn fetches every symbol's line range in the given files, one round
// trip for the whole set rather than one per file or one per hunk.
func (r *Runner) symbolsIn(ctx context.Context, root string, files []string, weight *meter) (map[string][]symbolRange, error) {
	if len(files) == 0 {
		return map[string][]symbolRange{}, nil
	}
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = cypherString(f)
	}
	query := fmt.Sprintf(
		"MATCH (n) WHERE n.file_path IN [%s] AND n.start_line IS NOT NULL AND n.end_line IS NOT NULL "+
			"RETURN n.file_path AS file_path, n.qualified_name AS qn, n.name AS name, n.label AS label, "+
			"n.start_line AS start_line, n.end_line AS end_line",
		strings.Join(quoted, ", "))
	raw, err := r.invoke(ctx, "query_graph", map[string]any{"project": root, "query": query}, weight)
	if err != nil {
		return nil, err
	}
	var resp queryGraphResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"codebase-memory: could not read query_graph's answer: %v", err).WithRaw(string(raw))
	}
	out := make(map[string][]symbolRange)
	for _, row := range resp.Rows {
		if len(row) < 6 {
			continue
		}
		file, _ := row[0].(string)
		qn, _ := row[1].(string)
		name, _ := row[2].(string)
		label, _ := row[3].(string)
		start := lineNumber(row[4])
		end := lineNumber(row[5])
		if file == "" || qn == "" || start <= 0 || end < start {
			continue
		}
		out[file] = append(out[file], symbolRange{qualifiedName: qn, name: name, kind: label, start: start, end: end})
	}
	return out, nil
}

// directlyChanged returns every symbol whose own range overlaps at least one
// hunk -- the depth-0 entries -- smallest enclosing symbol first when more
// than one contains the same hunk, so a change inside a method is that
// method's change and not also its file's.
func directlyChanged(hunks []hunk, symbols map[string][]symbolRange) []symbolRange {
	var out []symbolRange
	seen := make(map[string]struct{})
	for _, h := range hunks {
		var best *symbolRange
		for i := range symbols[h.file] {
			s := &symbols[h.file][i]
			if s.end < h.start || s.start > h.end {
				continue
			}
			if best == nil || (s.end-s.start) < (best.end-best.start) {
				best = s
			}
		}
		if best == nil {
			continue
		}
		if _, ok := seen[best.qualifiedName]; ok {
			continue
		}
		seen[best.qualifiedName] = struct{}{}
		out = append(out, *best)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].qualifiedName < out[j].qualifiedName })
	if len(out) > maxImpactSeeds {
		out = out[:maxImpactSeeds]
	}
	return out
}

// impactHit is one symbol code.impact reports, at the shortest distance any
// seed reached it from.
type impactHit struct {
	name, kind, filePath string
	line, depth          int
	hasLocation          bool
}

// walkImpact asks trace_path once per directly changed symbol who calls into
// it -- this is symbol.calls run for every seed instead of once by hand for
// each -- merged so a symbol two seeds both reach keeps its shortest
// distance.
func (r *Runner) walkImpact(ctx context.Context, root string, seeds []symbolRange, depth int, weight *meter) (map[string]impactHit, error) {
	out := make(map[string]impactHit, len(seeds))
	for _, s := range seeds {
		out[s.qualifiedName] = impactHit{name: s.name, kind: s.kind, filePath: "", line: s.start, depth: 0, hasLocation: true}
	}
	if depth <= 0 {
		return out, nil
	}
	for _, seed := range seeds {
		raw, err := r.invoke(ctx, "trace_path", map[string]any{
			"project":       root,
			"function_name": seed.name,
			"mode":          "calls",
			"direction":     "inbound",
			"depth":         depth,
		}, weight)
		if err != nil {
			if contract.KindOf(err) == contract.FailureNotFound {
				// trace_path resolves by name, and not everything the graph
				// calls a symbol is something it can trace calls for -- a
				// package-level Variable, say. Nothing to walk from it is
				// not a fault in the rest of this walk.
				continue
			}
			return nil, err
		}
		var resp traceCallsResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, contract.Fail(contract.FailureUnavailable,
				"codebase-memory: could not read trace_path's answer: %v", err).WithRaw(string(raw))
		}
		for _, h := range resp.Callers {
			if h.QualifiedName == "" {
				continue
			}
			existing, ok := out[h.QualifiedName]
			if ok && existing.depth <= h.Hop {
				continue
			}
			out[h.QualifiedName] = impactHit{name: h.Name, depth: h.Hop}
		}
	}
	return out, nil
}

func (r *Runner) runCodeImpact(ctx context.Context, req contract.RunRequest, root string) (contract.Outcome, error) {
	started := time.Now()
	ask, err := readImpactAsk(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}
	weight := &meter{}

	files, hunks, err := r.diff(ctx, root, ask.baseline, ask.scope, weight)
	if err != nil {
		return contract.Outcome{}, err
	}
	symbols, err := r.symbolsIn(ctx, root, files, weight)
	if err != nil {
		return contract.Outcome{}, err
	}
	seeds := directlyChanged(hunks, symbols)

	hits, err := r.walkImpact(ctx, root, seeds, ask.depth, weight)
	if err != nil {
		return contract.Outcome{}, err
	}

	// The seeds already carry their own file and line from symbolsIn; every
	// caller trace_path found only carries a name and a qualified name, so
	// their locations are resolved in the one remaining round trip.
	var unresolvedQNs []string
	for qn, hit := range hits {
		if !hit.hasLocation {
			unresolvedQNs = append(unresolvedQNs, qn)
		}
	}
	locations, err := r.locate(ctx, root, unresolvedQNs, weight)
	if err != nil {
		return contract.Outcome{}, err
	}

	type record struct {
		path, name, kind, snippet string
		line, depth               int
		hasKind, hasSnippet       bool
	}
	var recs []record
	seedFiles := seedFileIndex(seeds, symbols)
	for qn, hit := range hits {
		file, line, kind, hasKind := hit.filePath, hit.line, hit.kind, hit.kind != ""
		if !hit.hasLocation {
			loc, ok := locations[qn]
			if !ok {
				// No definition site anywhere in this repository's own
				// graph -- nothing to put in path/line, both required.
				continue
			}
			file, line = loc.FilePath, loc.StartLine
		} else {
			file = seedFiles[qn]
		}
		if !inScope(file, ask.scope) {
			continue
		}
		rec := record{path: file, line: line, name: hit.name, depth: hit.depth, kind: kind, hasKind: hasKind}
		if ask.snippet {
			if snippet, err := r.snippetFor(root, file, line, ask.lines); err == nil {
				rec.snippet, rec.hasSnippet = snippet, true
			}
		}
		recs = append(recs, rec)
	}

	// "Every symbol the change reaches, nearest first" -- hits started life
	// in a map, which has no order of its own.
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].depth != recs[j].depth {
			return recs[i].depth < recs[j].depth
		}
		if recs[i].path != recs[j].path {
			return recs[i].path < recs[j].path
		}
		return recs[i].name < recs[j].name
	})

	affected := make([]any, len(recs))
	for i, rec := range recs {
		m := map[string]any{"path": rec.path, "line": rec.line, "name": rec.name, "depth": rec.depth}
		if rec.hasKind {
			m["kind"] = rec.kind
		}
		if rec.hasSnippet {
			m["snippet"] = rec.snippet
		}
		affected[i] = m
	}
	if files == nil {
		files = []string{}
	}

	result := map[string]any{"changed_files": files, "affected_symbols": affected}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}
	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		Spent:   contract.Sample{Duration: time.Since(started), PeakRSS: weight.peak},
	}
	if len(hunks) > 0 && len(seeds) == 0 {
		// The diff touched files but not one hunk landed inside a symbol
		// the graph knows -- generated code, a file outside every language
		// this provider parses, whitespace-only changes. An empty walk that
		// does not say why looks identical to "nothing reaches anything".
		outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
			Level: contract.ContextRepository,
			Note: fmt.Sprintf(
				"code.impact against %s: %d file(s) changed but no hunk fell inside a symbol codebase-memory's graph tracks",
				ask.baseline, len(files)),
		})
	} else if len(symbols) > 0 && len(directlyChangedAll(hunks, symbols)) > maxImpactSeeds {
		outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
			Level: contract.ContextRepository,
			Note: fmt.Sprintf(
				"code.impact against %s: more than %d symbols were directly changed, only the first %d were walked for callers",
				ask.baseline, maxImpactSeeds, maxImpactSeeds),
		})
	}
	return outcome, nil
}

// seedFileIndex maps a depth-0 symbol's qualified name back to its file. It
// exists because impactHit does not carry a seed's file directly -- only its
// line, kept for the required output field -- and looking it back up in
// symbols is cheaper than adding a field only seeds would ever fill in.
func seedFileIndex(seeds []symbolRange, symbols map[string][]symbolRange) map[string]string {
	out := make(map[string]string, len(seeds))
	for file, ranges := range symbols {
		for _, s := range ranges {
			out[s.qualifiedName] = file
		}
	}
	return out
}

// directlyChangedAll is directlyChanged without the maxImpactSeeds cap, used
// only to decide whether the cap actually cut anything worth mentioning.
func directlyChangedAll(hunks []hunk, symbols map[string][]symbolRange) []symbolRange {
	var out []symbolRange
	seen := make(map[string]struct{})
	for _, h := range hunks {
		var best *symbolRange
		for i := range symbols[h.file] {
			s := &symbols[h.file][i]
			if s.end < h.start || s.start > h.end {
				continue
			}
			if best == nil || (s.end-s.start) < (best.end-best.start) {
				best = s
			}
		}
		if best == nil {
			continue
		}
		if _, ok := seen[best.qualifiedName]; ok {
			continue
		}
		seen[best.qualifiedName] = struct{}{}
		out = append(out, *best)
	}
	return out
}
