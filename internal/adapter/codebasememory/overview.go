package codebasememory

// symbol.overview: what a file declares, read out of the graph the provider
// already built for the repository instead of parsing the file again.
//
// The graph is the whole answer except for one field. It knows every
// declaration and the line each one starts on, but it stores no column, so
// the file itself is opened once to recover them -- one pass for the whole
// file, not one per symbol.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// declares lists the node labels that count as something a file declares.
//
// It is an allowlist rather than a denylist because the graph also holds
// nodes that are about the file rather than in it -- the File node itself,
// its folder, its package, a route it registers -- and a label added
// upstream tomorrow must not silently start appearing as a symbol.
//
// Section is in the list: for a markdown file its headings are exactly what
// it declares, and no language server answers that question at all.
var declares = []string{
	"Function", "Method", "Struct", "Class", "Interface", "Variable", "Section",
}

// overviewAsk is symbol.overview's payload, read once.
type overviewAsk struct {
	file  string
	depth int
}

func readOverviewAsk(payload map[string]any) (overviewAsk, error) {
	var out overviewAsk

	out.file = strings.TrimSpace(stringAt(payload, "file"))
	if out.file == "" {
		return overviewAsk{}, contract.Fail(contract.FailureInvalidInput, "symbol.overview: file is required")
	}
	if depth, ok := intAt(payload, "depth"); ok {
		if depth < 0 {
			return overviewAsk{}, contract.Fail(contract.FailureInvalidInput,
				"symbol.overview: depth must not be negative, got %d", depth)
		}
		out.depth = depth
	}
	if out.depth != 0 {
		// The catalog keeps this call away from here with a max_input
		// bound, so reaching this line means the settings file and the code
		// disagree. Refusing is still cheaper than answering half the
		// question: the graph holds a file's top-level declarations and
		// nothing inside them -- a struct's fields are not nodes -- so a
		// deeper walk would return the same list and quietly call it
		// complete.
		return overviewAsk{}, contract.Fail(contract.FailureInvalidInput,
			"symbol.overview: codebase-memory answers depth 0 only, got %d: "+
				"the graph holds what a file declares at its own top level, not what is nested inside those declarations", out.depth)
	}
	return out, nil
}

// overviewRow is one declaration as the graph reports it.
type overviewRow struct {
	name, kind string
	start, end int
}

func (r *Runner) runSymbolOverview(ctx context.Context, req contract.RunRequest, root string) (contract.Outcome, error) {
	started := time.Now()
	ask, err := readOverviewAsk(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}
	if r.isSensitive(ask.file) {
		// The graph would answer this one without touching the disk, and
		// that is the reason to refuse rather than to filter: a list of
		// every name a secret-carrying file declares is the shape of the
		// secret. The other readers in this adapter refuse out loud for the
		// same reason.
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets and is not read", ask.file)
	}
	if _, err := within(root, ask.file); err != nil {
		return contract.Outcome{}, err
	}
	weight := &meter{}

	rows, known, err := r.declarationsIn(ctx, root, ask.file, weight)
	if err != nil {
		return contract.Outcome{}, err
	}
	if !known {
		// "Declares nothing" and "was never indexed" are different answers
		// and only one of them is true here. Saying the first when the
		// second holds is the failure this capability would be least able
		// to recover from: an empty list reads as a finished answer.
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"%s is not in this repository's graph: atenea ask repository.index --repo %s builds one",
			ask.file, req.Repository.ID)
	}

	// "In the order the provider found them" is a row order the graph does
	// not promise, so it is imposed here: where the symbol sits in the file,
	// which is the order a reader opening it would meet them in.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].start != rows[j].start {
			return rows[i].start < rows[j].start
		}
		return rows[i].name < rows[j].name
	})

	columns, err := r.columnsFor(root, ask.file, rows)
	if err != nil {
		return contract.Outcome{}, err
	}

	symbols := make([]any, len(rows))
	for i, row := range rows {
		m := map[string]any{
			"name":   row.name,
			"kind":   row.kind,
			"line":   row.start,
			"column": columns[i],
		}
		if row.end > row.start {
			m["end_line"] = row.end
		}
		symbols[i] = m
	}

	result := map[string]any{"symbols": symbols}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}
	return contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		Spent:   contract.Sample{Duration: time.Since(started), PeakRSS: weight.peak},
	}, nil
}

// declarationsIn returns what the graph says the file declares, and whether
// the graph has heard of the file at all. The File node is fetched alongside
// the declarations rather than in a second round trip: it is the only
// evidence that separates an empty file from an unindexed one.
func (r *Runner) declarationsIn(ctx context.Context, root, file string, weight *meter) ([]overviewRow, bool, error) {
	query := fmt.Sprintf(
		"MATCH (n) WHERE n.file_path = %s "+
			"RETURN n.name AS name, n.label AS label, n.start_line AS start_line, n.end_line AS end_line",
		cypherString(file))
	raw, err := r.invoke(ctx, "query_graph", map[string]any{"project": root, "query": query}, weight)
	if err != nil {
		return nil, false, err
	}
	var resp queryGraphResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false, contract.Fail(contract.FailureUnavailable,
			"codebase-memory: could not read query_graph's answer: %v", err).WithRaw(string(raw))
	}
	var out []overviewRow
	for _, row := range resp.Rows {
		if len(row) < 4 {
			continue
		}
		name, _ := row[0].(string)
		label, _ := row[1].(string)
		if name == "" || !slices.Contains(declares, label) {
			continue
		}
		start := lineNumber(row[2])
		if start <= 0 {
			continue
		}
		out = append(out, overviewRow{name: name, kind: label, start: start, end: lineNumber(row[3])})
	}
	return out, len(resp.Rows) > 0, nil
}

// columnsFor recovers each symbol's column by finding its name on the line
// the graph reported, in one pass over the file.
//
// A name that is not there is not skipped. The graph and the working tree
// disagreeing about where a declaration sits means the index is behind the
// file, and every line number in this answer is then suspect -- reporting
// the rest as if they were fine would send the caller to the wrong places
// one at a time instead of once, out loud.
func (r *Runner) columnsFor(root, file string, rows []overviewRow) ([]int, error) {
	want := make([]int, 0, len(rows))
	for _, row := range rows {
		want = append(want, row.start)
	}
	text, err := r.linesAt(root, file, want)
	if err != nil {
		return nil, err
	}
	out := make([]int, len(rows))
	for i, row := range rows {
		line, ok := text[row.start]
		if !ok {
			return nil, contract.Fail(contract.FailureUnavailable,
				"%s has fewer than %d line(s) but the graph puts %s there: "+
					"the index is behind the file, and atenea ask repository.index rebuilds it",
				file, row.start, row.name)
		}
		column, ok := columnOf(line, row.name)
		if !ok {
			return nil, contract.Fail(contract.FailureUnavailable,
				"%s:%d does not declare %s as a whole word: "+
					"the index is behind the file, and atenea ask repository.index rebuilds it",
				file, row.start, row.name)
		}
		out[i] = column
	}
	return out, nil
}

// columnOf finds the 1-based column where name sits as a whole word in text.
// It is wordAt's inverse: a known name on a known line, rather than a known
// column on one.
func columnOf(text, name string) (int, bool) {
	runes := []rune(text)
	target := []rune(name)
	if len(target) == 0 {
		return 0, false
	}
	for i := 0; i+len(target) <= len(runes); i++ {
		if i > 0 && isWord(runes[i-1]) {
			continue
		}
		if !slices.Equal(runes[i:i+len(target)], target) {
			continue
		}
		if i+len(target) < len(runes) && isWord(runes[i+len(target)]) {
			continue
		}
		return i + 1, true
	}
	return 0, false
}
