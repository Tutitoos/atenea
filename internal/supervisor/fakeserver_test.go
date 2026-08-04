package supervisor

// The tests in this package watch real processes, not fakes called in
// process: a supervisor's whole job is Start, Wait and Kill on something
// external, and a test double that never left a goroutine would exercise
// none of that. So this file gives every other test file a real, separate
// program to point Spec.Command at -- this same test binary, re-executed
// with an environment variable that tells its own TestMain to become a
// small MCP server instead of running the suite again.
//
// What that server does is controlled entirely by env vars, one knob per
// behavior a test needs: answer a valid initialize, refuse to, take a while
// to come up, run for a bit and then crash, or shrug off SIGTERM the way a
// server with its own cleanup-on-exit logic might. Nothing here is a
// supervisor concern; it is scaffolding for exercising one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("ATENEA_TEST_FAKE_SERVER") == "1" {
		os.Exit(runFakeServer())
	}
	os.Exit(m.Run())
}

// runFakeServer is the entire fake MCP server. It never returns except via
// its own os.Exit or its parent's SIGKILL: normal completion is not a shape
// a supervised server's far side has.
func runFakeServer() int {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if port == "" {
		fmt.Fprintln(os.Stderr, "fake-server: no --port given")
		return 1
	}

	if ms := exitAfterMillis(); ms > 0 {
		code := 1
		if c := os.Getenv("FAKE_EXIT_CODE"); c != "" {
			code, _ = strconv.Atoi(c)
		}
		go func() {
			time.Sleep(ms)
			fmt.Fprintf(os.Stderr, "fake-server: simulated crash (exit %d)\n", code)
			os.Exit(code)
		}()
	}

	// The default disposition for SIGTERM is already "terminate the
	// process", which is the well-behaved case every graceful-stop test
	// relies on: most of these tests need no handler at all. Only the test
	// for the SIGKILL escalation asks this server to swallow SIGTERM,
	// which takes an explicit handler that does nothing.
	if boolEnv("FAKE_IGNORE_TERM") {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		go func() {
			for range c {
				fmt.Fprintln(os.Stderr, "fake-server: got SIGTERM, ignoring")
			}
		}()
	}

	if d := envMillis("FAKE_DELAY_MS"); d > 0 {
		time.Sleep(d)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if boolEnv("FAKE_BAD_INITIALIZE") {
			http.Error(w, "not ready yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"protocolVersion": protocolVersion},
		})
	})
	fmt.Fprintln(os.Stderr, "fake-server: listening on", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, "fake-server: serve error:", err)
		return 1
	}
	return 0
}

// exitAfterMillis decides how long this invocation should live before its
// simulated crash. FAKE_EXIT_AFTER_MS is a single fixed value; a test that
// needs different behavior across the same process's own restarts instead
// sets FAKE_EXIT_AFTER_MS_SEQUENCE, a comma-separated list picked one value
// per invocation by counting invocations in FAKE_STATE_FILE, clamped to the
// last entry once the sequence runs out.
func exitAfterMillis() time.Duration {
	seq := os.Getenv("FAKE_EXIT_AFTER_MS_SEQUENCE")
	if seq == "" {
		return envMillis("FAKE_EXIT_AFTER_MS")
	}
	parts := strings.Split(seq, ",")
	i := nextInvocation(os.Getenv("FAKE_STATE_FILE"))
	if i >= len(parts) {
		i = len(parts) - 1
	}
	ms, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
	return time.Duration(ms) * time.Millisecond
}

// nextInvocation reads an integer counter from path, writes back n+1, and
// returns n. The count has to survive on disk rather than in memory: each
// restart this fake server models really is a separate process.
func nextInvocation(path string) int {
	n := 0
	if raw, err := os.ReadFile(path); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(n+1)), 0600)
	return n
}

func boolEnv(key string) bool { return os.Getenv(key) == "1" }

func envMillis(key string) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

// fakeSpec is a Spec that launches this test binary as the fake server
// above. Timeouts are short throughout: every one of these tests is
// exercising a real process and a real socket, not simulating time, so
// keeping the suite fast means keeping the waits themselves small rather
// than faking a clock.
func fakeSpec(id string, lifecycle Lifecycle, env map[string]string) Spec {
	kv := []string{"ATENEA_TEST_FAKE_SERVER=1"}
	for k, v := range env {
		kv = append(kv, k+"="+v)
	}
	return Spec{
		ID:           id,
		Command:      os.Args[0],
		Args:         []string{"--port", portPlaceholder},
		Env:          kv,
		Lifecycle:    lifecycle,
		EndpointPath: "/mcp",
		ReadyTimeout: 2 * time.Second,
		RestartDelay: 30 * time.Millisecond,
		StopGrace:    300 * time.Millisecond,
		IdleTimeout:  120 * time.Millisecond,
		RestartLimit: 2,
	}
}

// waitFor polls cond until it is true or timeout passes, failing the test on
// timeout. Every state this package reports settles from a background
// goroutine's own timing, not the caller's, so asserting on it means polling
// rather than reading once.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
