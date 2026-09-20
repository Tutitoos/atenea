package sshinventory

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrUnresolved means the selected host uses syntax that cannot be
	// evaluated without OpenSSH's dynamic configuration passes.
	ErrUnresolved = errors.New("ssh inventory: selected host configuration unresolved")
	// ErrChanged means configuration changed during static resolution.
	ErrChanged = errors.New("ssh inventory: configuration changed during resolution")
)

// Selection is a provisional, side-effect-free reading of a selected alias.
// It is not a verified target identity or permission to probe/connect.
type Selection struct {
	Alias         string
	HostName      string
	User          string
	Port          uint16
	HostKeyAlias  string
	ProxyJump     string
	ProxyCommand  string
	IdentityFiles []string
	Snapshot      string
	Sources       []Diagnostic
}

// RevalidateSelection rereads the same config roots and rejects a stale or
// modified selected alias. It must run again immediately before execution;
// a successful check alone does not authorize a connection.
func RevalidateSelection(userConfig, systemConfig string, selected Selection) error {
	if selected.Alias == "" || selected.Snapshot == "" {
		return ErrUnresolved
	}
	current, err := ResolveStatic(userConfig, systemConfig, selected.Alias)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, selected) {
		return ErrChanged
	}
	return nil
}

type resolver struct {
	alias  string
	result Selection
	set    map[string]bool
	active map[string]bool
	files  int
	bytes  int
}

// ResolveStatic applies supported Host and Include sections with OpenSSH's
// first-value-wins rule. It rejects all Match conditions except Match all,
// canonicalization, and target-changing tokens it cannot evaluate exactly.
// No helper, network call, or ssh -G invocation occurs here.
func ResolveStatic(userConfig, systemConfig, alias string) (Selection, error) {
	if !concrete(alias) {
		return Selection{}, ErrUnresolved
	}
	before, err := Scan(userConfig, systemConfig)
	if err != nil {
		return Selection{}, err
	}
	for _, diagnostic := range before.Diagnostics {
		if diagnostic.Code != "match_requires_selected_resolution" {
			return Selection{}, fmt.Errorf("%w: %s at %s:%d", ErrUnresolved, diagnostic.Code, diagnostic.Source, diagnostic.Line)
		}
	}
	r := &resolver{alias: alias, result: Selection{Alias: alias, HostName: alias, Port: 22}, set: make(map[string]bool), active: make(map[string]bool)}
	for index, root := range []string{userConfig, systemConfig} {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return Selection{}, err
		}
		includeRoot, err := includeRootForConfig(abs, index == 0)
		if err != nil {
			return Selection{}, err
		}
		if err := r.file(abs, includeRoot, index == 0, 0, true); err != nil {
			return Selection{}, err
		}
	}
	after, err := Scan(userConfig, systemConfig)
	if err != nil {
		return Selection{}, err
	}
	if before.Snapshot != after.Snapshot {
		return Selection{}, ErrChanged
	}
	r.result.Snapshot = after.Snapshot
	return r.result, nil
}

func (r *resolver) file(path, includeRoot string, user bool, depth int, optional bool) error {
	if depth > maxDepth || r.files >= maxFiles {
		return fmt.Errorf("%w: include limit", ErrUnresolved)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: config file unavailable", ErrUnresolved)
	}
	if r.active[canonical] {
		return fmt.Errorf("%w: include cycle", ErrUnresolved)
	}
	file, err := os.Open(canonical)
	if err != nil {
		return fmt.Errorf("%w: config file unavailable", ErrUnresolved)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maxBytes-r.bytes) {
		return fmt.Errorf("%w: config file limit", ErrUnresolved)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes-r.bytes)+1))
	if err != nil || len(data) > maxBytes-r.bytes {
		return fmt.Errorf("%w: config file limit", ErrUnresolved)
	}
	r.files++
	r.bytes += len(data)
	r.active[canonical] = true
	defer delete(r.active, canonical)

	selected := true
	reader := bufio.NewScanner(strings.NewReader(string(data)))
	reader.Buffer(make([]byte, 4096), maxLine+1)
	for line := 1; reader.Scan(); line++ {
		fields, valid := splitLine(reader.Text())
		if !valid {
			return fmt.Errorf("%w: syntax at %s:%d", ErrUnresolved, path, line)
		}
		if len(fields) == 0 {
			continue
		}
		key, args := strings.ToLower(fields[0]), fields[1:]
		switch key {
		case "host":
			if len(args) == 0 {
				return fmt.Errorf("%w: empty Host", ErrUnresolved)
			}
			blocked, unknown := evaluate([]condition{{patterns: args}}, r.alias)
			if unknown {
				return fmt.Errorf("%w: invalid Host pattern", ErrUnresolved)
			}
			selected = !blocked
		case "match":
			if len(args) == 1 && strings.EqualFold(args[0], "all") {
				selected = true
			} else {
				return fmt.Errorf("%w: Match at %s:%d", ErrUnresolved, path, line)
			}
		case "include":
			if !selected {
				continue
			}
			if len(args) == 0 {
				return fmt.Errorf("%w: empty Include", ErrUnresolved)
			}
			for _, pattern := range args {
				pattern, supported := includePatternPath(pattern, includeRoot, user)
				if !supported {
					return fmt.Errorf("%w: dynamic Include at %s:%d", ErrUnresolved, path, line)
				}
				paths, err := filepath.Glob(pattern)
				if err != nil {
					return fmt.Errorf("%w: invalid Include glob", ErrUnresolved)
				}
				sort.Strings(paths)
				for _, nested := range paths {
					if err := r.file(nested, includeRoot, user, depth+1, true); err != nil {
						return err
					}
				}
			}
		default:
			if selected {
				if err := r.option(key, args, reader.Text(), path, line); err != nil {
					return err
				}
			}
		}
	}
	if reader.Err() != nil {
		return fmt.Errorf("%w: line limit", ErrUnresolved)
	}
	return nil
}

func (r *resolver) option(key string, args []string, raw, path string, line int) error {
	if key == "proxycommand" {
		// Rejoining quoted shell words would change the user's route. The
		// first version supports only unquoted, space-separated commands.
		if strings.ContainsAny(raw, "\"'\\") {
			return fmt.Errorf("%w: quoted ProxyCommand", ErrUnresolved)
		}
		if len(args) == 0 {
			return fmt.Errorf("%w: empty ProxyCommand", ErrUnresolved)
		}
		args = []string{strings.Join(args, " ")}
	}
	if len(args) != 1 && key != "identityfile" {
		return fmt.Errorf("%w: unsupported option syntax at %s:%d", ErrUnresolved, path, line)
	}
	if key == "identityfile" {
		if len(args) != 1 {
			return fmt.Errorf("%w: IdentityFile syntax", ErrUnresolved)
		}
		r.result.IdentityFiles = append(r.result.IdentityFiles, args[0])
		return nil
	}
	value := args[0]
	if r.set[key] {
		return nil
	}
	if (key == "proxyjump" && r.set["proxycommand"]) || (key == "proxycommand" && r.set["proxyjump"]) {
		return nil // OpenSSH uses whichever of these is specified first.
	}
	switch key {
	case "hostname":
		if strings.Contains(value, "%") || value == "" {
			return fmt.Errorf("%w: dynamic HostName", ErrUnresolved)
		}
		r.result.HostName = value
	case "user":
		if strings.Contains(value, "%") || value == "" {
			return fmt.Errorf("%w: dynamic User", ErrUnresolved)
		}
		r.result.User = value
	case "port":
		parsed, err := strconv.ParseUint(value, 10, 16)
		if err != nil || parsed == 0 {
			return fmt.Errorf("%w: invalid Port", ErrUnresolved)
		}
		r.result.Port = uint16(parsed)
	case "hostkeyalias":
		if strings.Contains(value, "%") {
			return fmt.Errorf("%w: dynamic HostKeyAlias", ErrUnresolved)
		}
		r.result.HostKeyAlias = value
	case "proxyjump":
		r.result.ProxyJump = value
	case "proxycommand":
		r.result.ProxyCommand = value // recorded only; never executed here
	case "canonicalizehostname":
		if value != "no" && value != "none" {
			return fmt.Errorf("%w: hostname canonicalization", ErrUnresolved)
		}
	default:
		// Retain no implied safety for other options. A later probe plan must
		// explicitly inspect the complete selected config before connecting.
		return fmt.Errorf("%w: unsupported option %s at %s:%d", ErrUnresolved, key, path, line)
	}
	r.set[key] = true
	r.result.Sources = append(r.result.Sources, Diagnostic{Source: path, Line: line, Code: key})
	return nil
}
