package passthrough_test

import (
	"bufio"
	"context"
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
	// What the client said when this server asked it something, reported
	// back inside the tool result: the answer to a server-initiated request
	// is invisible from the caller's side otherwise.
	heard := ""
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
			// A server that writes to stdout and never frames a line. Real
			// ones do it by accident -- a panic trace, a progress bar, a
			// library logging to the wrong descriptor -- and the reader on
			// the other end has to have a roof or it grows a buffer until
			// the kernel picks a process to kill.
			if n, _ := strconv.Atoi(os.Getenv("HELPER_FLOODS")); n > 0 {
				chunk := strings.Repeat("x", 64<<10)
				for written := 0; written < n; written += len(chunk) {
					if _, err := os.Stdout.WriteString(chunk); err != nil {
						return
					}
				}
			}
			// A handshake that takes its time, so a test can cancel the
			// context of the chat that happened to arrive first while the
			// process every other chat will share is still initializing.
			if d, err := time.ParseDuration(os.Getenv("HELPER_SLOW_INIT")); err == nil {
				time.Sleep(d)
			}
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
		// Ask the client something before answering, the way semgrep asks
		// roots/list before its first tools/call, and refuse to move until
		// it replies. A client that drops the question wedges this server
		// for good -- which is the failure this reproduces. The value is
		// the id to ask with, verbatim, so a test can check it comes back
		// unchanged whether it is a number or a string.
		if id := os.Getenv("HELPER_ASKS_FIRST"); id != "" && heard == "" {
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"method":"roots/list","params":{}}`+"\n", id)
			if !in.Scan() {
				return // the client went away rather than answer
			}
			heard = in.Text()
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
				"text": fmt.Sprintf("pid=%d tool=%s echo=%v heard=%s", os.Getpid(), msg.Params.Name, msg.Params.Arguments["echo"], heard),
			}}})
		}
	}
	// A server that does not take EOF on stdin for a goodbye. Closing stdin
	// is the polite request every stdio server is supposed to understand;
	// this one ignores it, which is what forces stop() down its kill path and
	// holds open the window between a Close that has already dropped the
	// process and the reader of that process finally noticing it is dead.
	if os.Getenv("HELPER_STUBBORN") == "1" {
		time.Sleep(30 * time.Second)
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
		ID:      "graph",
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

// The protocol runs in both directions, and a backend is allowed to ask the
// client something before it answers. Semgrep does exactly this -- `roots/list`
// ahead of its first tools/call -- and until 2026-08-09 the question was read
// as if it were an answer, delivered to a caller waiting on an id that nobody
// had used, and dropped. The server then waited for a reply that was never
// sent, so every call to that backend died on its timeout while the process
// sat healthy and idle. One unanswered question wedges the process for every
// chat attached to it, not only the one that asked.
func TestABackendThatAsksTheClientFirstStillGetsAnswered(t *testing.T) {
	b := helper(t, []string{"search_code"}, map[string]string{"HELPER_ASKS_FIRST": "0"})
	raw, err := b.Call(t.Context(), "search_code", map[string]any{"echo": "one"})
	if err != nil {
		t.Fatalf("a backend that asks first never answered: %v", err)
	}
	if got := field(t, raw, "echo="); got != "one" {
		t.Errorf("echo = %q, want the caller's own argument", got)
	}
}

// What Atenea says back matters as much as saying anything. The handshake
// declares no capabilities, so the honest answer to a request for roots,
// sampling or elicitation is that it does not serve it -- a JSON-RPC error
// against the id the server chose, not an invented result. The id is echoed
// verbatim rather than parsed as a number, because the protocol allows a
// string there and a server given back an id it never used is no better off
// than one given silence.
func TestARefusalCarriesTheServersOwnIDAndSaysNo(t *testing.T) {
	b := helper(t, []string{"search_code"}, map[string]string{"HELPER_ASKS_FIRST": `"ab-7"`})
	raw, err := b.Call(t.Context(), "search_code", map[string]any{"echo": "one"})
	if err != nil {
		t.Fatalf("calling: %v", err)
	}
	heard := tail(t, raw, "heard=")
	var answer struct {
		ID    string `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(heard), &answer); err != nil {
		t.Fatalf("the server was sent %q, which is not JSON: %v", heard, err)
	}
	if answer.ID != "ab-7" {
		t.Errorf("answered id %q, want the string id the server asked with", answer.ID)
	}
	if answer.Error == nil {
		t.Fatalf("answered %q with a result, want a refusal: atenea serves no roots", heard)
	}
	if answer.Error.Code != -32601 {
		t.Errorf("refusal code = %d, want -32601 method not found", answer.Error.Code)
	}
	if !strings.Contains(answer.Error.Message, "roots/list") {
		t.Errorf("refusal message = %q, want it to name the method refused", answer.Error.Message)
	}
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

// tail returns everything from key to the end of the text, spaces included.
// field() splits on whitespace, which is right for a scalar and wrong for a
// whole JSON message quoted inside the result.
func tail(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var body struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Content) == 0 {
		t.Fatalf("unreadable result %s: %v", raw, err)
	}
	_, rest, ok := strings.Cut(body.Content[0].Text, key)
	if !ok {
		t.Fatalf("no %s in %q", key, body.Content[0].Text)
	}
	return rest
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

// A process that fell over on its own has to be reaped, not merely forgotten.
//
// ensure noticed the death and set b.proc to nil, which is enough to stop
// handing out a corpse and is not enough to bury one: nothing called Wait, so
// the child stayed in the process table as a zombie for as long as Atenea ran,
// with the three os.File ends of its pipes still open. A backend that crashes
// and respawns leaks one of each per crash, and the state a `kill -0` cannot
// tell apart from a healthy process is exactly the state this leaves behind --
// which is why the check is the same one Close's test uses.
func TestARespawnBuriesTheProcessItReplaces(t *testing.T) {
	dir := t.TempDir()
	ledger := dir + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{
		"HELPER_LEDGER": ledger, "HELPER_DIE_ONCE": dir + "/died",
	})
	if _, err := b.Call(t.Context(), "search_code", nil); err == nil {
		t.Fatal("the helper was asked to fall over and answered instead")
	}
	dead := readPID(t, ledger)
	// The next caller is what makes ensure notice the death and start again.
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("after the death: %v", err)
	}
	if n := lines(t, ledger); n != 2 {
		t.Fatalf("%d processes, want 2: one that died and one that replaced it", n)
	}
	if err := waitGone(dead); err != nil {
		t.Errorf("%v: it exited but was never waited on, so it is a zombie", err)
	}
}

// The process outlives the chat that happened to need it first, and so must
// its handshake.
//
// spawn refuses exec.CommandContext for exactly this reason -- a child bound
// to one call's context is a shared server that quietly becomes a per-chat one
// -- and then handed that same context to the initialize that runs one line
// later. A first caller that pressed ctrl-c during the handshake took down the
// process every other chat was about to share: ensure saw the cancellation as
// a failed handshake and killed a child that was perfectly healthy. The
// caller's own call still fails, because its context really is gone; what must
// not fail is everybody else's.
func TestCancellingTheFirstChatDoesNotKillTheSharedProcess(t *testing.T) {
	ledger := t.TempDir() + "/spawns"
	b := helper(t, []string{"search_code"}, map[string]string{
		"HELPER_LEDGER": ledger, "HELPER_SLOW_INIT": "400ms",
	})
	impatient, giveUp := context.WithCancel(t.Context())
	go func() {
		time.Sleep(80 * time.Millisecond)
		giveUp()
	}()
	if _, err := b.Call(impatient, "search_code", map[string]any{"echo": "one"}); err == nil {
		t.Fatal("a call on a canceled context answered")
	}
	// The chat that arrives next finds the handshake done and the process up.
	raw, err := b.Call(t.Context(), "search_code", map[string]any{"echo": "two"})
	if err != nil {
		t.Fatalf("the second chat found no backend: %v", err)
	}
	if got := field(t, raw, "echo="); got != "two" {
		t.Errorf("echo = %q, want the second chat's own argument", got)
	}
	if n := lines(t, ledger); n != 1 {
		t.Errorf("%d processes were started, want 1: the first chat's cancellation killed the shared one", n)
	}
}

// A backend that writes to stdout without ever framing a line must be cut off
// at a ceiling, not read until the machine runs out of memory.
//
// bufio.Reader.ReadString, which this used, grows a slice until it finds the
// delimiter: the reader's buffer size sets how much is read per syscall and
// nothing sets a roof on one message. The HTTP half of this package has capped
// an answer at maxBody since it was written; the stdio half, which is the one
// Atenea spawns itself and shares between every chat, had no cap at all. The
// symptom is the giveaway: without the ceiling the flood is swallowed whole
// and the answer that follows it is read as part of the same line, so the
// handshake dies on its timeout with no idea why.
func TestAServerThatNeverFramesALineIsCutOff(t *testing.T) {
	b := helper(t, []string{"search_code"}, map[string]string{
		"HELPER_FLOODS": strconv.Itoa(16 << 20),
	})
	start := time.Now()
	_, err := b.Tools(t.Context())
	if err == nil {
		t.Fatal("a backend that never framed a line was reported as listing tools")
	}
	if !strings.Contains(err.Error(), "not framing JSON-RPC") {
		t.Errorf("error = %v, want it to name the framing as the fault", err)
	}
	// The reader gives up at the ceiling rather than at the caller's deadline,
	// and the difference between the two is the whole repair.
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the flood took %v to report: that is the call's timeout, not a ceiling", took)
	}
}

// The routing table belongs to the pipe, not to the backend.
//
// Close drops b.proc and only then calls stop(), so from the moment it
// releases the lock a new chat may spawn a replacement and register calls
// against it. The reader of the process being stopped reaches its own defer
// long afterwards -- with a server that ignores the polite close of stdin, a
// whole procgroup.Grace afterwards -- and a clear() of a table shared by the
// backend abandons the successor's callers at that point: their answers arrive
// to a routing table that no longer has them in it, and the calls die on their
// timeouts with a healthy process on the other end.
func TestStoppingOneProcessDoesNotAbandonTheNextOnesCallers(t *testing.T) {
	b := helper(t, []string{"search_code"}, map[string]string{"HELPER_STUBBORN": "1"})
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("warming the first process: %v", err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		b.Close()
	}()
	// Long enough for Close to have dropped the process and be sitting inside
	// stop(), and far shorter than the grace it will spend there.
	time.Sleep(200 * time.Millisecond)

	// This call spawns the successor. Its answer is deliberately slower than
	// the grace the first process's stop is burning, so the doomed reader
	// reaches its defer while this caller is still waiting.
	start := time.Now()
	raw, err := b.Call(t.Context(), "search_code", map[string]any{"echo": "next", "sleep": "3s"})
	if err != nil {
		t.Fatalf("a call on the successor process failed while it was alive and answering: %v", err)
	}
	if got := field(t, raw, "echo="); got != "next" {
		t.Errorf("echo = %q, want this caller's own argument", got)
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("the answer took %v: it was dropped and re-found rather than routed", took)
	}
	<-stopped
}
