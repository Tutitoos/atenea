package core_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/toolstats"
)

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
func TestStatsCLIInvalidCapabilityAndCancellation(t *testing.T) {
	atenea, _ := measured(t, "")
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
