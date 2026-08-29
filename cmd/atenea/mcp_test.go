package main

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The relay's whole job: a client's line reaches the service and the service's
// answer reaches the client, unchanged. Anything this bridge understood would
// be a second place the protocol could be decided.
func TestTheBridgeCarriesAConversationBothWays(t *testing.T) {
	settingsPath, _ := isolated(t)
	stop := serveFrom(t, settingsPath)
	defer stop()

	toBridge, fromClient := io.Pipe()
	fromBridge, toClient := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- cmdMCP(toBridge, toClient, "") }()

	answers := bufio.NewScanner(fromBridge)
	write := func(line string) {
		t.Helper()
		if _, err := io.WriteString(fromClient, line+"\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		if !answers.Scan() {
			t.Fatalf("the bridge sent nothing back: %v", answers.Err())
		}
		var out map[string]any
		if err := json.Unmarshal(answers.Bytes(), &out); err != nil {
			t.Fatalf("not JSON: %v (%s)", err, answers.Text())
		}
		return out
	}

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bridge-test","version":"1"}}}`)
	first := read()
	result, ok := first["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize did not reach the service: %v", first)
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}

	// A second message on the same connection: the chat the handshake opened
	// is still there, which is the thing a relay could plausibly get wrong by
	// reconnecting per line.
	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	second := read()
	if got := second["id"]; got != float64(2) {
		t.Fatalf("answers are out of step: %v", second)
	}
	listed, ok := second["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list was refused: %v", second)
	}
	if tools, _ := listed["tools"].([]any); len(tools) == 0 {
		t.Errorf("no tools came back: %v", listed)
	}

	// The client going away ends the relay, rather than leaving a process
	// holding a chat that nobody is on the other end of.
	_ = fromClient.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the bridge failed on a clean client exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the client hung up and the bridge kept running")
	}
}

// The setup failure that will actually happen: a client configured to launch
// this bridge before the service is installed or started. An MCP client shows
// one line and hides the rest, so that line has to name the fix.
func TestTheBridgeWithNoServiceSaysHowToStartOne(t *testing.T) {
	isolated(t)

	err := cmdMCP(strings.NewReader(""), io.Discard, "")
	if err == nil {
		t.Fatal("the bridge started with no service behind it")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable", got)
	}
	if !strings.Contains(err.Error(), "systemctl --user start atenea.service") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

// `--check` answers the setup question without going through a client at all,
// which is the only way to tell "my config is wrong" from "the server is down".
func TestTheBridgeCheckReportsAListeningService(t *testing.T) {
	settingsPath, _ := isolated(t)

	out, err := cli(t, "mcp", "--check")
	if err == nil {
		t.Fatalf("--check passed with no service running:\n%s", out)
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable", got)
	}

	stop := serveFrom(t, settingsPath)
	defer stop()

	out, err = cli(t, "mcp", "--check")
	if err != nil {
		t.Fatalf("--check failed with a service running: %v\n%s", err, out)
	}
	if !strings.Contains(out, "is listening at") {
		t.Errorf("--check does not say the service is there:\n%s", out)
	}
	if !strings.Contains(out, "would be offered as tools") {
		t.Errorf("--check does not say what the client would get:\n%s", out)
	}
}

// A word the command does not know is a typo, and a relay that started anyway
// would sit there silently reading a terminal.
func TestTheBridgeRefusesArgumentsItDoesNotKnow(t *testing.T) {
	isolated(t)

	_, err := cli(t, "mcp", "--verbose")
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", got)
	}
}

func TestTheBridgeOwnsTheDesktopProfileInInitialize(t *testing.T) {
	line, ok := injectMCPProfile([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","_meta":{"atenea":{"profile":"attacker"},"trace":"remove-only"},"params":{"_meta":{"atenea":{"profile":"attacker"},"trace":"keep"}}}`), "claude")
	if !ok {
		t.Fatal("initialize was not transformed")
	}
	var message map[string]any
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatal(err)
	}
	params, _ := message["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	atenea, _ := meta["atenea"].(map[string]any)
	if atenea["profile"] != "claude" {
		t.Fatalf("profile = %v, want trusted wrapper profile", atenea["profile"])
	}
	if meta["trace"] != "keep" {
		t.Fatalf("unrelated metadata changed: %v", meta)
	}
	topMeta, _ := message["_meta"].(map[string]any)
	if _, ok := topMeta["atenea"]; ok {
		t.Fatalf("untrusted top-level profile survived: %v", topMeta)
	}
}

// The isolation is a claim until somebody can see it. A connected client is a
// row on the status screen, and a screen that cannot show one makes every
// statement about per-chat grants unfalsifiable from the outside.
func TestAConnectedClientIsOnTheStatusScreen(t *testing.T) {
	settingsPath, _ := isolated(t)
	stop := serveFrom(t, settingsPath)
	defer stop()

	out, err := cli(t, "--config", settingsPath, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "chats") {
		t.Errorf("a chats table with nobody connected:\n%s", out)
	}

	toBridge, fromClient := io.Pipe()
	fromBridge, toClient := io.Pipe()
	go func() { _ = cmdMCP(toBridge, toClient, "") }()
	defer func() { _ = fromClient.Close() }()
	go func() { _, _ = io.Copy(io.Discard, fromBridge) }()

	if _, err := io.WriteString(fromClient, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"on-the-screen","version":"1"}}}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err = cli(t, "--config", settingsPath, "status")
		if err == nil && strings.Contains(out, "on-the-screen") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("a connected client never appeared on the screen:\n%s", out)
}
