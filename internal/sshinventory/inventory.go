// Package sshinventory lists explicit OpenSSH aliases without invoking ssh,
// evaluating Match exec, or opening a network connection. It does not resolve
// effective options or grant authorization to connect.
package sshinventory

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxDepth = 16
	maxFiles = 256
	maxBytes = 4 << 20
	maxLine  = 64 << 10
)

// Host is an explicit, non-wildcard Host token. Conditional means an enclosing
// Include condition could not be established without effective-config evaluation.
type Host struct {
	Alias       string
	Source      string
	Line        int
	Conditional bool
}

// Diagnostic reports syntax and unsupported evaluation without printing config values.
type Diagnostic struct {
	Source string
	Line   int
	Code   string
}

// Inventory is a display-only list with diagnostics and a change fingerprint.
type Inventory struct {
	Hosts       []Host
	Diagnostics []Diagnostic
	Snapshot    string // hash of traversed file contents and Include expansion order
}

type condition struct {
	patterns []string
	unknown  bool
}

type scanner struct {
	result Inventory
	hash   io.Writer
	files  int
	bytes  int
	active map[string]bool
	seen   map[string]int
}

// Scan reads the user and system config in OpenSSH precedence order. Empty
// paths omit a source. Include paths are relative to the source's config
// directory, not the directory of a nested included file.
func Scan(userConfig, systemConfig string) (Inventory, error) {
	h := sha256.New()
	s := &scanner{hash: h, active: make(map[string]bool), seen: make(map[string]int)}
	for _, root := range []string{userConfig, systemConfig} {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return Inventory{}, err
		}
		fmt.Fprintf(s.hash, "root:%s\x00", abs)
		if err := s.file(abs, filepath.Dir(abs), nil, 0, true); err != nil {
			return Inventory{}, err
		}
	}
	s.result.Snapshot = hex.EncodeToString(h.Sum(nil))
	return s.result, nil
}

func (s *scanner) file(path, includeRoot string, inherited []condition, depth int, optional bool) error {
	if depth > maxDepth || s.files >= maxFiles {
		s.diagnostic(path, 0, "limit_exceeded")
		return nil
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		s.diagnostic(path, 0, "file_unavailable")
		return nil
	}
	if s.active[canonical] {
		s.diagnostic(path, 0, "include_cycle")
		return nil
	}
	file, err := os.Open(canonical)
	if err != nil {
		s.diagnostic(path, 0, "file_unavailable")
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maxBytes-s.bytes) {
		s.diagnostic(path, 0, "file_unavailable_or_limit")
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes-s.bytes)+1))
	if err != nil || len(data) > maxBytes-s.bytes {
		s.diagnostic(path, 0, "file_unavailable_or_limit")
		return nil
	}
	s.files++
	s.bytes += len(data)
	fmt.Fprintf(s.hash, "%s\x00%d\x00", canonical, len(data))
	_, _ = s.hash.Write(data)
	s.active[canonical] = true
	defer delete(s.active, canonical)

	current := condition{}
	reader := bufio.NewScanner(strings.NewReader(string(data)))
	reader.Buffer(make([]byte, 4096), maxLine+1)
	for line := 1; reader.Scan(); line++ {
		fields, ok := splitLine(reader.Text())
		if !ok {
			s.diagnostic(path, line, "invalid_syntax")
			continue
		}
		if len(fields) == 0 {
			continue
		}
		key, args := strings.ToLower(fields[0]), fields[1:]
		conditions := append(append([]condition(nil), inherited...), current)
		switch key {
		case "host":
			current = condition{patterns: args}
			if len(args) == 0 {
				s.diagnostic(path, line, "empty_host")
			}
			for _, alias := range args {
				if !concrete(alias) {
					continue
				}
				blocked, unknown := evaluate(append(inherited, current), alias)
				if blocked {
					continue
				}
				key := strings.ToLower(alias)
				if index, exists := s.seen[key]; exists {
					if s.result.Hosts[index].Conditional && !unknown {
						s.result.Hosts[index] = Host{Alias: alias, Source: path, Line: line}
					}
					continue
				}
				s.seen[key] = len(s.result.Hosts)
				s.result.Hosts = append(s.result.Hosts, Host{Alias: alias, Source: path, Line: line, Conditional: unknown})
			}
		case "match":
			// Match can depend on exec, final-pass processing, user, network,
			// tags, or a rewritten hostname. Never evaluate it while listing.
			current = condition{unknown: true}
			s.diagnostic(path, line, "match_requires_selected_resolution")
		case "include":
			if len(args) == 0 {
				s.diagnostic(path, line, "empty_include")
				continue
			}
			for _, pattern := range args {
				if strings.ContainsAny(pattern, "%${}") || (strings.HasPrefix(pattern, "~") && !strings.HasPrefix(pattern, "~/")) {
					s.diagnostic(path, line, "dynamic_include")
					continue
				}
				if strings.HasPrefix(pattern, "~/") {
					pattern = filepath.Join(includeRoot, pattern[2:])
				} else if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(includeRoot, pattern)
				}
				paths, err := filepath.Glob(pattern)
				if err != nil {
					s.diagnostic(path, line, "invalid_include_glob")
					continue
				}
				sort.Strings(paths)
				fmt.Fprintf(s.hash, "include:%s\x00", pattern)
				for _, nested := range paths {
					fmt.Fprintf(s.hash, "%s\x00", nested)
					if err := s.file(nested, includeRoot, conditions, depth+1, true); err != nil {
						return err
					}
				}
			}
		}
	}
	if reader.Err() != nil {
		s.diagnostic(path, 0, "line_limit")
	}
	return nil
}

func (s *scanner) diagnostic(path string, line int, code string) {
	s.result.Diagnostics = append(s.result.Diagnostics, Diagnostic{Source: path, Line: line, Code: code})
}

func concrete(token string) bool {
	return token != "" && !strings.HasPrefix(token, "!") && !strings.ContainsAny(token, "*?[]/\\ \t\r\n")
}

func evaluate(conditions []condition, alias string) (blocked, unknown bool) {
	for _, c := range conditions {
		if c.unknown {
			unknown = true
			continue
		}
		if len(c.patterns) == 0 {
			continue
		}
		positive := false
		for _, pattern := range c.patterns {
			negated := strings.HasPrefix(pattern, "!")
			matched, err := filepath.Match(strings.ToLower(strings.TrimPrefix(pattern, "!")), strings.ToLower(alias))
			if err != nil {
				unknown = true
				continue
			}
			if matched && negated {
				return true, unknown
			}
			positive = positive || (matched && !negated)
		}
		if !positive {
			return true, unknown
		}
	}
	return false, unknown
}

func splitLine(line string) ([]string, bool) {
	var fields []string
	var word strings.Builder
	quoted, started := false, false
	flush := func() {
		if started {
			fields = append(fields, word.String())
			word.Reset()
			started = false
		}
	}
	for _, c := range line {
		switch {
		case c == '"':
			quoted, started = !quoted, true
		case c == '#' && !quoted:
			flush()
			return normalizeEquals(fields), true
		case (c == ' ' || c == '\t') && !quoted:
			flush()
		default:
			word.WriteRune(c)
			started = true
		}
	}
	if quoted {
		return nil, false
	}
	flush()
	return normalizeEquals(fields), true
}

func normalizeEquals(fields []string) []string {
	if len(fields) == 0 {
		return fields
	}
	if index := strings.IndexByte(fields[0], '='); index >= 0 {
		key, first := fields[0][:index], fields[0][index+1:]
		if key == "" {
			return fields
		}
		fields[0] = key
		if first != "" {
			fields = append(fields[:1], append([]string{first}, fields[1:]...)...)
		}
	} else if len(fields) > 1 && strings.HasPrefix(fields[1], "=") {
		first := strings.TrimPrefix(fields[1], "=")
		if first == "" {
			fields = append(fields[:1], fields[2:]...)
		} else {
			fields[1] = first
		}
	}
	return fields
}
