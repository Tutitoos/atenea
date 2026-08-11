// Package wrap launches a client with MCP configuration Atenea generated and
// checked, instead of configuration somebody typed once and never read again.
//
// The problem it exists for is not that client config is hard to write. It is
// that a written config is a claim nobody checks. A client reads it, tells a
// model what it can do, and discovers the truth only when a tool call fails --
// by which point the model has already planned around a tool that was never
// there. Measured on the machine this was written on: five MCP servers
// declared in one client's config, two of them dead, both reported as a
// warning in a list nobody runs, and neither dead long enough ago for anyone
// to say when it happened.
//
// So the generated config carries only servers that answered a handshake
// moments before the client started, and the ones that did not are named on
// the way past. Nothing is written to disk. The payload lives in one
// environment variable for the lifetime of one child process, which is what
// makes this safe to try: a client launched without wrap is a client with
// exactly the configuration it had before.
package wrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/mcpprobe"
)

// Checked is one declared server and what happened when it was checked.
//
// Server is the value that was actually probed, not the settings block it
// came from, and the payload is rendered from this same value. That is
// deliberate: it makes it structurally impossible to hand a client an
// endpoint different from the one that answered.
type Checked struct {
	Server mcpprobe.Server
	Result mcpprobe.Result
	// Raw is whether this entry carries expose = "raw", and it is here for
	// the report rather than the payload. The two kinds of held look
	// identical from outside -- both absent from every payload -- but only
	// a raw one is re-offered, as raw.<id>.<tool>. A backend held because
	// capabilities run on it has no tool surface of its own at all.
	Raw bool
}

// Plan is the decision, made before the client is launched: who goes into the
// payload and who does not.
type Plan struct {
	Declared []Checked
	Refused  []Checked
	// Held are the backends Atenea answers for. They are checked and
	// reported like the others and then deliberately kept out of every
	// payload: a client pointed at one would reach it directly, under no
	// allow list and no effect check, which is the budget being routed
	// around by the very command that is supposed to apply it.
	//
	// Two kinds qualify, and until 2026-08-09 only the first did. A raw
	// backend is held because Atenea filters it. A backend behind a
	// capability is held for the same reason and was being handed over
	// anyway: `serena` and `codebase-memory` carry all eight capabilities
	// on this machine, and wrap was putting both in the payload, so the
	// client reached the funnel's own backends around the funnel. The
	// command that exists to point clients at the core was pointing them
	// past it.
	Held []Checked
}

// toProbe is kept as a name for what this conversion means here, and delegates
// to the declaration's own method: the status screen needs the same conversion
// and two copies of the URL-or-command rule would let a screen and a probe
// describe one server differently.
func toProbe(s config.MCPServer) mcpprobe.Server { return s.Probe() }

// Check probes every declared server and sorts them into the three piles.
//
// The probes run together because they are independent and a client launch
// waits on all of them: checking eleven servers one after another would add
// their timeouts, and the whole point is that this happens before a person
// has finished reading the line that says it is happening.
//
// served names the backends some capability implementation runs against.
// It is passed in rather than derived here because the answer lives in the
// implementation catalog, and a backend's own block cannot know whether
// anybody points at it.
func Check(ctx context.Context, servers []config.MCPServer, served map[string]bool) Plan {
	held := make([]bool, len(servers))
	raw := make([]bool, len(servers))
	for i, s := range servers {
		raw[i] = s.Expose == config.ExposeRaw
		held[i] = raw[i] || served[s.ID]
	}
	probes := make([]mcpprobe.Server, len(servers))
	results := make([]mcpprobe.Result, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		probes[i] = toProbe(s)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = mcpprobe.Probe(ctx, probes[i])
		}()
	}
	wg.Wait()

	var plan Plan
	for i := range probes {
		entry := Checked{Server: probes[i], Result: results[i], Raw: raw[i]}
		switch {
		// Held before reachable: a raw backend that is down is still one
		// Atenea owns, and putting it in Refused would invite the reading
		// that the client should be told about it after all.
		case held[i]:
			plan.Held = append(plan.Held, entry)
		case results[i].OK:
			plan.Declared = append(plan.Declared, entry)
		default:
			plan.Refused = append(plan.Refused, entry)
		}
	}
	// Sorted so two runs against the same machine produce the same payload
	// and the same report. A diff between two wrap runs should be a change in
	// the world, never a change in map iteration order.
	sortByID(plan.Declared)
	sortByID(plan.Refused)
	sortByID(plan.Held)
	return plan
}

func sortByID(entries []Checked) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Server.ID < entries[j].Server.ID })
}

// openCodeServer is one entry of OpenCode's `mcp` object. The two shapes are
// discriminated by Type, and the unused half must stay absent rather than
// empty: OpenCode merges this over the user's own config key by key, so a
// zero value here would overwrite a real value there.
type openCodeServer struct {
	Type        string            `json:"type"`
	URL         string            `json:"url,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

// Core is the entry every payload carries: the client's one door to Atenea.
//
// It is not a settings block and never was. Until 2026-08-09 no payload named
// Atenea at all, so `atenea wrap` handed a client every backend and left out
// the core -- measured, five servers and no door. The command is built from
// the running binary rather than a configured path so that a wrap can only
// ever point at the Atenea that performed it.
type Core struct {
	ID      string
	Command []string
}

// OpenCodePayload renders what goes in OPENCODE_CONFIG_CONTENT.
//
// Only servers that answered are in it. A refused server is left out rather
// than disabled, and that asymmetry is the safety property: OpenCode
// deep-merges this over the user's own config, so an absent key leaves
// whatever they already had in place. Atenea declining to vouch for a server
// must never be the reason a client loses one that was working for them.
//
// The core is the one exception to "only what answered": it is the process
// doing the answering. Leaving it out on a bad probe would hand the client a
// config with no door at all, which is worse than the door it came with.
func (p Plan) OpenCodePayload(core Core) (string, error) {
	servers := make(map[string]openCodeServer, len(p.Declared)+1)
	servers[core.ID] = openCodeServer{Type: "local", Command: core.Command}
	for _, entry := range p.Declared {
		s := entry.Server
		if s.URL != "" {
			servers[s.ID] = openCodeServer{Type: "remote", URL: s.URL}
			continue
		}
		servers[s.ID] = openCodeServer{Type: "local", Command: s.Command, Environment: s.Env}
	}
	// encoding/json sorts map keys, so this is stable without further work.
	out, err := json.Marshal(struct {
		MCP map[string]openCodeServer `json:"mcp"`
	}{MCP: servers})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// claudeServer is one entry of Claude Code's `mcpServers` object.
//
// The shape differs from OpenCode's in the one way that stops a shared
// renderer from working: Claude splits the executable from its arguments where
// OpenCode takes a single list. Two clients, two shapes, two functions --
// rather than one renderer with a switch in it, which is where a settings
// file's vocabulary starts leaking into a client's.
type claudeServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
}

// ClaudePayload renders the JSON handed to `claude --mcp-config`.
//
// Claude Code merges what arrives here with everything else it resolves --
// project `.mcp.json`, the user file, plugins -- unless `--strict-mcp-config`
// is passed, and wrap never passes it. That is the same asymmetry the OpenCode
// payload is built on: Atenea declining to vouch for a server must never be
// the reason a client loses one that was working. Where a name collides the
// whole entry from one source wins rather than the two merging field by field,
// and under either outcome the client still has a server under that name.
//
// `type` is written even for stdio, where the documentation makes it optional.
// An entry carrying a url and no type is a hard error in Claude Code -- it
// reads a typeless entry as stdio and skips it -- so being explicit on both
// costs a few bytes and removes the class.
func (p Plan) ClaudePayload(core Core) (string, error) {
	if len(core.Command) == 0 {
		return "", fmt.Errorf("the core has no command; there is nothing to point the client at")
	}
	servers := make(map[string]claudeServer, len(p.Declared)+1)
	servers[core.ID] = claudeServer{Type: "stdio", Command: core.Command[0], Args: core.Command[1:]}
	for _, entry := range p.Declared {
		s := entry.Server
		if s.URL != "" {
			servers[s.ID] = claudeServer{Type: "http", URL: s.URL}
			continue
		}
		// The settings file admits exactly one of url or command and no
		// empty argument inside it, so the head is here to be taken.
		servers[s.ID] = claudeServer{
			Type: "stdio", Command: s.Command[0], Args: s.Command[1:], Env: s.Env,
		}
	}
	out, err := json.Marshal(struct {
		MCPServers map[string]claudeServer `json:"mcpServers"`
	}{MCPServers: servers})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ClaudeArgs is the payload with the flag that carries it, kept together so
// the two cannot drift: the JSON shape is Claude's because the flag is.
func (p Plan) ClaudeArgs(core Core) ([]string, error) {
	payload, err := p.ClaudePayload(core)
	if err != nil {
		return nil, err
	}
	return []string{"--mcp-config", payload}, nil
}

// CodexArgs renders the `-c` overrides handed to codex.
//
// One override per server, never one for the whole table. `-c
// mcp_servers={...}` would replace the map, so a user with their own servers
// in `~/.codex/config.toml` would lose every one of them for the length of the
// session; `-c mcp_servers.<id>={...}` sets a single key and leaves the rest
// of the table alone. Measured against codex 0.146.0: an injected server was
// listed beside the four already declared, and the config file's hash did not
// move.
//
// The value is TOML rather than JSON -- an inline table, `=` not `:` -- so
// every string goes through an escaper instead of being concatenated.
func (p Plan) CodexArgs(core Core) ([]string, error) {
	if len(core.Command) == 0 {
		return nil, fmt.Errorf("the core has no command; there is nothing to point the client at")
	}
	args := make([]string, 0, 2*(len(p.Declared)+1))
	add := func(id string, s mcpprobe.Server) {
		args = append(args, "-c", "mcp_servers."+tomlKey(id)+"="+tomlTable(s))
	}
	add(core.ID, mcpprobe.Server{Command: core.Command})
	for _, entry := range p.Declared {
		add(entry.Server.ID, entry.Server)
	}
	return args, nil
}

// tomlTable renders one server as a TOML inline table.
func tomlTable(s mcpprobe.Server) string {
	if s.URL != "" {
		return "{url=" + tomlString(s.URL) + "}"
	}
	var b strings.Builder
	b.WriteString("{command=")
	b.WriteString(tomlString(s.Command[0]))
	if len(s.Command) > 1 {
		b.WriteString(",args=[")
		for i, arg := range s.Command[1:] {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(tomlString(arg))
		}
		b.WriteByte(']')
	}
	if len(s.Env) > 0 {
		keys := make([]string, 0, len(s.Env))
		for k := range s.Env {
			keys = append(keys, k)
		}
		// Sorted for the same reason the piles are: two wraps of one
		// machine must produce the same argv, never map order.
		sort.Strings(keys)
		b.WriteString(",env={")
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(tomlKey(k))
			b.WriteByte('=')
			b.WriteString(tomlString(s.Env[k]))
		}
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return b.String()
}

// tomlString renders a TOML basic string.
//
// strconv.Quote is Go's grammar, not TOML's: it emits `\x00` for a byte TOML
// has no escape for, and codex would reject the whole override. The escapes
// TOML defines are written here and everything else non-printable becomes
// `\uXXXX`, which it also defines.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlKey writes a key bare where TOML allows it and quoted where it does not.
// Server ids cannot carry a dot -- the settings file refuses one, because an
// id is a segment of every tool name built from it -- but an environment
// variable's name arrives from the same file with no such promise.
func tomlKey(k string) string {
	bare := k != ""
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			bare = false
		}
	}
	if bare {
		return k
	}
	return tomlString(k)
}

// Report says what happened, in the words the operator can act on.
//
// It prints even when everything worked. A check whose output only appears on
// failure trains a reader to assume silence means it ran, and the failure
// this replaces was itself silent.
func (p Plan) Report(w io.Writer, client string) {
	total := len(p.Declared) + len(p.Refused) + len(p.Held)
	if total == 0 {
		// Not "unchanged" any more, and saying so would be the lie this
		// package was written to stop. A settings file with no backends
		// still has a running core with capabilities behind it, and the
		// payload carries the door to it either way.
		fmt.Fprintf(w, "wrap %s  no mcp_server declared in settings\n", client)
		fmt.Fprintf(w, "  %s gets atenea and keeps everything else it already declares.\n", client)
		return
	}
	fmt.Fprintf(w, "wrap %s  %d checked: %d declared, %d refused, %d held\n\n",
		client, total, len(p.Declared), len(p.Refused), len(p.Held))

	width := 0
	for _, group := range [][]Checked{p.Declared, p.Refused, p.Held} {
		for _, entry := range group {
			width = max(width, len(entry.Server.ID))
		}
	}
	for _, entry := range p.Declared {
		fmt.Fprintf(w, "  declared  %-*s  %-5s  %s%s\n",
			width, entry.Server.ID, entry.Server.Transport(), entry.Server.Where(), who(entry.Result))
	}
	for _, entry := range p.Refused {
		fmt.Fprintf(w, "  refused   %-*s  %-5s  %s\n",
			width, entry.Server.ID, entry.Server.Transport(), entry.Server.Where())
		// The reason goes on its own line rather than off the right edge.
		// It is the only part of this report that is not already in the
		// settings file, so it is the part that must not be clipped.
		fmt.Fprintf(w, "  %-*s            %v\n", width, "", entry.Result.Err)
	}
	for _, entry := range p.Held {
		fmt.Fprintf(w, "  held      %-*s  %-5s  %s%s\n",
			width, entry.Server.ID, entry.Server.Transport(), entry.Server.Where(), who(entry.Result))
		// Why it is not in the payload, at the row rather than in a
		// footnote: an operator comparing this against their client's tool
		// list needs the reason where the absence is.
		//
		// And the reason has to be the real one. Announcing raw.<id>.<tool>
		// for a backend that carries no expose sends that operator looking
		// for tools no tools/list will ever return: a claim this binary
		// cannot keep, printed by the command whose entire job is checking
		// claims before a client is allowed to believe them.
		if entry.Raw {
			fmt.Fprintf(w, "  %-*s            served as raw.%s.<tool>; %s is not pointed at it\n",
				width, "", entry.Server.ID, client)
			continue
		}
		fmt.Fprintf(w, "  %-*s            no raw surface; reached only through the capabilities that run on it\n",
			width, "")
	}
	// Always printed, including the all-green run -- especially then. A
	// report reading "5 declared, 0 refused" is the moment the word does the
	// most work and is least examined, and what it actually attests to is
	// one MCP handshake. A server can answer initialize perfectly and have
	// every one of its tools fail on the first call; that has happened on
	// this stack, to semgrep, for days, while it reported healthy. The line
	// costs one row and is the difference between a measurement and a
	// promise the check never made.
	fmt.Fprintf(w, "\n  Declared means it answered an MCP handshake, not that its tools work.\n")
	if len(p.Refused) > 0 {
		fmt.Fprintf(w, "  A refused server is left out of the payload, not switched off:\n")
		fmt.Fprintf(w, "  %s keeps whatever it already declares under that name.\n", client)
	}
}

// who names the server that answered, when it introduced itself. A server
// that does not is not a problem -- the handshake is what was being checked,
// not the manners -- so it contributes nothing rather than "unknown".
func who(r mcpprobe.Result) string {
	name := strings.TrimSpace(r.Name)
	if version := strings.TrimSpace(r.Version); name != "" && version != "" {
		name += " " + version
	}
	took := r.Took.Round(time.Millisecond)
	if name == "" {
		return fmt.Sprintf("  (%s)", took)
	}
	return fmt.Sprintf("  %s (%s)", name, took)
}
