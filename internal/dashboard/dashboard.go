// Package dashboard resolves and opens the optional web UI attached to an MCP
// declaration. It intentionally does not start processes: an MCP lifecycle
// and opening its browser are separate operator decisions.
package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
)

const (
	managedBegin = "# BEGIN ATENEA MANAGED HOSTS"
	managedEnd   = "# END ATENEA MANAGED HOSTS"
)

var (
	// ErrNotFound means no configured MCP has the requested id.
	ErrNotFound = errors.New("dashboard MCP not found")
	// ErrNotDeclared means the MCP exists but does not expose a dashboard.
	ErrNotDeclared = errors.New("MCP has no dashboard")
	// ErrAliasConflict means a dashboard alias collides with an unmanaged entry.
	ErrAliasConflict = errors.New("dashboard alias conflicts with an unmanaged hosts entry")
	// ErrInvalidHosts means the managed hosts block cannot be parsed safely.
	ErrInvalidHosts = errors.New("invalid managed hosts block")
	// ErrInvalidAlias means a dashboard alias is not a name a hosts file can
	// carry on a line of its own.
	ErrInvalidAlias = errors.New("dashboard alias is not a usable hostname")
)

// aliasPattern is the vocabulary an alias may use, and it is the same one
// pkg/contract already demands of agent and capability ids.
//
// It is checked here because of what PlanHosts does with the value: it
// composes the managed line as "127.0.0.1 " + alias, and the alias is
// literally the id out of the settings file. The upstream validation rejects
// only the empty id and the one containing a dot, so nothing stopped an id
// carrying a space, a tab or a newline -- and a newline in it appends whole
// lines of its author's choosing to /etc/hosts, which is the first thing every
// name lookup on the machine consults. An alias that cannot be a hostname is
// refused before the line exists rather than after it is written.
var aliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Entry is the stable public mapping used by the CLI and status renderers.
type Entry struct {
	ID    string
	URL   string
	Alias string
}

// KivgraphID is the stable id used for the optional graph viewer dashboard.
const KivgraphID = "kivgraph"

// Resolve returns one configured dashboard by MCP id.
func Resolve(servers []config.MCPServer, id string) (Entry, error) {
	for _, server := range servers {
		if server.ID != id {
			continue
		}
		if server.Dashboard == "" {
			return Entry{}, fmt.Errorf("%w: %s", ErrNotDeclared, id)
		}
		return Entry{ID: server.ID, URL: server.Dashboard, Alias: server.ID}, nil
	}
	return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// ResolveConfig includes dashboards owned by an orchestrator as well as
// ordinary [[mcp_server]] declarations. Kivgraph's MCP endpoint is normally
// authenticated HTTP in 0.9.2 (with stdio as an explicit alternative), so its
// viewer is represented by the orchestrator configuration instead of a second
// fake MCP declaration.
func ResolveConfig(cfg config.Config, id string) (Entry, error) {
	entry, err := Resolve(cfg.MCPServers, id)
	if err == nil || id != KivgraphID {
		return entry, err
	}
	if cfg.Orchestrator.Kivgraph.Dashboard == "" {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotDeclared, id)
	}
	return Entry{
		ID:    KivgraphID,
		URL:   cfg.Orchestrator.Kivgraph.Dashboard,
		Alias: KivgraphID,
	}, nil
}

// All returns configured dashboards in declaration order and rejects duplicate
// aliases defensively, including callers that construct Config values in code.
func All(servers []config.MCPServer) ([]Entry, error) {
	seen := make(map[string]string)
	out := make([]Entry, 0, len(servers))
	for _, server := range servers {
		if server.Dashboard == "" {
			continue
		}
		if previous, ok := seen[server.ID]; ok {
			return nil, fmt.Errorf("%w: %s and %s use %s", ErrAliasConflict, previous, server.ID, server.ID)
		}
		seen[server.ID] = server.ID
		out = append(out, Entry{ID: server.ID, URL: server.Dashboard, Alias: server.ID})
	}
	return out, nil
}

// AllConfig is the complete dashboard catalog, including Kivgraph's separate
// read-only viewer when one is configured.
func AllConfig(cfg config.Config) ([]Entry, error) {
	entries, err := All(cfg.MCPServers)
	if err != nil {
		return nil, err
	}
	if cfg.Orchestrator.Kivgraph.Dashboard == "" {
		return entries, nil
	}
	for _, entry := range entries {
		if entry.Alias == KivgraphID {
			return nil, fmt.Errorf("%w: %s", ErrAliasConflict, KivgraphID)
		}
	}
	return append(entries, Entry{ID: KivgraphID, URL: cfg.Orchestrator.Kivgraph.Dashboard, Alias: KivgraphID}), nil
}

// Check performs a harmless GET and considers any HTTP response evidence that
// the web process is listening. A 4xx dashboard route is still an accessible
// dashboard; transport errors and 5xx responses are reported to the caller.
func Check(ctx context.Context, rawURL string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("dashboard returned %s", resp.Status)
	}
	return nil
}

// Launcher is injectable so opening a browser is testable without launching
// a real application.
type Launcher interface {
	Open(string) error
}

type commandLauncher struct{}

// DefaultLauncher uses the native opener for the current platform.
func DefaultLauncher() Launcher { return commandLauncher{} }

func (commandLauncher) Open(rawURL string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	case "linux":
		name, args = "xdg-open", []string{rawURL}
	default:
		return fmt.Errorf("unsupported browser platform %s", runtime.GOOS)
	}
	return exec.Command(name, args...).Run()
}

// HostsPath is injectable for tests. Production callers should pass
// "/etc/hosts" explicitly rather than hiding a privileged path in tests.
const HostsPath = "/etc/hosts"

// SerenaDashboardBasePort is Serena's documented dashboard API base port.
// Serena chooses the first free port from this base for every process.
const SerenaDashboardBasePort = 0x5EDA

// DiscoverSerena finds the dashboard whose active project is root. Serena's
// dashboard port is process-local and cannot be represented by one static MCP
// URL when Atenea keeps one Serena process per repository.
func DiscoverSerena(ctx context.Context, root string) (Entry, error) {
	return discoverSerena(ctx, root, SerenaDashboardBasePort, 128)
}

func discoverSerena(ctx context.Context, root string, base, count int) (Entry, error) {
	want := filepath.Clean(root)
	client := &http.Client{Timeout: 120 * time.Millisecond}
	for port := base; port < base+count; port++ {
		apiURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/get_config_overview"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var overview struct {
			ActiveProject struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"active_project"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&overview)
		_ = resp.Body.Close()
		if decodeErr != nil || filepath.Clean(overview.ActiveProject.Path) != want {
			continue
		}
		return Entry{
			ID:    "serena",
			Alias: "serena",
			URL:   "http://127.0.0.1:" + strconv.Itoa(port) + "/dashboard/index.html",
		}, nil
	}
	return Entry{}, fmt.Errorf("%w: no active Serena dashboard for %s", ErrNotDeclared, root)
}

// HostsPlan contains a proposed managed block replacement.
type HostsPlan struct {
	Content string
	Changed bool
}

// PlanHosts creates an idempotent managed block while preserving all foreign
// lines. It never reads or writes the real hosts file by itself.
func PlanHosts(existing string, entries []Entry, removeObsolete bool) (HostsPlan, error) {
	managed, foreign, err := splitManaged(existing)
	if err != nil {
		return HostsPlan{}, err
	}
	aliases := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !aliasPattern.MatchString(entry.Alias) {
			return HostsPlan{}, fmt.Errorf("%w: %q", ErrInvalidAlias, entry.Alias)
		}
		if aliases[entry.Alias] {
			return HostsPlan{}, fmt.Errorf("%w: %s", ErrAliasConflict, entry.Alias)
		}
		aliases[entry.Alias] = true
		if hostLineClaimsAlias(foreign, entry.Alias) {
			return HostsPlan{}, fmt.Errorf("%w: %s", ErrAliasConflict, entry.Alias)
		}
	}

	managedLines := make([]string, 0, len(managed)+len(entries))
	if !removeObsolete {
		managedLines = append(managedLines, managed...)
	}
	// A configured alias is authoritative. Remove its old managed line before
	// adding the normalized local-hosts form, while leaving unrelated managed
	// entries in place unless --remove-obsolete was requested.
	for _, entry := range entries {
		filtered := managedLines[:0]
		for _, line := range managedLines {
			if !hostLineClaimsAlias([]string{line}, entry.Alias) {
				filtered = append(filtered, line)
			}
		}
		managedLines = filtered
		managedLines = append(managedLines, "127.0.0.1 "+entry.Alias)
	}
	lines := make([]string, 0, len(foreign)+len(managedLines)+2)
	lines = append(lines, foreign...)
	if len(managedLines) > 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, managedBegin)
		sort.Strings(managedLines)
		lines = append(lines, managedLines...)
		lines = append(lines, managedEnd)
	}
	content := strings.Join(lines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return HostsPlan{Content: content, Changed: content != existing}, nil
}

func splitManaged(existing string) (managed, foreign []string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(existing))
	inManaged := false
	for scanner.Scan() {
		line := scanner.Text()
		switch line {
		case managedBegin:
			if inManaged {
				return nil, nil, ErrInvalidHosts
			}
			inManaged = true
			continue
		case managedEnd:
			if !inManaged {
				return nil, nil, ErrInvalidHosts
			}
			inManaged = false
			continue
		}
		if inManaged {
			managed = append(managed, line)
		} else {
			foreign = append(foreign, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if inManaged {
		return nil, nil, ErrInvalidHosts
	}
	return managed, foreign, nil
}

func hostLineClaimsAlias(lines []string, alias string) bool {
	for _, line := range lines {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			if field == alias {
				return true
			}
		}
	}
	return false
}

// ReadHosts reads a caller-selected hosts file.
func ReadHosts(path string) (string, error) { return stringReadFile(path) }

// WriteHosts writes only after the caller explicitly chose the hosts command.
//
// Temporary file, fsync, rename, fsync of the directory -- the pattern
// checkpoint.Save, notebook.Clear, benchmark.atomicWrite and backup.Snapshot
// all follow. This one wrote straight over the destination with os.WriteFile,
// which truncates the file before it writes a byte of the replacement, and the
// destination is /etc/hosts: an interrupted write there does not lose a
// document, it leaves the machine resolving names against half a file until
// somebody notices. A rename is atomic, so every reader sees either the whole
// old hosts file or the whole new one.
func WriteHosts(path, content string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	// In the destination's own directory, because rename is atomic only within
	// one filesystem and /etc is not always the filesystem TMPDIR points at.
	tmp, err := os.CreateTemp(dir, ".atenea-hosts-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Removed on every path that does not reach the rename. After a successful
	// rename the name is already gone and this fails harmlessly.
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	// CreateTemp opens at 0600. The replacement has to carry the permissions
	// the destination already had, or a hosts file readable by every account
	// on the machine comes back owner-only and every lookup by an unprivileged
	// process stops seeing the managed names.
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	// The contents reach the disk before the name does. Renaming first would
	// publish an entry that a power cut could leave pointing at empty blocks,
	// which is the same truncated hosts file by another route.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func stringReadFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
