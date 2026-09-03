package kivgraph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/pidlock"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	defaultImpactDepth = 3
	impactMaxNodes     = 5000
	impactMaxResults   = 500
)

var diffHunkPattern = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)

type diffRange struct {
	start int
	end   int
}

type impactSymbol struct {
	key       string
	repo      string
	path      string
	line      int
	name      string
	kind      string
	depth     int
	qualified string
	snippet   string
}

type blastRadius struct {
	TraversalTruncated bool          `json:"traversal_truncated"`
	Symbols            []blastSymbol `json:"symbols"`
}

type blastEnvelope struct {
	Results *blastRadius `json:"results"`
	blastRadius
}

type blastSymbol struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	Depth         int    `json:"depth"`
	Repository    string `json:"repository"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	StableKey     string `json:"stable_key"`
}

type indexEvent struct {
	Event  string         `json:"event"`
	Result *indexDocument `json:"result,omitempty"`
}

type indexDocument struct {
	Passed       bool   `json:"passed"`
	GenerationID string `json:"generation_id"`
	Counts       struct {
		Symbols                     int `json:"symbols"`
		Edges                       int `json:"edges"`
		RustWorkspacesNotLoaded     int `json:"rust_workspaces_not_loaded"`
		GoModulesNotLoaded          int `json:"go_modules_not_loaded"`
		PythonRepositoriesNotLoaded int `json:"python_repositories_not_loaded"`
		DartRepositoriesNotLoaded   int `json:"dart_repositories_not_loaded"`
		JavaRepositoriesNotLoaded   int `json:"java_repositories_not_loaded"`
		CSharpRepositoriesNotLoaded int `json:"csharp_repositories_not_loaded"`
	} `json:"counts"`
	Error string `json:"error,omitempty"`
}

// runIndex executes the only mutating Kivgraph operation exposed by Atenea.
// The command indexes every repository in Kivgraph's registry; the requested
// repository is checked afterwards so the result cannot claim success for a
// graph that did not publish that repository.
func (r *Runner) runIndex(ctx context.Context, sess Session, req contract.RunRequest) (map[string]any, []string, error) {
	if r.index == nil {
		return nil, nil, contract.Fail(contract.FailureUnavailable,
			"kivgraph repository.index: no official index command is configured")
	}
	var previous int
	if r.requireFresh {
		if r.maintenanceDirectory == "" {
			return nil, nil, contract.Fail(contract.FailureUnavailable, "graph maintenance directory missing")
		}
		for {
			release, err := pidlock.Claim(filepath.Join(r.maintenanceDirectory, "index.lock"))
			if err == nil {
				defer release()
				break
			}
			if !errors.Is(err, pidlock.ErrHeld) {
				return nil, nil, err
			}
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if before, err := r.fetchStatus(ctx, sess); err == nil && before != nil {
			previous = before.SnapshotID
		}
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}
	mode, _ := stringAt(req.Payload, "mode")
	if mode == "" {
		mode = "full"
	}
	if mode != "full" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"kivgraph repository.index: only mode full is supported, got %q", mode)
	}
	report, err := r.index(ctx, root, mode)
	if err != nil {
		return nil, nil, err
	}
	status, err := r.fetchStatus(ctx, sess)
	if err != nil {
		return nil, nil, err
	}
	if r.requireFresh {
		generation, err := strconv.Atoi(report.Generation)
		if err != nil || generation <= previous {
			return nil, nil, contract.Fail(contract.FailureUnavailable, "index did not advance generation")
		}
		for status != nil && status.SnapshotID < generation {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(time.Second):
			}
			status, err = r.fetchStatus(ctx, sess)
			if err != nil {
				return nil, nil, err
			}
		}
		if !contentFresh(status) || status.SnapshotID != generation {
			return nil, nil, contract.Fail(contract.FailureUnavailable, "published generation freshness is not verified")
		}
	}
	if err := checkGraphReady(status, root); err != nil {
		return nil, nil, err
	}
	result := map[string]any{
		"status": status.Status,
		"nodes":  report.Nodes,
		"edges":  report.Edges,
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}
	note := fmt.Sprintf("kivgraph indexed its registered workspace for %s: %d nodes and %d edges",
		req.Repository.ID, report.Nodes, report.Edges)
	if report.Generation != "" {
		note += fmt.Sprintf(" (generation %s)", report.Generation)
	}
	notes := []string{note}
	for _, language := range []string{"go", "rust", "python", "dart", "java", "csharp"} {
		if count := report.NotLoaded[language]; count > 0 {
			notes = append(notes, fmt.Sprintf("kivgraph index warning: %d %s workspace(s) were not loaded", count, language))
		}
	}
	return result, notes, nil
}

// runImpact maps a baseline diff to declaration roots and then asks the
// provider's official blast-radius traversal for incoming consumers. Kivgraph
// returns a global graph, while code.impact has repository-relative paths and
// no repository field, so foreign repositories are intentionally omitted.
func (r *Runner) runImpact(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	baseline, ok := stringAt(req.Payload, "baseline")
	if !ok || strings.TrimSpace(baseline) == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph code.impact: baseline is required")
	}
	if strings.HasPrefix(strings.TrimSpace(baseline), "-") {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph code.impact: baseline must be a Git revision, not an option")
	}
	depth, ok := intAt(req.Payload, "depth")
	if !ok {
		depth = defaultImpactDepth
	}
	if depth < 0 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph code.impact: depth must not be negative, got %d", depth)
	}
	kivgraphRepo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	scope, err := impactScope(root, scopeEntries(req.Payload))
	if err != nil {
		return nil, nil, err
	}
	changed, ranges, unrecognized, err := gitDiff(ctx, root, baseline, scope)
	if err != nil {
		return nil, nil, err
	}

	var notes []string
	if unrecognized > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d file header(s) in the baseline diff could not be read, so those files are covered whole rather than by the lines that changed",
			unrecognized))
	}
	roots := make([]impactSymbol, 0)
	for _, relative := range changed {
		fileRanges := ranges[relative]
		if len(fileRanges) == 0 {
			notes = append(notes, fmt.Sprintf("%s changed but has no current lines to map (deleted, binary or empty)", relative))
			continue
		}
		if r.isSensitive(relative) {
			return nil, nil, contract.Fail(contract.FailurePermissionDenied,
				"kivgraph code.impact: %s carries secrets: this adapter never reads it", relative)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			// Deleted or binary files remain in changed_files, but have no
			// current declaration that can be walked in the live graph.
			notes = append(notes, fmt.Sprintf("%s changed but no longer exists in the current working tree", relative))
			continue
		}
		text, err := sess.Call(ctx, toolOutline, map[string]any{
			"repository": kivgraphRepo, "path": relative, "view": "full", "response_format": "detailed",
		})
		if err != nil {
			// A baseline can contain a newly added or currently unindexed file.
			// That is an incomplete graph answer, not a dead provider: retain the
			// file in changed_files and make the missing coverage explicit.
			if strings.Contains(err.Error(), "SYMBOL_NOT_FOUND") || strings.Contains(err.Error(), "has no indexed file") {
				notes = append(notes, fmt.Sprintf("%s changed but Kivgraph does not have that file in the published snapshot", relative))
				continue
			}
			return nil, nil, contract.Fail(contract.FailureUnavailable,
				"kivgraph code.impact: could not read %s's outline: %v", relative, err)
		}
		var outline outlineAnswer
		if err := json.Unmarshal([]byte(text), &outline); err != nil {
			return nil, nil, contract.Fail(contract.FailureUnavailable,
				"kivgraph code.impact: unreadable outline for %s: %v", relative, err)
		}
		metadataNotes, err := outlineMetadata(text)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, metadataNotes...)
		found := 0
		unaddressed := 0
		for _, decl := range outline.declarations() {
			if !declarationOverlaps(decl, fileRanges) {
				continue
			}
			key, err := r.stableKeyOf(ctx, sess, CapabilityImpact, kivgraphRepo, relative, decl)
			if err != nil {
				// One declaration the graph cannot address is one root missing
				// from the blast radius. A diff of any size touches many, and
				// refusing the whole analysis over the one Kivgraph has not
				// indexed throws away every consumer of the others -- the same
				// judgement the unindexed-file branch above already makes.
				// Anything that is not "nothing by that name" still stops the
				// call: that is a provider in trouble, not a gap.
				if contract.KindOf(err) == contract.FailureNotFound {
					unaddressed++
					named := decl.QualifiedName
					if named == "" {
						named = decl.Name
					}
					notes = append(notes, fmt.Sprintf(
						"%s at %s:%d changed, but Kivgraph could not address it, so whatever depends on it is missing from this answer",
						named, relative, decl.StartLine))
					continue
				}
				return nil, nil, err
			}
			roots = append(roots, impactSymbol{
				key: key, repo: kivgraphRepo, path: relative, line: decl.StartLine,
				name: decl.Name, kind: decl.Kind, qualified: decl.QualifiedName,
			})
			found++
		}
		// Only when nothing overlapped at all: a declaration that overlapped
		// and could not be addressed has already said so, and saying "found
		// none" on top of it would be a second, untrue explanation.
		if found == 0 && unaddressed == 0 {
			notes = append(notes, fmt.Sprintf("%s changed but Kivgraph found no current declaration over the changed lines", relative))
		}
	}

	byKey := make(map[string]impactSymbol, len(roots))
	for _, symbol := range roots {
		addImpactSymbol(byKey, symbol)
	}
	truncated := false
	foreign := 0
	for _, rootSymbol := range roots {
		if depth == 0 {
			continue
		}
		text, err := sess.Call(ctx, toolBlast, map[string]any{
			"stable_key":      rootSymbol.key,
			"depth":           depth,
			"max_nodes":       impactMaxNodes,
			"limit":           impactMaxResults,
			"response_format": "detailed",
			"view":            "full",
		})
		if err != nil {
			return nil, nil, fmt.Errorf("kivgraph %s for %s: %w", toolBlast, rootSymbol.name, err)
		}
		var answer blastEnvelope
		if err := json.Unmarshal([]byte(text), &answer); err != nil {
			return nil, nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolBlast, err)
		}
		metadataNotes, err := queryMetadata(toolBlast, text, true)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, metadataNotes...)
		radius := answer.blastRadius
		if answer.Results != nil {
			radius = *answer.Results
		}
		truncated = truncated || radius.TraversalTruncated
		for _, reached := range radius.Symbols {
			if reached.Repository != "" && repositoryNameFromKey(reached.Repository) != kivgraphRepo {
				foreign++
				continue
			}
			relative, ok := graphRelativePath(reached.FilePath)
			if !ok || reached.StartLine <= 0 || reached.Name == "" {
				continue
			}
			addImpactSymbol(byKey, impactSymbol{
				key: reached.StableKey, repo: kivgraphRepo, path: relative,
				line: reached.StartLine, name: reached.Name, kind: reached.Kind,
				depth: reached.Depth, qualified: reached.QualifiedName,
			})
		}
	}

	ordered := make([]impactSymbol, 0, len(byKey))
	for _, symbol := range byKey {
		if boolAt(req.Payload, "include_snippet") {
			snippet, err := r.snippetAt(root, symbol.path, symbol.line, snippetWindow(req.Payload))
			if err != nil {
				return nil, nil, err
			}
			symbol.snippet = snippet
		}
		ordered = append(ordered, symbol)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].depth != ordered[j].depth {
			return ordered[i].depth < ordered[j].depth
		}
		if ordered[i].path != ordered[j].path {
			return ordered[i].path < ordered[j].path
		}
		if ordered[i].line != ordered[j].line {
			return ordered[i].line < ordered[j].line
		}
		return ordered[i].name < ordered[j].name
	})

	records := make([]any, 0, len(ordered))
	for _, symbol := range ordered {
		record := map[string]any{"path": symbol.path, "line": symbol.line, "name": symbol.name, "depth": symbol.depth}
		if symbol.kind != "" {
			record["kind"] = symbol.kind
		}
		if symbol.snippet != "" {
			record["snippet"] = symbol.snippet
		}
		records = append(records, record)
	}
	result := map[string]any{"changed_files": changed, "affected_symbols": records}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}
	notes = append(notes, fmt.Sprintf("kivgraph mapped %d changed file(s) to %d affected symbol(s) at depth %d", len(changed), len(records), depth))
	if foreign > 0 {
		notes = append(notes, fmt.Sprintf("%d affected symbol(s) from other repositories were omitted because code.impact returns repository-relative paths without a repository field", foreign))
	}
	if truncated {
		notes = append(notes, "Kivgraph truncated at least one blast-radius traversal; the result is a bounded impact, not a completeness proof")
	}
	return result, notes, nil
}

func addImpactSymbol(index map[string]impactSymbol, symbol impactSymbol) {
	key := symbol.key
	if key == "" {
		key = fmt.Sprintf("%s:%s:%d:%s", symbol.repo, symbol.path, symbol.line, symbol.name)
		symbol.key = key
	}
	if current, ok := index[key]; !ok || symbol.depth < current.depth {
		index[key] = symbol
	}
}

func declarationOverlaps(decl outlineDeclaration, ranges []diffRange) bool {
	start, end := decl.StartLine, decl.EndLine
	if end < start {
		end = start
	}
	for _, changed := range ranges {
		if start <= changed.end && changed.start <= end {
			return true
		}
	}
	return false
}

func graphRelativePath(value string) (string, bool) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || filepath.IsAbs(filepath.FromSlash(value)) {
		return "", false
	}
	clean := pathClean(value)
	if clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func pathClean(value string) string {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." {
		return ""
	}
	return clean
}

func impactScope(root string, scope []string) ([]string, error) {
	if len(scope) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(scope))
	for _, entry := range scope {
		entry = pathClean(entry)
		if entry == "" {
			return nil, contract.Fail(contract.FailureInvalidInput, "kivgraph code.impact: scope entry %q is not a repository-relative path", entry)
		}
		if _, err := within(root, entry); err != nil {
			return nil, err
		}
		normalized = append(normalized, entry)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// gitDiff reports which files changed against the baseline and which of their
// CURRENT lines the change touches.
//
// The fourth return counts file headers in the hunk diff that this parser
// could not attribute to a path. It should always be zero, and the reason it
// is reported rather than assumed is the third invocation below: the parser
// reads "+++ b/<path>", and "b/" is only the default. diff.noprefix removes it
// and diff.mnemonicPrefix replaces it (w/ for the working tree), both of them
// ordinary entries in a developer's ~/.gitconfig. With either set, every
// header stopped matching, no file got a hunk, and code.impact silently fell
// back to covering whole files -- an answer that looks exactly like a correct
// one and blames far more symbols than the change touched. The prefixes are
// forced on the command line now; the count is what would say so if the
// assumption ever breaks again.
// gitIn builds a git invocation that operates on root and nowhere else.
//
// command.Dir alone does not achieve that, which is the whole reason this
// exists. GIT_DIR names the repository git works on and OVERRIDES the working
// directory; GIT_WORK_TREE, GIT_INDEX_FILE and the object-directory variables
// steer the rest of it the same way. Every one of them is exported into the
// environment by a git hook, so an Atenea reached from inside one -- or from
// any parent that happened to set them -- would diff a repository the caller
// never named, and answer confidently about the wrong code.
//
// It is the same class of problem the prefix flags above already guard
// against: the environment is not a neutral place to run git from, and the
// fix in both cases is to say what is meant rather than to inherit it. The
// variables are REMOVED rather than blanked, because `GIT_DIR=` is a GIT_DIR
// holding the empty string and git refuses it outright.
func gitIn(ctx context.Context, root string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	steering := []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
		"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES="}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if slices.ContainsFunc(steering, func(prefix string) bool {
			return strings.HasPrefix(entry, prefix)
		}) {
			continue
		}
		environment = append(environment, entry)
	}
	command.Env = environment
	return command
}

func gitDiff(ctx context.Context, root, baseline string, scope []string) ([]string, map[string][]diffRange, int, error) {
	args := []string{"diff", "--name-only", "-z", "--no-ext-diff", "--no-renames", baseline, "--"}
	args = append(args, scope...)
	command := gitIn(ctx, root, args...)
	var names bytes.Buffer
	if output, err := command.Output(); err != nil {
		return nil, nil, 0, fmt.Errorf("git diff changed files against %q: %w", baseline, err)
	} else {
		names.Write(output)
	}
	changed := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range strings.Split(names.String(), "\x00") {
		if relative, ok := graphRelativePath(value); ok && inScope(relative, scope) {
			appendChangedPath(&changed, seen, relative)
		}
	}
	// `git diff baseline` intentionally excludes untracked files. They are
	// nevertheless part of the current working tree a caller asked us to
	// compare, so include them as additions and cover their current lines.
	args = []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}
	args = append(args, scope...)
	command = gitIn(ctx, root, args...)
	output, err := command.Output()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("git list untracked files: %w", err)
	}
	for _, value := range strings.Split(string(output), "\x00") {
		if relative, ok := graphRelativePath(value); ok && inScope(relative, scope) {
			appendChangedPath(&changed, seen, relative)
		}
	}
	sort.Strings(changed)

	// --src-prefix/--dst-prefix override whatever diff.noprefix and
	// diff.mnemonicPrefix say in the user's configuration, so the headers this
	// loop reads are the ones it expects on every machine.
	args = []string{"diff", "--unified=0", "--no-ext-diff", "--no-renames",
		"--src-prefix=a/", "--dst-prefix=b/", baseline, "--"}
	args = append(args, scope...)
	command = gitIn(ctx, root, args...)
	output, err = command.Output()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("git diff hunks against %q: %w", baseline, err)
	}
	ranges := make(map[string][]diffRange)
	current := ""
	unrecognized := 0
	// "diff --git " at column 0 is the one unambiguous anchor in a diff body:
	// added lines are prefixed with "+", removed ones with "-", context with a
	// space, so no content line can produce it. Reading the target header only
	// straight after it keeps a line of added source that happens to start with
	// "++ " from being mistaken for a file header.
	expectTarget := false
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			current, expectTarget = "", true
			continue
		}
		if target, found := strings.CutPrefix(line, "+++ "); found && expectTarget {
			expectTarget = false
			// A deletion names /dev/null as its target and has no current
			// lines: not a header this parser failed to read.
			if target == "/dev/null" {
				continue
			}
			name, prefixed := strings.CutPrefix(target, "b/")
			if !prefixed {
				unrecognized++
				continue
			}
			current, _ = graphRelativePath(name)
			continue
		}
		matches := diffHunkPattern.FindStringSubmatch(line)
		if current == "" || matches == nil {
			continue
		}
		start, _ := strconv.Atoi(matches[1])
		count := 1
		if matches[2] != "" {
			count, _ = strconv.Atoi(matches[2])
		}
		if count == 0 {
			continue
		}
		ranges[current] = append(ranges[current], diffRange{start: start, end: start + count - 1})
	}
	for _, relative := range changed {
		if _, already := ranges[relative]; already {
			continue
		}
		lines := readLines(filepath.Join(root, filepath.FromSlash(relative)))
		if len(lines) > 0 {
			ranges[relative] = []diffRange{{start: 1, end: len(lines)}}
		}
	}
	return changed, ranges, unrecognized, nil
}

func appendChangedPath(changed *[]string, seen map[string]struct{}, relative string) {
	if _, exists := seen[relative]; exists {
		return
	}
	seen[relative] = struct{}{}
	*changed = append(*changed, relative)
}

// RunConfiguredIndex runs Kivgraph's official full-index command for the core
// wiring and returns the final JSONL result document. mode is deliberately
// explicit even though the only supported provider mode is full.
func RunConfiguredIndex(ctx context.Context, executable string, env []string, root, mode string) (IndexReport, error) {
	if strings.TrimSpace(executable) == "" {
		return IndexReport{}, contract.Fail(contract.FailureUnavailable, "kivgraph repository.index: binary is empty")
	}
	if mode != "full" {
		return IndexReport{}, contract.Fail(contract.FailureInvalidInput,
			"kivgraph repository.index: only mode full is supported, got %q", mode)
	}
	command := exec.CommandContext(ctx, executable, "index", "--full", "--json")
	command.Dir = root
	command.Env = append(os.Environ(), env...)
	// stdout is scanned as it arrives; only stderr is tailed.
	//
	// It used to be tailed too, and then parsed: limitedTail keeps the last
	// 8 KiB and cuts wherever the byte fell, so the first surviving line of
	// any longer output was half an object. parseIndexReport failed on it and
	// repository.index -- the one mutating capability in the system -- came
	// back "returned invalid JSONL" for an indexing run that had completed
	// perfectly. `kivgraph index --full --json` emits a progress event per
	// unit of work; the workspace this was built against reports 19,885
	// symbols, so passing 8 KiB is the ordinary case and staying under it was
	// the exception.
	//
	// stderr is a different job: it is quoted in an error message, so the tail
	// is what a reader wants and the truncation costs nothing.
	stdout, err := command.StdoutPipe()
	if err != nil {
		return IndexReport{}, fmt.Errorf("kivgraph index --full: %w", err)
	}
	var stderr limitedTail
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return IndexReport{}, fmt.Errorf("kivgraph index --full failed: %w", err)
	}
	report, parseErr := parseIndexStream(stdout)
	// Drained before Wait, always: Wait closes the pipe, and a Wait that ran
	// while the scan was still reading would race it. parseIndexStream returns
	// only after the stream ends or a line refuses to parse -- and on the
	// second, the rest is discarded so the child is not left writing into a
	// full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	if err := command.Wait(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return IndexReport{}, fmt.Errorf("kivgraph index --full failed: %w: %s", err, detail)
		}
		return IndexReport{}, fmt.Errorf("kivgraph index --full failed: %w", err)
	}
	if parseErr != nil {
		return IndexReport{}, parseErr
	}
	return report, nil
}

// parseIndexStream reads the JSONL as it arrives and keeps the last result.
//
// maxIndexLine is generous rather than tight: the result document carries the
// whole run's counters, and a line the scanner refuses is reported as such
// instead of being silently cut into something that no longer parses -- which
// is the failure this replaced.
func parseIndexStream(stream io.Reader) (IndexReport, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64<<10), maxIndexLine)
	var report *indexDocument
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event indexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return IndexReport{}, fmt.Errorf("kivgraph index --full returned invalid JSONL: %w", err)
		}
		if event.Event == "result" && event.Result != nil {
			report = event.Result
		}
	}
	if err := scanner.Err(); err != nil {
		return IndexReport{}, fmt.Errorf("kivgraph index --full: reading the report: %w", err)
	}
	return finishIndexReport(report)
}

// parseIndexReport is the same reading over a string somebody already holds.
func parseIndexReport(stream string) (IndexReport, error) {
	return parseIndexStream(strings.NewReader(stream))
}

// maxIndexLine bounds one JSONL line. A provider that emits more than this on
// a single line is reported rather than truncated.
const maxIndexLine = 4 << 20

func finishIndexReport(report *indexDocument) (IndexReport, error) {
	if report == nil {
		return IndexReport{}, fmt.Errorf("kivgraph index --full returned no result event")
	}
	if !report.Passed {
		if report.Error != "" {
			return IndexReport{}, fmt.Errorf("kivgraph index --full did not pass: %s", report.Error)
		}
		return IndexReport{}, fmt.Errorf("kivgraph index --full did not pass")
	}
	// A count below zero is not a small index, it is a broken sender. These
	// two numbers are the declared output of repository.index -- what a chat
	// reads to decide whether the graph is worth querying -- and nothing
	// downstream treats them as anything but sizes. Refusing here says which
	// field was wrong, where letting it through hands a negative size to a
	// caller with no way to trace it back.
	notLoaded := map[string]int{
		"go":     report.Counts.GoModulesNotLoaded,
		"rust":   report.Counts.RustWorkspacesNotLoaded,
		"python": report.Counts.PythonRepositoriesNotLoaded,
		"dart":   report.Counts.DartRepositoriesNotLoaded,
		"java":   report.Counts.JavaRepositoriesNotLoaded,
		"csharp": report.Counts.CSharpRepositoriesNotLoaded,
	}
	if report.Counts.Symbols < 0 || report.Counts.Edges < 0 {
		return IndexReport{}, fmt.Errorf(
			"kivgraph index --full reported %d symbols and %d edges, and a count cannot be negative",
			report.Counts.Symbols, report.Counts.Edges)
	}
	for language, count := range notLoaded {
		if count < 0 {
			return IndexReport{}, fmt.Errorf("kivgraph index --full reported a negative not_loaded count for %s", language)
		}
	}
	return IndexReport{Generation: report.GenerationID, Nodes: report.Counts.Symbols, Edges: report.Counts.Edges, NotLoaded: notLoaded}, nil
}

type limitedTail struct {
	data []byte
}

func (b *limitedTail) Write(data []byte) (int, error) {
	b.data = append(b.data, data...)
	if len(b.data) > 8192 {
		b.data = b.data[len(b.data)-8192:]
	}
	return len(data), nil
}

func (b *limitedTail) String() string { return string(b.data) }
