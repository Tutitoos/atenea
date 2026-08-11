// Package clientconfig reads the client configuration a team keeps in a
// repository and says what the project is asking for.
//
// It reads and it never runs. A `.mcp.json` arriving with a git clone is a
// list of commands somebody else wrote, and the whole value of Atenea sitting
// between a client and its backends disappears the moment those commands are
// launched because a file said so. So this package answers one question --
// what does this project expect to have available -- and Atenea answers it
// from its own registered providers, or says it cannot.
//
// The guarantee is structural rather than promised. A declaration's command,
// arguments, environment and URL are parsed and then dropped at this boundary:
// no type below carries them, so no caller downstream can execute what it was
// never handed. What survives is a name, a kind and where it was read from.
// A test asserts that, because a promise in a comment is the kind of guarantee
// that quietly stops being true.
package clientconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// maxFile caps what will be read from any one declaration file. A repository
// is untrusted input, and a parser with no ceiling is a way to spend all the
// memory on the machine by committing a large file.
const maxFile = 1 << 20 // 1 MiB

// Kind is what a project declared.
type Kind string

const (
	// KindServer is an MCP backend the project expects to be able to reach.
	KindServer Kind = "mcp server"
	// KindSkill is prose: instructions a client loads. Atenea has no
	// equivalent and does not pretend to -- they are reported so the reader
	// knows what else the repository carries.
	KindSkill Kind = "skill"
)

// Transport is how a declaration said its backend is reached. It is kept
// because it is a fact worth printing and cannot be acted on; the address
// itself is not kept.
type Transport string

const (
	// TransportLocal is a process the client would spawn. Claude Code calls
	// this "stdio" and opencode calls it "local"; one shape, two names.
	TransportLocal Transport = "local"
	// TransportRemote is a backend reached over the network.
	TransportRemote Transport = "remote"
	// TransportUnknown is a declaration that did not say.
	TransportUnknown Transport = "unspecified"
)

// Request is one thing a project asked for.
//
// There is deliberately no Command, Args, Env or URL field. This is the type
// the rest of Atenea sees, and what it cannot name it cannot run.
type Request struct {
	Kind Kind
	// Name is what the project called it.
	Name string
	// Transport is how it said the backend is reached, for KindServer.
	Transport Transport
	// Enabled is false when the project's own client settings switched this
	// declaration off. A disabled server is still reported: a reader
	// comparing two repositories wants to see that the declaration exists.
	Enabled bool
	// Detail is a skill's description, trimmed to one line. Prose, never a
	// command.
	Detail string
	// Source is the file it was read from, relative to the repository root.
	Source string
}

// Reading is everything one repository declares.
type Reading struct {
	// Root is the repository the files were read from.
	Root string
	// Files are the declaration files found, relative to Root, sorted.
	Files []string
	// Requests are what they asked for, sorted by kind then name.
	Requests []Request
	// Unreadable names files that exist and could not be parsed, with the
	// reason. A malformed file is reported rather than skipped: silence here
	// would read as "the project asks for nothing", which is a different
	// answer and the wrong one.
	Unreadable []string
}

// Servers returns the MCP declarations only.
func (r Reading) Servers() []Request {
	out := make([]Request, 0, len(r.Requests))
	for _, req := range r.Requests {
		if req.Kind == KindServer {
			out = append(out, req)
		}
	}
	return out
}

// Skills returns the skill declarations only.
func (r Reading) Skills() []Request {
	out := make([]Request, 0, len(r.Requests))
	for _, req := range r.Requests {
		if req.Kind == KindSkill {
			out = append(out, req)
		}
	}
	return out
}

// Empty reports whether the repository carries no client configuration at all.
func (r Reading) Empty() bool { return len(r.Files) == 0 && len(r.Unreadable) == 0 }

// identity is what makes two declarations the same thing: the kind, and the
// name with a client's packaging decoration removed.
//
// One function, because two screens answering "how much is this repository
// asking for" with two different numbers is the failure this whole report is
// written to avoid, and it reappears the moment the count is spelled twice.
func identity(request Request) string {
	return string(request.Kind) + "\x00" + normalize(request.Name)
}

// Asks counts the distinct things the repository is asking for. Declarations
// of one backend to two clients count once.
func (r Reading) Asks() int {
	seen := make(map[string]struct{}, len(r.Requests))
	for _, request := range r.Requests {
		seen[identity(request)] = struct{}{}
	}
	return len(seen)
}

// Read collects what the client configuration in root declares.
//
// A repository carrying none is not an error: most do not, and the honest
// answer to "what does this project ask for" is then "nothing".
func Read(root string) (Reading, error) {
	if root == "" {
		return Reading{}, contract.Fail(contract.FailureInvalidInput,
			"client config: no repository root to read")
	}
	out := Reading{Root: root}

	// Claude Code: servers in .mcp.json, switched on or off by the settings
	// files beside them, and skills as directories of prose.
	enabled, disabled := claudeToggles(root, &out)
	readClaudeServers(root, enabled, disabled, &out)
	readSkills(root, filepath.Join(".claude", "skills"), &out)

	// opencode: servers inline in the project's own config file, and skills
	// in the same shape as Claude Code's.
	readOpenCodeServers(root, &out)
	readSkills(root, filepath.Join(".opencode", "skills"), &out)

	sort.Strings(out.Files)
	out.Files = slices.Compact(out.Files)
	sort.Strings(out.Unreadable)
	sort.Slice(out.Requests, func(i, j int) bool {
		if out.Requests[i].Kind != out.Requests[j].Kind {
			return out.Requests[i].Kind < out.Requests[j].Kind
		}
		return out.Requests[i].Name < out.Requests[j].Name
	})
	return out, nil
}

// readFile reads one declaration file, capped. A missing file is not an error
// here: absence is the normal case for every one of them.
func readFile(path string) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxFile {
		return nil, false, fmt.Errorf("larger than %d bytes", maxFile)
	}
	return raw, true, nil
}

// claudeSettings is the sliver of Claude Code's settings files this reads.
// Everything else in them -- hooks, permissions, model choices -- is the
// client's business and none of Atenea's.
type claudeSettings struct {
	Enabled  []string `json:"enabledMcpjsonServers"`
	Disabled []string `json:"disabledMcpjsonServers"`
}

// claudeToggles reads which of .mcp.json's servers the project switched on.
//
// Both files are read, the local one last, because that is the order the
// client applies them.
func claudeToggles(root string, out *Reading) (enabled, disabled []string) {
	for _, name := range []string{"settings.json", "settings.local.json"} {
		rel := filepath.Join(".claude", name)
		raw, found, err := readFile(filepath.Join(root, rel))
		if err != nil {
			out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		if !found {
			continue
		}
		var settings claudeSettings
		if err := json.Unmarshal(raw, &settings); err != nil {
			out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		out.Files = append(out.Files, rel)
		enabled = append(enabled, settings.Enabled...)
		disabled = append(disabled, settings.Disabled...)
	}
	return enabled, disabled
}

// mcpFile is the shape of .mcp.json. The values are decoded into a struct that
// keeps only the transport: json.Unmarshal fills what it is given and throws
// the rest away, so the command never exists as a Go value at all.
type mcpFile struct {
	Servers map[string]declaration `json:"mcpServers"`
}

// declaration is one entry, reduced to what may safely leave this file.
//
// `type` and `url` are the only keys read. `command`, `args` and `env` are not
// fields here on purpose: they are in the JSON, they are parsed over, and they
// are dropped. The presence of a url is turned into a bool rather than kept,
// so not even an address survives to be dialed.
type declaration struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (d declaration) transport() Transport {
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "stdio", "local":
		return TransportLocal
	case "http", "sse", "remote":
		return TransportRemote
	}
	if d.URL != "" {
		return TransportRemote
	}
	return TransportUnknown
}

func readClaudeServers(root string, enabled, disabled []string, out *Reading) {
	const rel = ".mcp.json"
	raw, found, err := readFile(filepath.Join(root, rel))
	if err != nil {
		out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
		return
	}
	if !found {
		return
	}
	var parsed mcpFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
		return
	}
	out.Files = append(out.Files, rel)
	for name, decl := range parsed.Servers {
		// A server listed in neither toggle is on: that is the client's own
		// default for a project file, and reporting it as off would invent a
		// state the project never asked for.
		on := !slices.Contains(disabled, name)
		if len(enabled) > 0 && !slices.Contains(enabled, name) {
			on = false
		}
		out.Requests = append(out.Requests, Request{
			Kind:      KindServer,
			Name:      name,
			Transport: decl.transport(),
			Enabled:   on,
			Source:    rel,
		})
	}
}

// openCodeFile is the sliver of opencode's project config this reads.
type openCodeFile struct {
	MCP map[string]openCodeServer `json:"mcp"`
}

type openCodeServer struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

func readOpenCodeServers(root string, out *Reading) {
	// Both names, because opencode accepts both and a project that picked the
	// commented one is not a project that declared nothing.
	for _, rel := range []string{"opencode.json", "opencode.jsonc"} {
		raw, found, err := readFile(filepath.Join(root, rel))
		if err != nil {
			out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		if !found {
			continue
		}
		var parsed openCodeFile
		if err := json.Unmarshal(stripComments(raw), &parsed); err != nil {
			out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		out.Files = append(out.Files, rel)
		for name, server := range parsed.MCP {
			on := server.Enabled == nil || *server.Enabled
			out.Requests = append(out.Requests, Request{
				Kind:      KindServer,
				Name:      name,
				Transport: declaration{Type: server.Type, URL: server.URL}.transport(),
				Enabled:   on,
				Source:    rel,
			})
		}
	}
}

// stripComments removes // line comments so a .jsonc file parses. It respects
// string literals, because a URL contains "//" and eating it would turn a
// readable file into a parse error blamed on the project.
func stripComments(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			if i < len(raw) {
				out = append(out, '\n')
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

// readSkills collects the skills under dir. Only the frontmatter is read: a
// skill is prose for a model, and the first lines are the only part with a
// shape.
func readSkills(root, dir string, out *Reading) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", dir, err))
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rel := filepath.Join(dir, entry.Name(), "SKILL.md")
		raw, found, err := readFile(filepath.Join(root, rel))
		if err != nil {
			out.Unreadable = append(out.Unreadable, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		if !found {
			continue
		}
		out.Files = append(out.Files, rel)
		out.Requests = append(out.Requests, Request{
			Kind:    KindSkill,
			Name:    entry.Name(),
			Enabled: true,
			Detail:  frontmatterDescription(raw),
			Source:  rel,
		})
	}
}

// frontmatterDescription pulls the description out of a skill's frontmatter
// and flattens it to one line. Folded YAML scalars are common here, and a
// description printed over four lines would bury the list it sits in.
func frontmatterDescription(raw []byte) string {
	text := string(raw)
	if !strings.HasPrefix(text, "---") {
		return ""
	}
	end := strings.Index(text[3:], "\n---")
	if end < 0 {
		return ""
	}
	var collected []string
	inDescription := false
	for _, line := range strings.Split(text[3:end+3], "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "description:"):
			inDescription = true
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			// A folded or literal block scalar carries its text on the
			// following lines instead.
			if value != "" && value != ">" && value != "|" && value != ">-" && value != "|-" {
				collected = append(collected, value)
			}
		case inDescription:
			// Another key at the top level ends the description.
			if trimmed == "" || (!strings.HasPrefix(line, " ") && strings.Contains(trimmed, ":")) {
				inDescription = false
				continue
			}
			collected = append(collected, trimmed)
		}
	}
	return strings.Join(collected, " ")
}
