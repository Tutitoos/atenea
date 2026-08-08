package passthrough_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The fake server is this test binary re-executed, which is the standard way
// to get a real child process without shipping a fixture binary or depending
// on an interpreter being installed. It behaves the way MCP stdio servers on
// this machine actually behave, including the parts the specification says
// they should not: it logs to stdout, and it answers nothing until it has been
// initialized.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ATENEA_STDIO_HELPER") != "1" {
		t.Skip("not a helper invocation")
	}
	// Every spawn appends a line, so a test can count how many processes its
	// calls really produced rather than assuming.
	if ledger := os.Getenv("HELPER_LEDGER"); ledger != "" {
		f, err := os.OpenFile(ledger, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
		}
	}
	// A server that logs to stdout before saying anything protocol-shaped.
	fmt.Println("starting up, listening on stdin")

	var initialized bool
	calls := 0
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
		switch msg.Method {
		case "initialize":
			initialized = true
			reply(msg.ID, map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "helper"}})
			continue
		case "notifications/initialized":
			continue
		}
		if !initialized {
			// The reason the handshake has to be replayed per spawn: before
			// it, this server answers nothing.
			continue
		}
		calls++
		if n, _ := strconv.Atoi(os.Getenv("HELPER_DIE_AFTER")); n > 0 && calls >= n {
			fmt.Fprintln(os.Stderr, "helper: falling over on purpose")
			os.Exit(3)
		}
		// Die once and only once: the first process to reach this falls
		// over, and its replacement finds the marker and behaves. That is
		// the shape a retry has to survive -- a backend that died, not one
		// that dies every time, which no retry could help.
		if marker := os.Getenv("HELPER_DIE_ONCE"); marker != "" {
			if _, err := os.Stat(marker); err != nil {
				_ = os.WriteFile(marker, []byte("died"), 0o600)
				fmt.Fprintln(os.Stderr, "helper: falling over on purpose")
				os.Exit(3)
			}
		}
		switch msg.Method {
		case "tools/list":
			fmt.Println("about to answer a list") // more stdout noise
			reply(msg.ID, map[string]any{"tools": []map[string]any{
				{"name": "search_code", "description": "search", "inputSchema": map[string]any{"type": "object"}},
				{"name": "index_repository", "description": "index", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			// A slow tool, so a test can have several in flight at once and
			// prove the answers do not cross.
			if d, err := time.ParseDuration(fmt.Sprint(msg.Params.Arguments["sleep"])); err == nil {
				time.Sleep(d)
			}
			reply(msg.ID, map[string]any{"content": []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf("pid=%d tool=%s echo=%v", os.Getpid(), msg.Params.Name, msg.Params.Arguments["echo"]),
			}}})
		}
	}
}

func reply(id *int64, result map[string]any) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(body))
}

func helper(t *testing.T, allowed []string, env map[string]string) passthrough.Backend {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	full := map[string]string{"ATENEA_STDIO_HELPER": "1"}
	for k, v := range env {
		full[k] = v
	}
	b := passthrough.New(passthrough.Spec{
		ID:      "codebase-memory",
		Command: []string{self, "-test.run=TestHelperProcess", "-test.v=false"},
		Env:     full,
		Timeout: 10 * time.Second,
		Allowed: allowed,
	})
	t.Cleanup(b.Close)
	return b
}

// A command declaration produces a stdio backend and a url one does not: the
// choice is a consequence of the block, never a second decision.
func TestTheTransportFollowsTheDeclaration(t *testing.T) {
	stdio := passthrough.New(passthrough.Spec{ID: "x", Command: []string{"/bin/true"}})
	if got := stdio.Where(); got != "/bin/true" {
		t.Errorf("stdio backend Where() = %q, want the command", got)
	}
	http := passthrough.New(passthrough.Spec{ID: "x", URL: "http://127.0.0.1:1/mcp"})
	if got := http.Where(); got != "http://127.0.0.1:1/mcp" {
		t.Errorf("http backend Where() = %q, want the url", got)
	}
}

// The point of the whole mode: many chats, one process. Six clients each
// spawning a private copy is what this replaces, so the count is the test.
func TestConcurrentCallersShareOneProcess(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{"HELPER_LEDGER": ledger})

	const chats = 8
	pids := make([]string, chats)
	var wg sync.WaitGroup
	for i := range chats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := b.Call(t.Context(), "search_code", map[string]any{"echo": i, "sleep": "20ms"})
			if err != nil {
				t.Errorf("chat %d: %v", i, err)
				return
			}
			pids[i] = field(t, raw, "pid=")
		}()
	}
	wg.Wait()

	for i, pid := range pids {
		if pid == "" {
			continue
		}
		if pid != pids[0] {
			t.Errorf("chat %d talked to pid %s, chat 0 talked to %s: that is two processes", i, pid, pids[0])
		}
	}
	if n := lines(t, ledger); n != 1 {
		t.Errorf("%d processes were started for %d concurrent chats, want 1", n, chats)
	}
}

// Answers are routed by id. Every caller asking at once must get its own
// answer back and not somebody else's -- the failure this would catch is
// silent, which is why it is worth a test of its own.
func TestAnswersGoBackToTheCallerThatAsked(t *testing.T) {
	b := helper(t, []string{"search_code"}, nil)
	// Warm the handshake first so the race under test is the routing one.
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("listing: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Staggered sleeps mean the server answers out of the order it
			// was asked, so an implementation that paired answers with
			// callers by arrival would fail here and only here.
			raw, err := b.Call(t.Context(), "search_code", map[string]any{
				"echo": i, "sleep": fmt.Sprintf("%dms", (12-i)*8),
			})
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			if got := field(t, raw, "echo="); got != strconv.Itoa(i) {
				t.Errorf("caller %d got the answer for %s", i, got)
			}
		}()
	}
	wg.Wait()
}

// The handshake belongs to the process. A second call must not repeat it, and
// a chat that arrives later must find it already done.
func TestTheHandshakeHappensOncePerProcess(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{"HELPER_LEDGER": ledger})
	for range 3 {
		if _, err := b.Tools(t.Context()); err != nil {
			t.Fatalf("listing: %v", err)
		}
	}
	if n := lines(t, ledger); n != 1 {
		t.Errorf("%d processes for three sequential calls, want 1", n)
	}
}

// stdin must not close between calls: a stdio server reads EOF as its client
// leaving and exits. The shim this replaces held a FIFO open with `sleep
// infinity` for exactly this reason.
func TestTheProcessSurvivesBetweenCalls(t *testing.T) {
	b := helper(t, []string{"search_code"}, nil)
	first, err := b.Call(t.Context(), "search_code", map[string]any{"echo": "one"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Long enough that a server which took EOF for a goodbye would be gone.
	time.Sleep(300 * time.Millisecond)
	second, err := b.Call(t.Context(), "search_code", map[string]any{"echo": "two"})
	if err != nil {
		t.Fatalf("second call after a pause: %v", err)
	}
	if a, b := field(t, first, "pid="), field(t, second, "pid="); a != b {
		t.Errorf("pid changed from %s to %s between calls: the process did not survive", a, b)
	}
}

// A server that dies mid-call fails its caller with the reason it printed,
// and the call after it gets a new process rather than an error.
func TestADeadProcessFailsItsCallerAndThenRecovers(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{
		"HELPER_LEDGER": ledger, "HELPER_DIE_AFTER": "2",
	})
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// The second call is the one the helper dies on. It must come back as a
	// failure with the server's own last words, not as a timeout.
	start := time.Now()
	_, err := b.Call(t.Context(), "search_code", nil)
	if err == nil {
		t.Fatal("a call to a dying server answered")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the death took %v to report: that is a timeout, not a death", took)
	}
	if !strings.Contains(err.Error(), "falling over on purpose") {
		t.Errorf("error = %v, want the server's own stderr in it", err)
	}
	// And the next caller is not punished for it.
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("after the death: %v", err)
	}
	if n := lines(t, ledger); n != 2 {
		t.Errorf("%d processes, want 2: one that died and one that replaced it", n)
	}
}

// A dead listing is retried and a dead call is not, and the difference is
// about effects: the server may have run the tool and died carrying the
// answer back, so re-sending it would run somebody's write twice.
func TestADeadCallIsNotSilentlyRepeated(t *testing.T) {
	dir := t.TempDir()
	ledger := dir + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{
		"HELPER_LEDGER": ledger, "HELPER_DIE_ONCE": dir + "/died",
	})
	// The first call is a tools/call, and it is the one the helper dies on.
	if _, err := b.Call(t.Context(), "search_code", nil); err == nil {
		t.Fatal("a call to a dying server answered: it was retried on a fresh process")
	}
	if n := lines(t, ledger); n != 1 {
		t.Errorf("%d processes were started for one dead call, want 1: the call was re-sent", n)
	}

	// A listing is the other half of the rule: nothing happened on the
	// server, so the caller gets an answer from its replacement.
	freshDir := t.TempDir()
	fresh := freshDir + "/spawns"
	list := helper(t, []string{"search_code"}, map[string]string{
		"HELPER_LEDGER": fresh, "HELPER_DIE_ONCE": freshDir + "/died",
	})
	if _, err := list.Tools(t.Context()); err != nil {
		t.Fatalf("a listing was not retried past the death: %v", err)
	}
	if n := lines(t, fresh); n != 2 {
		t.Errorf("%d processes, want 2: the listing should have been asked again", n)
	}
}

// The budget is the same rule on both transports: it filters the list and it
// refuses the call, and the refusal names the right bin.
func TestTheBudgetAppliesToAStdioBackend(t *testing.T) {
	b := helper(t, []string{"search_code"}, nil)
	tools, err := b.Tools(t.Context())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search_code" {
		t.Fatalf("tools = %v, want only search_code", names(tools))
	}
	_, err = b.Call(t.Context(), "index_repository", nil)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("calling an unlisted tool = %v, want permission_denied", got)
	}
}

// Close stops the child. Atenea started this one, so leaving it running would
// recreate the waste the mode exists to remove -- one orphan per restart.
func TestCloseStopsTheProcess(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{"HELPER_LEDGER": ledger})
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("listing: %v", err)
	}
	pid := lines(t, ledger)
	if pid != 1 {
		t.Fatalf("%d processes before close, want 1", pid)
	}
	b.Close()
	// Asking the operating system rather than trusting the call: the whole
	// failure being tested is a process that is still there afterwards.
	if err := waitGone(readPID(t, ledger)); err != nil {
		t.Error(err)
	}
}

func waitGone(pid int) error {
	// `kill -0` delivers no signal and succeeds only while the process is
	// there, so its refusal is the answer being waited for rather than a
	// failure to report. Asked as a question, because that is what it is.
	alive := func() bool { return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil }
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive() {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("pid %d is still running after Close", pid)
}

func field(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var body struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Content) == 0 {
		t.Fatalf("unreadable result %s: %v", raw, err)
	}
	for _, part := range strings.Fields(body.Content[0].Text) {
		if rest, ok := strings.CutPrefix(part, key); ok {
			return rest
		}
	}
	t.Fatalf("no %s in %q", key, body.Content[0].Text)
	return ""
}

func lines(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(body)))
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	fields := strings.Fields(string(body))
	pid, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("unreadable pid: %v", err)
	}
	return pid
}

func names(tools []passthrough.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
