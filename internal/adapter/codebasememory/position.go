package codebasememory

// Turning Atenea's file:line:column into the identifier sitting there, and
// reading the handful of lines around a location for a snippet. Both jobs
// touch the filesystem directly rather than asking codebase-memory-mcp,
// which is the same choice internal/adapter/serena makes and for the same
// reason: neither trace_path nor query_graph can be asked "what word is
// under this exact column", and the file on disk always can. It is lexical
// on purpose -- letters, digits and underscore -- because deciding what is a
// type and what is a variable would be a second brain, and there is only
// supposed to be one.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// maxLineBytes caps one line read out of a source file. A minified bundle or
// a checked-in blob is not something to pull into memory whole just to find
// the word under a cursor.
const maxLineBytes = 1 << 20

// identifierAt reads the one word the caller pointed at.
//
// The read stays inside the repository. That is not politeness: a step
// carries permission for the unit of work it was commissioned against, and a
// path that climbs out of it -- with .., with an absolute path, or through a
// symlink -- is reading something nobody authorized.
func (r *Runner) identifierAt(root, file string, line, column int) (string, error) {
	if r.isSensitive(file) {
		// symbol.calls asks about one exact position, so answering "nothing
		// found" for a sensitive file would be a lie. It is refused out loud
		// instead, the same choice Serena's own identifierAt makes.
		return "", contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets and is not read", file)
	}
	resolved, err := within(root, file)
	if err != nil {
		return "", err
	}
	f, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", contract.Fail(contract.FailureNotFound, "%s is not in this repository", file)
		}
		return "", contract.Fail(contract.FailureUnavailable, "cannot read %s: %v", file, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for n := 1; scanner.Scan(); n++ {
		if n != line {
			continue
		}
		return wordAt(scanner.Text(), column, file, line)
	}
	if err := scanner.Err(); err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "cannot read %s: %v", file, err)
	}
	return "", contract.Fail(contract.FailureInvalidInput, "%s has fewer than %d line(s)", file, line)
}

// snippetFor reads a window of n lines starting at line, the convention this
// capability family already uses: "how much of it to return" is a forward
// look from the anchor line, not a window centered on it.
func (r *Runner) snippetFor(root, file string, line, n int) (string, error) {
	if r.isSensitive(file) {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets and is not read", file)
	}
	resolved, err := within(root, file)
	if err != nil {
		return "", err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "cannot read %s: %v", file, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var lines []string
	for i := 1; scanner.Scan(); i++ {
		if i < line {
			continue
		}
		if i >= line+n {
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "cannot read %s: %v", file, err)
	}
	return strings.Join(lines, "\n"), nil
}

// wordAt extracts the identifier sitting at a 1-based column.
func wordAt(text string, column int, file string, line int) (string, error) {
	runes := []rune(text)
	index := column - 1
	if index < 0 || index >= len(runes) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"%s:%d has %d column(s), so column %d is past its end", file, line, len(runes), column)
	}
	if !isWord(runes[index]) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"%s:%d:%d is %q, which is not part of a name", file, line, column, string(runes[index]))
	}
	start := index
	for start > 0 && isWord(runes[start-1]) {
		start--
	}
	end := index
	for end+1 < len(runes) && isWord(runes[end+1]) {
		end++
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

// within resolves a repository-relative path against root, refusing anything
// that climbs out of it: an absolute path, a "..", or a symlink that lands
// outside once resolved.
func within(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(filepath.FromSlash(name)) {
		return "", contract.Fail(contract.FailureInvalidInput, "%q must be a relative path", name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "cannot resolve repository root: %v", err)
	}
	joined := filepath.Join(rootAbs, filepath.FromSlash(name))
	if err := contained(rootAbs, joined, name); err != nil {
		return "", err
	}
	// A symlink can point outside the repository even when the lexical path
	// does not climb out of it. EvalSymlinks fails on a path that does not
	// exist yet, which is fine here: the caller's own os.Open reports that
	// case as not-found, a clearer answer than this function guessing at it.
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			return joined, nil
		}
		return "", contract.Fail(contract.FailureUnavailable, "cannot resolve %q: %v", name, err)
	}
	if err := contained(rootAbs, resolved, name); err != nil {
		return "", err
	}
	return joined, nil
}

// contained reports whether path sits inside root, once both are absolute.
func contained(root, path, name string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return contract.Fail(contract.FailureInvalidInput, "%q escapes the repository", name)
	}
	return nil
}
