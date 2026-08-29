package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdMCP is the bridge between an MCP client and the running service.
//
// MCP clients launch their servers as subprocesses and talk newline-delimited
// JSON over stdin and stdout. Atenea's door is a Unix socket carrying the same
// framing, so this is a relay and deliberately nothing more: no parsing, no
// buffering of whole messages, no opinion about what goes past. Every decision
// -- the handshake, the catalog, which implementation answers -- belongs to
// the one service, and a bridge that understood any of it would be a second
// place those answers could differ.
//
// One process per client, and one connection per process, which is what makes
// a chat mean something: the client's own name arrives in its `initialize`, and
// the chat closes when the client exits and this relay's socket goes with it.
//
// Not a fallback into running Atenea in-process. That would give each client
// its own core, its own catalog and its own idea of what is running, which is
// the arrangement this whole design exists to replace -- and the chats table
// would go back to being empty.
func cmdMCP(in io.Reader, out io.Writer, profile string) error {
	warnIfTerminal(in)
	conn, err := ipc.Dial(core.SocketPath())
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"no atenea service is listening at %s: start it with `systemctl --user start atenea.service` "+
				"(or `atenea service install` first), then reconnect this client",
			core.SocketPath())
	}
	defer func() { _ = conn.Close() }()

	// Two directions, and the first one to end takes the process with it.
	// A client that exits closes our stdin; a service that stops closes the
	// socket. Both mean the same thing here -- there is nobody left to relay
	// for -- and waiting for the other side after that is how a subprocess
	// becomes an orphan a client has to SIGKILL.
	if profile == "" {
		profile = "shared"
	}
	done := make(chan error, 2)
	go func() { done <- relayMCPClient(conn, in, "to the service", profile) }()
	go func() { done <- relay(out, conn, "from the service") }()
	return <-done
}

func relayMCPClient(dst io.Writer, src io.Reader, direction, profile string) error {
	if profile == "" {
		return relay(dst, src, direction)
	}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if transformed, ok := injectMCPProfile(line, profile); ok {
			line = transformed
		}
		if _, err := dst.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("relaying %s: %w", direction, err)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("reading %s: %w", direction, err)
	}
	return nil
}

func injectMCPProfile(line []byte, profile string) ([]byte, bool) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return line, false
	}
	var method string
	if raw, ok := message["method"]; !ok || json.Unmarshal(raw, &method) != nil || method != core.MethodInitialize {
		return line, false
	}
	params := map[string]json.RawMessage{}
	if raw, ok := message["params"]; ok {
		if err := json.Unmarshal(raw, &params); err != nil {
			return line, false
		}
	}
	meta := map[string]json.RawMessage{}
	if raw, ok := params["_meta"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return line, false
		}
	}
	delete(meta, "atenea")
	atenea, _ := json.Marshal(map[string]string{"profile": profile})
	meta["atenea"] = atenea
	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		return line, false
	}
	params["_meta"] = encodedMeta
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return line, false
	}
	message["params"] = encodedParams
	// `_meta` belongs inside initialize.params in MCP. If a non-standard
	// client also supplied a top-level copy, remove only its Atenea profile so
	// an untrusted value cannot survive in the forwarded request.
	if raw, ok := message["_meta"]; ok {
		topMeta := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &topMeta); err == nil {
			delete(topMeta, "atenea")
			if encodedTopMeta, marshalErr := json.Marshal(topMeta); marshalErr == nil {
				message["_meta"] = encodedTopMeta
			}
		}
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return line, false
	}
	return encoded, true
}

// relay copies whole lines, flushing each one.
//
// Line at a time, because both ends are waiting on the other: an io.Copy with
// a buffer underneath would hold a request until enough bytes arrived to be
// worth writing, and the reply that would have filled the buffer is behind the
// request that is still sitting in it. It deadlocks on the first message.
func relay(dst io.Writer, src io.Reader, direction string) error {
	lines := bufio.NewScanner(src)
	// One MCP message can carry a whole file's worth of matches, and the
	// default 64KB would truncate it into a parse error at the far end.
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for lines.Scan() {
		if _, err := dst.Write(append(lines.Bytes(), '\n')); err != nil {
			return fmt.Errorf("relaying %s: %w", direction, err)
		}
	}
	if err := lines.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("reading %s: %w", direction, err)
	}
	return nil
}

// mcpProbe reports whether a service is answering, for `atenea mcp --check`.
//
// An MCP client that cannot start its server usually says so in one line and
// hides the reason, so the setup question -- "is this configured right?" --
// needs an answer that does not go through a client at all.
func mcpProbe(out io.Writer) error {
	status, ok := core.Asked()
	if !ok {
		return contract.Fail(contract.FailureUnavailable,
			"no atenea service is listening at %s", core.SocketPath())
	}
	fmt.Fprintf(out, "atenea %s is listening at %s\n", status.Version, core.SocketPath())
	fmt.Fprintf(out, "%d capability(ies) would be offered as tools\n", len(status.Capabilities))
	fmt.Fprintf(out, "%d chat(s) open right now\n", len(status.Chats))
	return nil
}

// warnIfTerminal catches the person who runs `atenea mcp` by hand. It is a
// relay, so on a terminal it sits there reading the keyboard, which looks
// exactly like a hang. stderr is the only place a bridge may say anything:
// stdout carries the protocol and nothing else.
//
// It does not time anybody out. A client that connects and stays quiet holds
// one connection and one chat, which is cheap and correct -- an editor does
// precisely that between tasks.
func warnIfTerminal(in io.Reader) {
	file, ok := in.(*os.File)
	if !ok {
		return
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Fprintln(os.Stderr,
		"atenea mcp is a bridge for an MCP client, not a command to run by hand.")
	fmt.Fprintln(os.Stderr,
		"It is relaying this terminal now. Ctrl-D to stop, or `atenea mcp --check` to test the setup.")
}
