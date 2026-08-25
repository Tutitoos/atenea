package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdDesktop performs one desktop action, after asking.
//
// # Why this command exists at all
//
// The plan it comes from said desktop actions would be CLI-only, so that a
// human sat at a terminal would confirm each one. That turned out to be
// impossible as stated, and the reason is worth keeping: `ask` and
// `decide --run` run the core IN THIS PROCESS, and this process was started
// from a shell. macOS attributes a device permission to the responsible
// ancestor, so the permissions in play here belong to the terminal -- which is
// exactly what internal/adapter/desktop refuses to spend.
//
// So the confirmation and the execution have to happen in different places.
// This command asks here, where there is a TTY, and then dispatches over the
// socket `atenea mcp` already relays to, so the act itself runs inside the
// service -- the process launchd started, which holds the permission in its
// own name.
func cmdDesktop(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"usage: atenea desktop ACTION --repo ID --app BUNDLE_ID [--set NAME=VAL ...] --confirm")
	}
	action, rest := args[0], args[1:]

	var repository, application string
	var confirm bool
	var fields multiFlag
	flags := flag.NewFlagSet("desktop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository the commission belongs to")
	flags.StringVar(&application, "app", "", "bundle identifier to act on")
	flags.BoolVar(&confirm, "confirm", false, "show what will happen and require an interactive yes")
	flags.Var(&fields, "set", "capability input as NAME=VALUE; repeat for several")
	if err := flags.Parse(rest); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if repository == "" || application == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"desktop needs --repo and --app")
	}

	capability := "desktop." + action
	payload := map[string]any{"repository": repository, "application": application}
	for _, pair := range fields {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			return contract.Fail(contract.FailureInvalidInput,
				"--set wants NAME=VALUE, got %q", pair)
		}
		payload[name] = desktopValue(value)
	}

	// Always, and with no flag to turn it off. Every other confirmation in
	// this binary is opt-in because the thing behind it is usually harmless;
	// nothing behind this one is. A `--yes` added later would be the whole
	// point of the command being removed for convenience.
	if err := confirmDesktop(out, in, capability, application, payload, confirm); err != nil {
		return err
	}
	return dispatchThroughService(capability, payload, out)
}

// confirmDesktop shows exactly what is about to happen and waits for a yes.
func confirmDesktop(out io.Writer, in io.Reader, capability, application string,
	payload map[string]any, confirmed bool) error {
	if !confirmed {
		return contract.Fail(contract.FailurePermissionDenied,
			"%s acts on this machine's screen or keyboard; pass --confirm", capability)
	}
	fmt.Fprintf(out, "\n  %s\n  on %s\n", capability, application)
	keys := make([]string, 0, len(payload))
	for key := range payload {
		if key != "repository" && key != "application" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		// The text is shown in full rather than elided. Somebody confirming a
		// keystroke has to be able to read the keystroke, and a summary is how
		// a confirmation becomes a formality.
		fmt.Fprintf(out, "  %-10s %v\n", key, payload[key])
	}
	fmt.Fprintf(out, "\n  This happens on your own desktop and cannot be undone from here.\n  Type yes to continue: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return contract.Fail(contract.FailurePermissionDenied, "no answer: %v", err)
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return contract.Fail(contract.FailurePermissionDenied, "not confirmed")
	}
	return nil
}

// dispatchThroughService sends one tools/call over the service's socket.
//
// The same door `atenea mcp` relays to, spoken directly rather than proxied,
// because there is one message to send and waiting on a relay's lifetime to
// deliver it would mean holding a pipe open for a reply this can simply read.
func dispatchThroughService(capability string, payload map[string]any, out io.Writer) error {
	conn, err := ipc.Dial(core.SocketPath())
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"no atenea service is listening at %s -- a desktop action has to run inside the "+
				"service, because that is the process the system grants the permission to. "+
				"Start it with `atenea service install`", core.SocketPath())
	}
	defer func() { _ = conn.Close() }()

	send := func(id int, method string, params any) error {
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": params,
		})
		if err != nil {
			return err
		}
		_, err = conn.Write(append(body, '\n'))
		return err
	}
	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	read := func() (map[string]any, error) {
		if !lines.Scan() {
			return nil, contract.Fail(contract.FailureUnavailable,
				"the service closed the connection without answering")
		}
		var msg map[string]any
		return msg, json.Unmarshal(lines.Bytes(), &msg)
	}

	if err := send(1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "atenea-desktop", "version": "1"},
	}); err != nil {
		return contract.Fail(contract.FailureUnavailable, "handshake: %v", err)
	}
	if _, err := read(); err != nil {
		return contract.Fail(contract.FailureUnavailable, "handshake: %v", err)
	}
	if err := send(2, "tools/call", map[string]any{
		"name": capability, "arguments": payload,
	}); err != nil {
		return contract.Fail(contract.FailureUnavailable, "dispatch: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	answer, err := read()
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "reading the answer: %v", err)
	}
	return printDesktopAnswer(out, answer)
}

func printDesktopAnswer(out io.Writer, answer map[string]any) error {
	result, _ := answer["result"].(map[string]any)
	content, _ := result["content"].([]any)
	text := ""
	for _, entry := range content {
		if row, ok := entry.(map[string]any); ok {
			if s, ok := row["text"].(string); ok {
				text += s
			}
		}
	}
	if failed, _ := result["isError"].(bool); failed {
		return contract.Fail(contract.FailurePermissionDenied, "%s", text)
	}
	fmt.Fprintf(out, "\n%s\n", text)
	return nil
}

// desktopValue reads a --set value as the narrowest thing it can be.
//
// Coordinates are the common case and they are numbers; a caller should not
// have to say so, and a schema that received "412" as a string would refuse a
// call that was correct.
func desktopValue(value string) any {
	var number int
	if _, err := fmt.Sscanf(value, "%d", &number); err == nil &&
		fmt.Sprintf("%d", number) == value {
		return number
	}
	switch strings.ToLower(value) {
	case "true":
		return true
	case "false":
		return false
	}
	return value
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
