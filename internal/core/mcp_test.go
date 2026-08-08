package core_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
)

// The protocol version this server speaks. A client that asks for it gets it
// back; a client that asks for anything else gets this and decides for itself.
const mcpVersion = "2025-06-18"

// A client is one connection held open across several messages, which is what
// the one-shot `ask` cannot express: MCP is a conversation, and the whole point
// of the handshake is that what comes after it depends on what it agreed.
type client struct {
	t     *testing.T
	conn  net.Conn
	lines *bufio.Scanner
	id    int
}

func dial(t *testing.T) *client {
	t.Helper()
	conn, err := net.Dial("unix", core.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	c := &client{t: t, conn: conn, lines: scanner}
	t.Cleanup(c.close)
	return c
}

func (c *client) close() { _ = c.conn.Close() }

// send writes one message and does not wait: a notification has no answer, and
// a test that waited for one would hang rather than fail.
func (c *client) send(msg map[string]any) {
	c.t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	if _, err := c.conn.Write(append(body, '\n')); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// call sends a request and reads the one answer it is owed.
func (c *client) call(method string, params map[string]any) map[string]any {
	c.t.Helper()
	c.id++
	msg := map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.send(msg)
	if !c.lines.Scan() {
		c.t.Fatalf("%s: nothing came back: %v", method, c.lines.Err())
	}
	var out map[string]any
	if err := json.Unmarshal(c.lines.Bytes(), &out); err != nil {
		c.t.Fatalf("%s: answer is not JSON: %v (%s)", method, err, c.lines.Text())
	}
	return out
}

// notify sends a notification, which by definition is not answered.
func (c *client) notify(method string, params map[string]any) {
	c.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.send(msg)
}

// handshake is the opening every other test needs and none of them are about.
func (c *client) handshake(name string) map[string]any {
	c.t.Helper()
	out := c.call("initialize", map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": name, "version": "1.0.0"},
	})
	c.notify("notifications/initialized", nil)
	return out
}

func result(t *testing.T, answer map[string]any, what string) map[string]any {
	t.Helper()
	if errObj, ok := answer["error"]; ok {
		t.Fatalf("%s failed: %v", what, errObj)
	}
	out, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result object: %v", what, answer)
	}
	return out
}

// The handshake is the whole contract of what follows: the version both sides
// will speak, and the fact that this server has tools at all. A client that
// cannot read this cannot ask for anything.
func TestTheHandshakeNamesTheProtocolAndTheTools(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	got := result(t, dial(t).handshake("omp"), "initialize")

	if got["protocolVersion"] != mcpVersion {
		t.Errorf("protocolVersion = %v, want %s", got["protocolVersion"], mcpVersion)
	}
	caps, _ := got["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("the server does not declare tools: %v", caps)
	}
	info, _ := got["serverInfo"].(map[string]any)
	if info["name"] != "atenea" {
		t.Errorf("serverInfo.name = %v, want atenea", info["name"])
	}
	if info["version"] == "" || info["version"] == nil {
		t.Errorf("serverInfo carries no version: %v", info)
	}
}

// Version negotiation, and the reason it is not just an equality check: a
// client that speaks something else is told what this server speaks and gets to
// decide whether to go on. Answering with the client's own unknown version
// would be a lie that only shows up later, on a message one side cannot parse.
func TestAVersionWeDoNotSpeakIsAnsweredWithOurs(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	got := result(t, dial(t).call("initialize", map[string]any{
		"protocolVersion": "1999-01-01",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "omp", "version": "1.0.0"},
	}), "initialize")

	if got["protocolVersion"] != mcpVersion {
		t.Errorf("protocolVersion = %v, want ours (%s)", got["protocolVersion"], mcpVersion)
	}
}

// A notification has no id and is owed no answer. Sending one back puts a
// message on the wire the client is not reading for, which desynchronises every
// response after it: the client reads our unsolicited line as the answer to its
// next request.
func TestANotificationIsNotAnswered(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	// The spelling below is the specification's own and is not a typo. A wire
	// literal is not prose, and correcting it would name a method nobody
	// implements.
	c.notify("notifications/cancelled", map[string]any{"requestId": 1}) //nolint:misspell // protocol method name

	// The next request must get its own answer, not a stale one.
	answer := c.call("tools/list", nil)
	if got := answer["id"]; got != float64(2) {
		t.Fatalf("answers are out of step: id = %v, want 2 (%v)", got, answer)
	}
}

// Every capability Atenea ships is a tool a client can call, described by the
// declaration in the settings file rather than by anything written twice.
func TestToolsListIsTheShippedCatalogue(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("no tools: %v", got)
	}
	var search map[string]any
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if tool["name"] == "code.search" {
			search = tool
		}
	}
	if search == nil {
		t.Fatalf("code.search is not on the list: %v", tools)
	}
	if search["description"] == "" || search["description"] == nil {
		t.Errorf("code.search has no description: %v", search)
	}
	schema, _ := search["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("inputSchema is not an object schema: %v", schema)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Errorf("the declared input is missing from the schema: %v", props)
	}
	if _, ok := search["outputSchema"]; !ok {
		t.Errorf("code.search declares outputs but the tool does not: %v", search)
	}
}

// The unit of work is the repository, and a tool nobody can aim is a tool
// nobody can use. The capability's own declaration says nothing about which
// repository to search, because that is Atenea's question rather than the
// capability's -- so the tool has to add it, or a model has no way to express
// what a caller at a terminal expresses with `--repo`.
func TestAToolIsAimableAtARepository(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		repo, ok := props["repository"].(map[string]any)
		if !ok {
			t.Errorf("%v cannot be aimed at a repository: %v", tool["name"], props)
			continue
		}
		if repo["description"] == "" || repo["description"] == nil {
			t.Errorf("%v: the repository argument does not say which ones exist", tool["name"])
		}
	}
}

// The call that makes the rest of it worth building: a real capability, run
// against a real repository, answered in the shape a client can act on.
func TestCallingAToolRunsTheCapability(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/call", map[string]any{
		"name":      "code.search",
		"arguments": map[string]any{"query": "TODO", "repository": "work"},
	}), "tools/call")

	if got["isError"] == true {
		t.Fatalf("the call failed: %v", got)
	}
	structured, ok := got["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", got)
	}
	matches, _ := structured["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("the search found nothing in a repository that contains a TODO: %v", structured)
	}

	// The text block is the same answer, not a second one: a client that
	// cannot read structuredContent must not get a different story.
	content, _ := got["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content blocks: %v", got)
	}
	first, _ := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Fatalf("the first block is not text: %v", first)
	}
	var echoed map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(first["text"])), &echoed); err != nil {
		t.Fatalf("the text block is not the answer as JSON: %v", err)
	}
	if len(echoed["matches"].([]any)) != len(matches) {
		t.Errorf("the two halves of the answer disagree: %v vs %v", echoed, structured)
	}
}

// A tool that does not exist is a mistake in the request, not a failure of the
// work: the caller asked for something that was never on the list it read.
func TestAnUnknownToolIsAProtocolError(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	answer := c.call("tools/call", map[string]any{
		"name":      "code.conjure",
		"arguments": map[string]any{},
	})

	errObj, ok := answer["error"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown tool was not refused: %v", answer)
	}
	if errObj["code"] != float64(-32602) {
		t.Errorf("code = %v, want -32602", errObj["code"])
	}
}

// Work that ran and failed is not a protocol error. The distinction is the
// client's: a protocol error means the request was malformed and retrying it
// unchanged is pointless, while a tool error is an answer -- the model reads it
// and can try something else.
func TestWorkThatFailsIsAToolErrorNotAProtocolError(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	answer := c.call("tools/call", map[string]any{
		"name":      "code.search",
		"arguments": map[string]any{"query": "TODO", "repository": "nowhere"},
	})

	if _, isProtocol := answer["error"]; isProtocol {
		t.Fatalf("a failed run was reported as a broken request: %v", answer)
	}
	got := result(t, answer, "tools/call")
	if got["isError"] != true {
		t.Errorf("a failed run was reported as success: %v", got)
	}
	content, _ := got["content"].([]any)
	if len(content) == 0 {
		t.Errorf("a failure with nothing to read: %v", got)
	}
}

// Initialization is the first interaction, and until it happens there is no
// chat to run anything on behalf of. Serving a tool call to a client that never
// said who it was would mean work with no chat behind it -- untraceable on the
// status screen and ungoverned by any grant.
func TestToolsBeforeTheHandshakeAreRefused(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	answer := dial(t).call("tools/list", nil)
	if _, ok := answer["error"]; !ok {
		t.Errorf("tools were served before the handshake: %v", answer)
	}
}

// Every connection is a chat, and the status screen is where that becomes
// visible. This is the table that was empty for the whole life of the project:
// two clients connected at once, each named, each its own isolation.
func TestEachConnectionIsItsOwnChat(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	first := dial(t)
	first.handshake("omp")
	second := dial(t)
	second.handshake("claude-code")

	status, ok := core.Asked()
	if !ok {
		t.Fatal("the service stopped answering")
	}
	if len(status.Chats) != 2 {
		t.Fatalf("chats = %d, want 2: %+v", len(status.Chats), status.Chats)
	}
	names := []string{status.Chats[0].Client, status.Chats[1].Client}
	for _, want := range []string{"omp", "claude-code"} {
		if !slicesContains(names, want) {
			t.Errorf("%s is not on the status screen: %v", want, names)
		}
	}

	// Hanging up closes the chat: a table that only grows is a leak that
	// looks like popularity.
	second.close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if status, ok := core.Asked(); ok && len(status.Chats) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("a client hung up and its chat stayed open")
}

func slicesContains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// mcpSettings is the socket fixture plus a repository that actually contains
// something to find, because the call test is only worth running against real
// output from a real tool.
func mcpSettings(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	body := "package main\n\n// TODO: the thing this test looks for\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return strings.Replace(socketSettings, `path = "/tmp"`, fmt.Sprintf("path = %q", repo), 1)
}
