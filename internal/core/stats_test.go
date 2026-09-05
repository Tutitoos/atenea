package core_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/toolstats"
)

// statsTotal retrieves one accounting-level total from a core snapshot.
func statsTotal(t *testing.T, s toolstats.Snapshot, level string) toolstats.Row {
	t.Helper()
	for _, r := range s.Totals {
		if r.Level == level {
			return r
		}
	}
	t.Fatalf("no total for %s", level)
	return toolstats.Row{}
}

// TestStatsRawAliasRefusalAndReadOnlyQueries covers normalized aliases, refused calls, and routing-metric isolation.
func TestStatsRawAliasRefusalAndReadOnlyQueries(t *testing.T) {
	fake := &fakeBackend{}
	backend := httptest.NewServer(fake)
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()
	// The stats method does not require initialize and does not discover upstream tools.
	before, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if statsTotal(t, before, "request").Calls != 0 {
		t.Fatal("stats counted itself")
	}
	client := dial(t)
	client.handshake("stats-test")
	result(t, client.call("tools/list", nil), "list")
	result(t, client.call("tools/call", map[string]any{"name": "raw/semgrep/semgrep_scan", "arguments": map[string]any{"code_files": []any{}}}), "raw")
	result(t, client.call("tools/call", map[string]any{"name": "raw.semgrep.semgrep_fix", "arguments": map[string]any{}}), "denied")
	after, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"request", "attempt"} {
		r := statsTotal(t, after, level)
		if r.Calls != 2 || r.OK != 1 || r.Refused != 1 {
			t.Fatalf("%s %+v", level, r)
		}
	}
	for _, r := range after.Rows {
		if r.Name == "raw/semgrep/semgrep_scan" {
			t.Fatal("alias created another row")
		}
	}
	fake.mu.Lock()
	calls := len(fake.calls)
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("upstream calls=%d", calls)
	}
	again, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if statsTotal(t, again, "request").Calls != 2 {
		t.Fatal("stats generated activity")
	}
	baseline, err := atenea.Measurements(before.At)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 0 {
		t.Fatal("raw statistics polluted routing baseline")
	}
}

// TestStatsCapabilityRequestIsNotCountedTwice checks request propagation through MCP dispatch.
func TestStatsCapabilityRequestIsNotCountedTwice(t *testing.T) {
	atenea := buildService(t, socketSettings)
	defer serve(t, atenea)()
	client := dial(t)
	client.handshake("stats-test")
	client.call("tools/call", map[string]any{"name": "code.search", "arguments": map[string]any{"query": "TODO"}})
	out, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if r := statsTotal(t, out, "request"); r.Calls != 1 {
		t.Fatalf("request %+v", r)
	}
	if r := statsTotal(t, out, "attempt"); r.Calls != 1 {
		t.Fatalf("attempt %+v", r)
	}
}

// TestStatsCLIInvalidCapabilityAndCancellation checks recording before capability validation.
func TestStatsCLIInvalidCapabilityAndCancellation(t *testing.T) {
	atenea, base := measured(t, "")
	if err := os.Chmod(filepath.Dir(base), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = atenea.Shutdown() }()
	_, _ = atenea.Ask(context.Background(), orchestrator.Question{Capability: "missing", Repository: "api"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = atenea.Ask(ctx, orchestrator.Question{Capability: "code.search", Repository: "api", Payload: map[string]any{"query": "TODO"}})
	out, err := atenea.Stats(context.Background(), toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	r := statsTotal(t, out, "request")
	if r.Calls != 2 || r.Fail < 1 {
		t.Fatalf("%+v", r)
	}
}

// TestStatsTaskRepositoryAttribution rejects first-repository attribution for global tasks.
func TestStatsTaskRepositoryAttribution(t *testing.T) {
	atenea, base := measured(t, "")
	if err := os.Chmod(filepath.Dir(base), 0700); err != nil {
		t.Fatal(err)
	}
	// A stopped core records the rejected request without executing providers.
	if err := atenea.Shutdown(); err != nil {
		t.Fatal(err)
	}
	for _, repos := range [][]string{nil, {"api", "scripts"}, {"api"}} {
		_, err := atenea.Do(context.Background(), orchestrator.Task{Text: "stats attribution test", Repositories: repos})
		if err == nil {
			t.Fatal("stopped core accepted task")
		}
	}
	for _, tc := range []struct {
		repo  string
		calls int64
	}{{"", 3}, {"api", 1}, {"scripts", 0}} {
		out, err := atenea.Stats(context.Background(), toolstats.Query{Repository: tc.repo})
		if err != nil {
			t.Fatal(err)
		}
		if r := statsTotal(t, out, "request"); r.Calls != tc.calls {
			t.Fatalf("repo=%q: %+v", tc.repo, r)
		}
	}
}

func TestRawFunctionalFailureAgreesWithReceiptAndStats(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{resultOverride: `{"content":[{"type":"text","text":"Invalid condition"}],"isError":true,"structuredContent":{"error_code":"INVALID_ARGS"}}`})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()
	c := dial(t)
	c.handshake("stats-test")
	got := result(t, c.call("tools/call", map[string]any{"name": "raw.semgrep.semgrep_scan", "arguments": map[string]any{"code_files": []any{}}}), "raw error")
	if got["isError"] != true {
		t.Fatal(got)
	}
	run := onlyRun(t, atenea)
	if run.Verdict != "failed" || run.ErrorCode != "INVALID_ARGS" || run.RequestID == "" || run.AttemptID == "" {
		t.Fatalf("receipt=%+v", run)
	}
	snapshot, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"request", "attempt"} {
		if r := statsTotal(t, snapshot, level); r.Fail != 1 || r.OK != 0 {
			t.Fatalf("%s: %+v", level, r)
		}
	}
	if len(snapshot.Errors) != 1 || snapshot.Errors[0].Code != "INVALID_ARGS" {
		t.Fatal(snapshot.Errors)
	}
}

// A backend's own timeout may expire while the client context is still live.
// The response must use the same cause as the receipt and statistics.
func TestRawBackendTimeoutAgreesAcrossResponseReceiptAndStats(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{blockCalls: true})
	defer backend.Close()
	settings := strings.Replace(rawSettings(t, backend.URL), "\nurl =", "\ntimeout = \"100ms\"\nurl =", 1)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()
	c := dial(t)
	c.handshake("timeout-audit")
	result(t, c.call("tools/list", nil), "catalog")
	got := result(t, c.call("tools/call", map[string]any{"name": "raw.semgrep.semgrep_scan", "arguments": map[string]any{"code_files": []any{}}}), "timeout")
	metadata, ok := got["structuredContent"].(map[string]any)
	if !ok || metadata["error_code"] != "timeout" {
		t.Fatalf("response=%+v", got)
	}
	run := onlyRun(t, atenea)
	if run.ErrorCode != "timeout" || run.Verdict != "failed" {
		t.Fatalf("receipt=%+v", run)
	}
	snapshot, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Errors) != 1 || snapshot.Errors[0].Code != "timeout" {
		t.Fatal(snapshot.Errors)
	}
}
