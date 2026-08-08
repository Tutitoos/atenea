package core_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The stdio backend under test is this test binary re-executed. It is the
// smallest way to get a real child process into an end-to-end test, and a real
// process is the whole subject: the thing being proved here is that Atenea
// spawns one, lends it to a chat, and takes it away again.
func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("ATENEA_CORE_STDIO_HELPER") != "1" {
		t.Skip("not a helper invocation")
	}
	if ledger := os.Getenv("HELPER_LEDGER"); ledger != "" {
		f, err := os.OpenFile(ledger, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
		}
	}
	var initialized bool
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for in.Scan() {
		var msg struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		var result any
		switch msg.Method {
		case "initialize":
			initialized = true
			result = map[string]any{"protocolVersion": "2025-06-18"}
		case "notifications/initialized":
			continue
		case "tools/list":
			if !initialized {
				continue
			}
			result = map[string]any{"tools": []map[string]any{
				{
					"name": "search_code", "description": "graph-augmented search",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"query": map[string]any{"type": "string"}},
					},
				},
				{"name": "index_repository", "description": "index", "inputSchema": map[string]any{"type": "object"}},
				// Offered by the server and absent from the declaration, so
				// it must never reach a chat.
				{"name": "delete_project", "description": "delete", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf("pid=%d tool=%s query=%v", os.Getpid(), msg.Params.Name, msg.Params.Arguments["query"]),
			}}}
		default:
			result = map[string]any{}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		fmt.Println(string(body))
	}
}

// stdioSettings declares the helper as a raw backend, with a budget that keeps
// one of the three tools it offers out.
func stdioSettings(t *testing.T, ledger string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(repo+"/main.go", []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	settings := strings.Replace(socketSettings, `path = "/tmp"`, fmt.Sprintf("path = %q", repo), 1)
	return settings + fmt.Sprintf("\n[[mcp_server]]\nid = \"codebase-memory\"\n"+
		"command = [%q, \"-test.run=TestStdioHelperProcess\"]\n"+
		"env = { ATENEA_CORE_STDIO_HELPER = \"1\", HELPER_LEDGER = %q }\n"+
		"expose = \"raw\"\ntools = [\"search_code\", \"index_repository\"]\neffects = [\"read\"]\n"+
		"\n  [[mcp_server.tool]]\n  name = \"index_repository\"\n  effects = [\"read\", \"write\"]\n",
		self, ledger)
}

// The end of the road for the whole phase: a command in the settings file
// becomes a process, and that process's tools reach a chat under the reserved
// prefix, filtered to the budget.
func TestAStdioBackendReachesAChat(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	atenea := buildService(t, stdioSettings(t, ledger))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	var found, capability map[string]any
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		switch tool["name"] {
		case "raw.codebase-memory.search_code":
			found = tool
		case "code.search":
			capability = tool
		case "raw.codebase-memory.delete_project":
			t.Error("a tool outside the budget was offered to a chat")
		}
	}
	if capability == nil {
		t.Error("the catalog disappeared when a stdio backend was declared")
	}
	if found == nil {
		t.Fatalf("the backend's tool is not on the list: %v", tools)
	}
	if found["description"] != "graph-augmented search" {
		t.Errorf("description = %v, want the backend's own", found["description"])
	}

	// And it answers. A listing that worked over a pipe the call cannot use
	// would be a passthrough that only looks connected.
	answer := result(t, c.call("tools/call", map[string]any{
		"name":      "raw.codebase-memory.search_code",
		"arguments": map[string]any{"query": "TODO"},
	}), "tools/call")
	if text := answerText(answer); !strings.Contains(text, "query=TODO") {
		t.Errorf("answer = %q, want the arguments to have arrived untouched", text)
	}
	if n := spawnCount(t, ledger); n != 1 {
		t.Errorf("%d processes for a listing and a call, want 1", n)
	}
}

// Every chat shares the one process. This is the number the whole phase exists
// to change: six clients used to mean six copies of this server.
func TestEveryChatSharesOneStdioProcess(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	atenea := buildService(t, stdioSettings(t, ledger))
	defer serve(t, atenea)()

	var pids []string
	for range 3 {
		c := dial(t)
		c.handshake("omp")
		answer := result(t, c.call("tools/call", map[string]any{
			"name":      "raw.codebase-memory.search_code",
			"arguments": map[string]any{"query": "x"},
		}), "tools/call")
		text := answerText(answer)
		_, pid, _ := strings.Cut(text, "pid=")
		pid, _, _ = strings.Cut(pid, " ")
		pids = append(pids, pid)
	}
	for i, pid := range pids {
		if pid != pids[0] {
			t.Errorf("chat %d talked to pid %s, chat 0 to %s: that is a copy per chat", i, pid, pids[0])
		}
	}
	if n := spawnCount(t, ledger); n != 1 {
		t.Errorf("%d processes for three chats, want 1", n)
	}
}

// Shutting Atenea down takes the process with it.
//
// Nothing called Close on a backend before this phase, which cost nothing when
// every backend was an HTTP session. A stdio backend is a child, and one left
// behind per restart is how a machine accumulates the copies this replaces.
func TestShutdownStopsTheStdioProcess(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	atenea := buildService(t, stdioSettings(t, ledger))
	stop := serve(t, atenea)

	c := dial(t)
	c.handshake("omp")
	result(t, c.call("tools/list", nil), "tools/list")
	pid := lastPID(t, ledger)

	stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Kill it rather than leak it into the rest of the suite.
	_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
	t.Errorf("pid %d survived shutdown", pid)
}

func spawnCount(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(body)))
}

func lastPID(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		t.Fatal("no process was ever started")
	}
	pid, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("unreadable pid: %v", err)
	}
	return pid
}
