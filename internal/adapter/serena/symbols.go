package serena

// Turning Atenea's coordinates into Serena's names, and Serena's answers back
// into Atenea's shape.
//
// Two coordinate systems meet here and they disagree about everything. Atenea
// counts lines and columns from 1, because that is what an editor shows and
// what the capability declares. Serena counts from 0. Atenea names a symbol by
// where it sits; Serena names it by what it is called. Every conversion lives
// in this file so the disagreement has exactly one place to be wrong.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// maxLineBytes caps one line read out of a source file. A minified bundle or a
// checked-in blob is not something to pull into memory whole just to find the
// word under a cursor.
const maxLineBytes = 1 << 20

// symbol is one entry of a find_symbol or find_implementations answer, in
// Serena's words -- both come back as the same flat array.
type symbol struct {
	NamePath string `json:"name_path"`
	Kind     string `json:"kind"`
	Path     string `json:"relative_path"`
	Body     string `json:"body"`
	Location struct {
		// Both are 0-based. See toContractLine: they are converted once, on
		// the way out, and nowhere else.
		StartLine int `json:"start_line"`
		EndLine   int `json:"end_line"`
	} `json:"body_location"`
}

// toContractLine converts one of Serena's 0-based lines into the 1-based line
// the capability declares. It is a function and not an inline `+1` so that the
// off-by-one has a name, a comment and one place to be tested.
func toContractLine(serenaLine int) int { return serenaLine + 1 }

// covers reports whether the symbol's body contains a 1-based contract line.
func (s symbol) covers(line int) bool {
	return toContractLine(s.Location.StartLine) <= line &&
		line <= toContractLine(s.Location.EndLine)
}

// parseSymbols reads a find_symbol or find_implementations answer: a JSON
// array of symbols.
func parseSymbols(text string) ([]symbol, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	var out []symbol
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("serena sent a symbol list nobody can read: %s", clip(trimmed))
	}
	return out, nil
}

// searchRank gives callers a stable ordering on top of Serena's structural
// matches. Exact qualified names win, then exact leaf names, then names that
// contain the requested text; provider order never affects the result.
func searchRank(query string, symbol symbol) int {
	query = strings.TrimSpace(query)
	if symbol.NamePath == query {
		return 0
	}
	if lastSegment(symbol.NamePath) == query {
		return 1
	}
	if strings.Contains(strings.ToLower(symbol.NamePath), strings.ToLower(query)) {
		return 2
	}
	return 3
}

// parseSymbol reads a find_declaration answer: one symbol object, not an
// array. The object has the same shape as one entry of parseSymbols' array --
// both come off the same symbol_dict on Serena's side -- so a query for
// "what is this again" always ends up shaped the same way no matter which
// tool answered it.
func parseSymbol(text string) (symbol, error) {
	trimmed := strings.TrimSpace(text)
	var out symbol
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return symbol{}, fmt.Errorf("serena sent a definition nobody can read: %s", clip(trimmed))
	}
	return out, nil
}

// overviewName is one name Serena's get_symbols_overview reported, before any
// location is known -- that tool never returns one, by design (its own
// docstring calls it the first move into an unfamiliar file, not a lookup).
// Serena groups names by kind and nests a symbol's own children -- a
// struct's fields, say -- under it instead of beside it, keyed only by name.
// parseOverviewNames flattens both facts into what every name needs to go
// find itself: what to hand find_symbol, and which name (if any) it sits
// inside of.
//
// Kind is deliberately not one of these fields. find_symbol reports its own
// kind for whatever name it actually locates, from the same vocabulary
// (verified live: both call it "Struct" for the same symbol) -- trusting
// that answer over an earlier, separate one about the same name is the more
// honest source once both exist, so locateOne reads kind from there instead.
type overviewName struct {
	name      string
	parent    string
	queryPath string
}

// parseOverviewNames reads a get_symbols_overview answer: one object keyed
// by kind, each value a mix of bare names -- nothing to descend into -- and
// single-key objects, where the key is the name and the value is that name's
// own children, grouped by kind exactly like the top level. How deep that
// nesting goes is the depth argument's doing, decided before this is called;
// parsing only has to unwrap however deep the answer actually came back.
func parseOverviewNames(text string) ([]overviewName, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == "{}" {
		return nil, nil
	}
	var grouped map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &grouped); err != nil {
		return nil, fmt.Errorf("serena sent a symbol overview nobody can read: %s", clip(trimmed))
	}
	// A Go map has no order, and two identical commissions returning the
	// same names shuffled would make every diff of two runs noise -- same
	// reasoning as parseReferences' own sort, below.
	kinds := make([]string, 0, len(grouped))
	for k := range grouped {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	var out []overviewName
	for _, k := range kinds {
		names, err := walkOverviewGroup(grouped[k], "", "")
		if err != nil {
			return nil, fmt.Errorf("serena sent a symbol overview nobody can read: %s", clip(trimmed))
		}
		out = append(out, names...)
	}
	return out, nil
}

// walkOverviewGroup reads one kind's array and recurses into whichever
// entries carry children. Order within one kind's array is a JSON array's,
// preserved by encoding/json -- unlike the kind keys above, this one is
// worth keeping: it is declaration order inside that kind.
func walkOverviewGroup(raw json.RawMessage, parent, parentPath string) ([]overviewName, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	var out []overviewName
	for _, item := range items {
		var bare string
		if err := json.Unmarshal(item, &bare); err == nil {
			out = append(out, overviewName{name: bare, parent: parent, queryPath: qualify(parentPath, bare)})
			continue
		}
		var withChildren map[string]json.RawMessage
		if err := json.Unmarshal(item, &withChildren); err != nil || len(withChildren) != 1 {
			return nil, fmt.Errorf("unreadable overview entry: %s", clip(string(item)))
		}
		for name, children := range withChildren {
			childPath := qualify(parentPath, name)
			out = append(out, overviewName{name: name, parent: parent, queryPath: childPath})
			var childGroups map[string]json.RawMessage
			if err := json.Unmarshal(children, &childGroups); err != nil {
				return nil, err
			}
			childKinds := make([]string, 0, len(childGroups))
			for ck := range childGroups {
				childKinds = append(childKinds, ck)
			}
			slices.Sort(childKinds)
			for _, ck := range childKinds {
				nested, err := walkOverviewGroup(childGroups[ck], name, childPath)
				if err != nil {
					return nil, err
				}
				out = append(out, nested...)
			}
		}
	}
	return out, nil
}

// qualify builds the name_path_pattern find_symbol needs to reach a nested
// name unambiguously -- "Options/Endpoint", not "Endpoint" -- matching the
// slash-jointed path syntax the tool's own schema documents.
func qualify(parentPath, name string) string {
	if parentPath == "" {
		return name
	}
	return parentPath + "/" + name
}

// parseReferences reads a find_referencing_symbols answer, which is a
// different shape from find_symbol's on purpose: path -> kind -> entries.
//
// The location that matters is NOT the entry's body_location. That is the
// enclosing symbol -- the function doing the referring -- and returning it
// would point the caller at a definition it did not ask about. The reference
// itself is the line Serena marks with ">" in the rendered context, so that is
// what gets parsed out.
func parseReferences(text string) ([]location, error) {
	trimmed := strings.TrimSpace(text)
	// find_referencing_symbols answers zero hits with "{}"; "" and "[]" are
	// accepted too so an empty answer is never mistaken for a shape this
	// adapter failed to read.
	if trimmed == "" || trimmed == "{}" || trimmed == "[]" {
		return nil, nil
	}
	var byPath map[string]map[string][]struct {
		NamePath string `json:"name_path"`
		Location struct {
			StartLine int `json:"start_line"`
		} `json:"body_location"`
		Around string `json:"content_around_reference"`
	}
	if err := json.Unmarshal([]byte(trimmed), &byPath); err != nil {
		return nil, fmt.Errorf("serena sent references nobody can read: %s", clip(trimmed))
	}
	var out []location
	for path, byKind := range byPath {
		for _, entries := range byKind {
			for _, entry := range entries {
				line, snippet, ok := markedLine(entry.Around)
				if !ok {
					// No marker: the enclosing symbol is all Serena gave, so
					// that is what is reported. Silently dropping the hit
					// would lose a real reference; pretending to know the
					// exact line would invent one.
					line = toContractLine(entry.Location.StartLine)
					snippet = strings.TrimSpace(entry.Around)
				}
				out = append(out, location{Path: path, Line: line, Snippet: snippet})
			}
		}
	}
	// Serena hands references back keyed by path, and a Go map has no order.
	// Two identical commissions returning the same locations shuffled would
	// make every answer unreproducible and every diff of two runs noise.
	slices.SortFunc(out, func(a, b location) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	return out, nil
}

// markedLine pulls the reference out of Serena's rendered context block, which
// looks like:
//
//	...   7:def twice():
//	  >   8:    return total() * 2
//	...   9:
//
// The ">" marks the referring line and the numbers are 0-based, like every
// other line number Serena reports.
func markedLine(around string) (int, string, bool) {
	for _, raw := range strings.Split(around, "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(raw), ">")
		if !found {
			continue
		}
		number, text, split := strings.Cut(strings.TrimSpace(rest), ":")
		if !split {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(number))
		if err != nil {
			continue
		}
		return toContractLine(n), strings.TrimSpace(text), true
	}
	return 0, "", false
}

// locationsFrom turns symbols into the capability's locations.
//
// The line reported is where the symbol writes its own name, not the first
// line of the range the language server handed over. Those are the same line
// in Go and different ones in Rust -- see nameSiteIn -- and a definition that
// answers with the doc comment above an item sends the caller to prose. It also
// made two capabilities disagree about one symbol in the same investigation:
// symbol.overview said `CANDIDATES` was on line 48, symbol.definition said 42.
//
// When the name cannot be placed the reported start stands. Unlike
// symbol.overview there is no sibling entry left to carry the call, and a line
// a few above the declaration is a better answer than refusing to give one.
func (r *Runner) locationsFrom(root string, symbols []symbol, a ask) []location {
	out := make([]location, 0, len(symbols))
	for _, s := range symbols {
		start := toContractLine(s.Location.StartLine)
		loc := location{Path: s.Path, Line: start}
		if name := lastSegment(s.NamePath); name != "" {
			span := toContractLine(s.Location.EndLine) - start + 1
			if span < 1 {
				span = 1
			}
			if span > nameScanLines {
				span = nameScanLines
			}
			// Best effort on purpose: this is a refinement of an answer that is
			// already usable, so an unreadable file leaves the start line alone
			// rather than failing a call that had succeeded.
			if window, err := readLinesFrom(r, root, s.Path, start, span); err == nil {
				if line, _, ok := nameSiteIn(window, start, name); ok {
					loc.Line = line
				}
			}
		}
		if a.snippet {
			body := s.Body
			if body == "" && s.Path != "" {
				start := toContractLine(s.Location.StartLine)
				if window, err := readLinesFrom(r, root, s.Path, start, a.lines); err == nil {
					body = strings.Join(window, "\n")
				}
			}
			loc.Snippet = trimToLines(body, a.lines)
		}
		out = append(out, loc)
	}
	return out
}

// lastSegment is the bare name at the end of a name path: "Options/Endpoint"
// names Endpoint. An overload index is dropped with it -- "my_method[1]" is
// written in the source as my_method.
func lastSegment(namePath string) string {
	name := namePath
	if cut := strings.LastIndex(name, "/"); cut >= 0 {
		name = name[cut+1:]
	}
	if cut := strings.LastIndex(name, "["); cut > 0 {
		name = name[:cut]
	}
	return name
}

// trimToLines honors "small by default, expandable": the caller said how much
// of a definition it wanted and a provider that returned a thousand-line class
// would have answered a different question.
func trimToLines(body string, lines int) string {
	if body == "" || lines <= 0 {
		return body
	}
	split := strings.Split(body, "\n")
	if len(split) <= lines {
		return body
	}
	return strings.Join(split[:lines], "\n")
}

// pick chooses which candidate the position meant.
//
// This is the whole reason the capability names a symbol by position: a file
// can hold several symbols with the same leaf name, and the design chose
// position precisely because it is unambiguous where a name is not. One
// candidate needs no choosing. Several, with the position inside one of them,
// is the case position exists for. Several with the position inside none is
// genuinely unresolved, and saying so beats guessing -- a wrong symbol answers
// a question nobody asked and looks exactly like a right one.
func pick(candidates []symbol, a ask) (symbol, error) {
	switch len(candidates) {
	case 0:
		return symbol{}, contract.Fail(contract.FailureNotFound,
			"serena knows no symbol named %q in %s", a.identifier, a.file)
	case 1:
		return candidates[0], nil
	}
	var covering []symbol
	for _, candidate := range candidates {
		if candidate.covers(a.line) {
			covering = append(covering, candidate)
		}
	}
	if len(covering) == 1 {
		return covering[0], nil
	}
	if len(covering) > 1 {
		// Nested symbols both cover the line -- a method inside a class, say.
		// The innermost one is the one the cursor is actually on.
		best := covering[0]
		for _, candidate := range covering[1:] {
			if candidate.Location.StartLine > best.Location.StartLine {
				best = candidate
			}
		}
		return best, nil
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.NamePath)
	}
	return symbol{}, contract.Fail(contract.FailureInvalidInput,
		"%s:%d:%d names %q, which is any of %s here; the position is inside none of them",
		a.file, a.line, a.column, a.identifier, strings.Join(names, ", "))
}

// readLineAt reads the exact 1-based line the caller pointed at, with the
// same sensitivity and containment checks every read in this adapter uses.
// identifierAt, declarationRegex and columnOf all need this line for
// different reasons -- two to find the word sitting on a known column, the
// third to find which column an already-known word sits at -- so the read
// has one place to be correct rather than three.
//
// file and line travel as plain values rather than an ask: columnOf's caller
// has a name from Serena's own overview, not a position, so there is no ask
// to hand in.
//
// The read stays inside the repository. That is not politeness: a step
// carries permission for the unit of work it was commissioned against, and a
// path that climbs out of it -- with .., with an absolute path, or through a
// symlink -- is reading something nobody authorized.
func readLineAt(r *Runner, root, file string, line int) (string, error) {
	lines, err := readLinesFrom(r, root, file, line, 1)
	if err != nil {
		return "", err
	}
	return lines[0], nil
}

// readLinesFrom reads up to count lines beginning at start, in one pass.
//
// The window exists for nameSiteIn: a symbol's own name sits somewhere inside
// the range its language server reported for it, and looking for it must not
// cost one file open per line of that range.
//
// A window running past the end of the file is not an error so long as it
// began inside it. The caller is asking about a symbol, not about a line
// number, and a short tail is just the end of the range.
func readLinesFrom(r *Runner, root, file string, start, count int) ([]string, error) {
	if r.isSensitive(file) {
		// Exploring skips these in silence because a skipped hit costs
		// nothing. This is not exploring: a caller asked about one exact
		// position, and answering "nothing found" would be a lie. It is
		// refused out loud instead.
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets and is not read", file)
	}
	resolved, err := within(root, file)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, contract.Fail(contract.FailureNotFound,
				"%s is not in this repository", file)
		}
		return nil, contract.Fail(contract.FailureUnavailable,
			"cannot read %s: %v", file, err)
	}
	// Nothing was written, so a failed close has nothing to report.
	defer func() { _ = f.Close() }()

	out := make([]string, 0, count)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for n := 1; scanner.Scan(); n++ {
		if n < start {
			continue
		}
		out = append(out, scanner.Text())
		if len(out) == count {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"cannot read %s: %v", file, err)
	}
	if len(out) == 0 {
		return nil, contract.Fail(contract.FailureNotFound,
			"%s has fewer than %d line(s)", file, start)
	}
	return out, nil
}

// identifierAt reads the one word the caller pointed at.
//
// This is the only place any adapter touches the filesystem, and it is the
// smallest thing that could work: one line, one word, no syntax. Serena names
// symbols and Atenea names positions, and something has to know which word
// sits at a position. Asking Serena is not an option -- its symbol overview
// carries no line numbers, its wildcard search times out on a real
// repository, and its position-based lookup (declarationRegex's job, below)
// needs the word already in hand before it can ask about it. All three were
// measured, not assumed.
func identifierAt(r *Runner, root string, a ask) (string, error) {
	line, err := readLineAt(r, root, a.file, a.line)
	if err != nil {
		return "", err
	}
	return wordAt(line, a.column, a)
}

// isSensitive matches the patterns against both the bare file name and the
// repository-relative path, so `.env` catches a root file and `config/*.pem`
// catches a nested one. Same rule as the other two adapters: one security
// design, applied the same way wherever a file is touched.
func (r *Runner) isSensitive(relative string) bool {
	name := path.Base(relative)
	for _, pattern := range r.sensitive {
		if ok, _ := path.Match(pattern, relative); ok {
			return true
		}
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

// within resolves a repository-relative path and refuses anything that leaves
// the repository, symlinks included.
func within(root, name string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = filepath.Clean(root)
	}
	joined := filepath.Clean(filepath.Join(realRoot, name))
	// The lexical check comes first so a path that never existed is still
	// refused for the right reason rather than as a missing file.
	if err := contained(realRoot, joined, name); err != nil {
		return "", err
	}
	// Then the real one. A symlink inside the repository pointing out of it
	// passes every string test there is, and following it would read a file
	// the commission never covered.
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to resolve: the file is simply not there, which the
			// caller finds out when it opens it and reports as not_found.
			return joined, nil
		}
		// Anything else means containment could not be verified. Failing open
		// on the check that keeps a read inside the commission would defeat
		// the point of having it.
		return "", contract.Fail(contract.FailurePermissionDenied,
			"file %q cannot be checked against the repository: %v", name, err)
	}
	if err := contained(realRoot, resolved, name); err != nil {
		return "", err
	}
	return resolved, nil
}

func contained(root, path, name string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return contract.Fail(contract.FailurePermissionDenied,
			"file %q leaves the repository", name)
	}
	return nil
}

// wordBounds finds the rune range of the identifier sitting at a 1-based
// column. wordAt and declarationRegex both need this: one to extract the
// word, the other to anchor it inside its own line.
//
// Identifier here means what every language this provider supports agrees on:
// letters, digits and underscore. It is lexical on purpose. An adapter that
// started deciding what is a type and what is a variable would be a second
// brain, and there is only supposed to be one.
func wordBounds(line string, column int, a ask) (runes []rune, start, end int, err error) {
	runes = []rune(line)
	index := column - 1
	if index >= len(runes) {
		return nil, 0, 0, contract.Fail(contract.FailureInvalidInput,
			"%s:%d has %d column(s), so column %d is past its end",
			a.file, a.line, len(runes), column)
	}
	if !isWord(runes[index]) {
		return nil, 0, 0, contract.Fail(contract.FailureInvalidInput,
			"%s:%d:%d is %q, which is not part of a name",
			a.file, a.line, column, string(runes[index]))
	}
	start = index
	for start > 0 && isWord(runes[start-1]) {
		start--
	}
	end = index
	for end+1 < len(runes) && isWord(runes[end+1]) {
		end++
	}
	return runes, start, end, nil
}

// wordAt extracts the identifier sitting at a 1-based column.
func wordAt(line string, column int, a ask) (string, error) {
	runes, start, end, err := wordBounds(line, column, a)
	if err != nil {
		return "", err
	}
	return string(runes[start : end+1]), nil
}

func isWord(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r > 127 // identifiers are not ASCII-only in Go, Python or TypeScript
}

// columnOf finds the 1-based column where name sits as a whole word on line
// -- the same lexical rule wordBounds enforces in the other direction,
// applied in reverse: a known name, on an already-known line, instead of a
// known column on a known line. It is the direction identifierAt never
// needed, because every other symbol.* capability starts from a position;
// this is the one capability that starts from a name instead.
//
// It returns the first whole-word match. That is deliberately not
// disambiguated further: Serena's own overview and find_symbol already
// agreed this line is where the name lives, so a second occurrence later on
// the same line -- a field used in its own default expression, say -- is not
// the one being pointed at.
func columnOf(line, name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	runes := []rune(line)
	target := []rune(name)
	for start := 0; start+len(target) <= len(runes); start++ {
		if start > 0 && isWord(runes[start-1]) {
			continue
		}
		end := start + len(target)
		if end < len(runes) && isWord(runes[end]) {
			continue
		}
		if string(runes[start:end]) == name {
			return start + 1, true
		}
	}
	return 0, false
}

// nameScanLines bounds how far into a symbol's reported range its own name is
// looked for. A declaration writes its name at the top of its range; scanning
// the whole of a long body would eventually find the name *used* rather than
// declared, and report that as the definition.
const nameScanLines = 24

// commentMarkers open a comment or continue one in every language this
// adapter serves. A Rust attribute (`#[derive(...)]`) is caught by the same
// `#` and skipped for the same reason: it is not where the name is declared.
var commentMarkers = []string{"///", "//", "/*", "*/", "*", "#"}

// looksLikeComment reports whether a line is prose or decoration rather than
// the declaration itself. A blank line counts, having nothing to declare.
func looksLikeComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	for _, marker := range commentMarkers {
		if strings.HasPrefix(trimmed, marker) {
			return true
		}
	}
	return false
}

// nameSiteIn finds where a symbol writes its own name inside the range its
// language server reported for it, and returns that line and column.
//
// The reported start line is not reliably the declaration, and which one it is
// depends on the language server rather than on anything Atenea controls.
// Measured live against two repositories: gopls starts a Go symbol at its
// `func` line, while rust-analyzer starts a Rust one at the first line of the
// doc comment above it -- `CANDIDATES` is declared on line 48 of encode.rs and
// reported as starting on 42. Reading only the start line therefore recovers
// the declaration in one language and a line of prose in the other, which is
// exactly what it did: `symbol.definition` answered 42 while `symbol.overview`,
// answering from a different provider, said 48 for the same symbol.
//
// Comment lines are passed over first, because a doc comment routinely
// mentions the name it documents and that line is not the declaration. If
// nothing outside a comment carries the name, a comment line holding it is
// still a better answer than none.
func nameSiteIn(lines []string, start int, name string) (line, column int, ok bool) {
	for i, text := range lines {
		if looksLikeComment(text) {
			continue
		}
		if column, found := columnOf(text, name); found {
			return start + i, column, true
		}
	}
	for i, text := range lines {
		if column, found := columnOf(text, name); found {
			return start + i, column, true
		}
	}
	return 0, 0, false
}

// declarationRegex builds the one-group pattern find_declaration's own
// interface requires in place of a line and column: the word at a.column,
// escaped, with the rest of its own line escaped around it.
//
// That line is what stands in for uniqueness -- Serena requires the regex to
// match its file exactly once, and the bare identifier alone would not (same-
// named symbols in one file are exactly the case find_declaration exists to
// disambiguate), but a whole line of real source rarely repeats itself
// verbatim anywhere else in the same file. When it does, or the position
// names nothing at all, the caller falls back to the same-file symbol search
// this adapter always used.
func declarationRegex(r *Runner, root string, a ask) (string, error) {
	line, err := readLineAt(r, root, a.file, a.line)
	if err != nil {
		return "", err
	}
	runes, start, end, err := wordBounds(line, a.column, a)
	if err != nil {
		return "", err
	}
	before := regexp.QuoteMeta(string(runes[:start]))
	word := regexp.QuoteMeta(string(runes[start : end+1]))
	after := regexp.QuoteMeta(string(runes[end+1:]))
	return before + "(" + word + ")" + after, nil
}
