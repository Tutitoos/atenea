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

func toProbe(s config.MCPServer) mcpprobe.Server {
	return mcpprobe.Server{ID: s.ID, URL: s.URL, Command: s.Command, Env: s.Env, Timeout: s.Timeout}
}

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
	for i, s := range servers {
		held[i] = s.Expose == config.ExposeRaw || served[s.ID]
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
		entry := Checked{Server: probes[i], Result: results[i]}
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
		fmt.Fprintf(w, "  %-*s            served as raw.%s.<tool>; %s is not pointed at it\n",
			width, "", entry.Server.ID, client)
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
