package core_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// These tests are about a silence, not a crash, so each one asserts both
// halves of it: what the client got, and what the operator was told. Either
// half alone is the bug -- tools quietly missing while the screen looks calm
// is exactly what shipped, and a red row nobody can act on is not much better.
//
// The incident: three declared servers were dead for hours because the service
// booted with a PATH that had no ~/.local/bin in it. tools/list dropped their
// tools and said nothing, and every hand-run check passed because a shell has a
// different PATH than a service.

// deadServer declares one raw stdio backend with the command given and nothing
// else that could answer, so a listing either carries its tools or is silent.
func deadServer(t *testing.T, id, command string) string {
	t.Helper()
	return socketSettings + fmt.Sprintf("\n[[mcp_server]]\nid = %q\ncommand = [%q]\n"+
		"expose = \"raw\"\ntools = [\"search_code\"]\neffects = [\"read\"]\n", id, command)
}

// rowFor finds a server on the status screen by id. A missing row is a failure
// of the test's own premise -- the screen must list every declaration -- so it
// is fatal rather than a skipped assertion.
func rowFor(t *testing.T, atenea *core.Core, id string) core.ServerStatus {
	t.Helper()
	for _, s := range atenea.Status().Servers {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no row for %q on the status screen: %+v", id, atenea.Status().Servers)
	return core.ServerStatus{}
}

// Test A: a declared command that does not exist. The tools must be absent
// from the listing -- that part already worked, silently -- and the screen must
// say failed with a reason, which is the part that did not exist.
//
// The name is kept short on purpose: t.TempDir() carries it into the socket
// path, and a unix socket dies past 103 bytes.
func TestDeadCommandIsAbsentAndRedOnScreen(t *testing.T) {
	atenea := buildService(t, deadServer(t, "codebase-memory", "/nonexistent/codebase-memory-mcp"))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if name, _ := tool["name"].(string); strings.HasPrefix(name, "raw.codebase-memory.") {
			t.Errorf("a backend that cannot spawn offered %q", name)
		}
	}
	// The catalog has to survive the failure: a broken backend that took the
	// whole listing down with it would be a different bug wearing this one's
	// clothes.
	if len(tools) == 0 {
		t.Error("tools/list is empty; the catalog went down with the backend")
	}

	row := rowFor(t, atenea, "codebase-memory")
	if row.State != core.BackendFailed {
		t.Errorf("state = %q, want %q", row.State, core.BackendFailed)
	}
	if row.Reason == "" {
		t.Error("Reason is empty: a red row an operator cannot act on is the silence again")
	}
	if row.LastChecked.IsZero() {
		t.Error("LastChecked is the zero time on a row that was checked")
	}
}

// Test B, the raw half of what actually happened: the command is a bare name
// and it is not on the PATH this process runs with. The reason has to name the
// command, because "failed" alone sent me reading configs for hours.
func TestABareNameNotOnPATHFailsWithAReasonThatNamesIt(t *testing.T) {
	const missing = "atenea-test-no-such-backend"
	atenea := buildService(t, deadServer(t, "context7", missing))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if name, _ := tool["name"].(string); strings.HasPrefix(name, "raw.context7.") {
			t.Errorf("a backend that is not on PATH offered %q", name)
		}
	}

	row := rowFor(t, atenea, "context7")
	if row.State != core.BackendFailed {
		t.Fatalf("state = %q, want %q (reason = %q)", row.State, core.BackendFailed, row.Reason)
	}
	if !strings.Contains(row.Reason, missing) {
		t.Errorf("reason = %q, want the command name %q in it", row.Reason, missing)
	}
}

// Test C: a server nobody has exercised reads unknown, never ok. This is the
// distinction the whole record exists for, and the one a future "default it to
// healthy" change would quietly delete.
//
// The struct says nothing else on purpose -- no reason, no timestamp -- because
// there is nothing to say. Making that emptiness unreadable as health is the
// renderer's job, and cmd/atenea tests it there.
func TestUnexercisedServerReadsUnknownNotOK(t *testing.T) {
	atenea := build(t, deadServer(t, "headroom", "/nonexistent/headroom"))

	row := rowFor(t, atenea, "headroom")
	if row.State != core.BackendUnknown {
		t.Errorf("state = %q, want %q: nobody has asked this server anything", row.State, core.BackendUnknown)
	}
	if row.Reason != "" {
		t.Errorf("reason = %q, want empty: no exchange happened to have a cause", row.Reason)
	}
	if !row.LastChecked.IsZero() {
		t.Error("LastChecked carries a timestamp for a check that never happened")
	}
}

// Test B for a non-raw server -- the case that literally bit codebase-memory,
// whose four capabilities vanished while nothing was exposed as raw.
//
// It is deliberately two halves joined at the seam the orchestrator uses,
// because a non-raw server is never asked for tools and so can never have a
// first-hand reading: the adapter classifies the failed spawn (proved in
// internal/adapter/codebasememory, where a bare name off PATH returns
// FailureUnavailable naming PATH), the orchestrator records that against the
// implementation, and this test asserts the half that was missing -- that a
// recorded unavailable for an implementation reaches the row of the server its
// provider names.
//
// Writing this as one dispatch through the real adapter would need
// codebase-memory-mcp installed on whatever machine runs the suite, which is
// how a test starts passing for the wrong reason on CI.
func TestANonRawServerGoesRedFromItsProvidersRecordedHealth(t *testing.T) {
	settings := catalog + "\n[[mcp_server]]\nid = \"codebase-memory\"\n" +
		"command = [\"codebase-memory-mcp\"]\nexpose = \"off\"\n"
	atenea := build(t, settings)

	if row := rowFor(t, atenea, "codebase-memory"); row.State != core.BackendUnknown {
		t.Fatalf("state = %q before anything ran, want %q", row.State, core.BackendUnknown)
	}

	// What the orchestrator does when a call comes back unavailable, with the
	// adapter's own words for the cause.
	const reason = `codebase-memory-mcp is not installed: "codebase-memory-mcp" is not on PATH`
	if err := atenea.Registry().SetHealth("api", "graph.search", contract.Health{
		State:  contract.HealthDown,
		Reason: reason,
	}); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}

	row := rowFor(t, atenea, "codebase-memory")
	if row.State != core.BackendFailed {
		t.Errorf("state = %q, want %q: the provider's implementation is down", row.State, core.BackendFailed)
	}
	if !strings.Contains(row.Reason, "not on PATH") {
		t.Errorf("reason = %q, want the adapter's cause carried through", row.Reason)
	}
	// The implementation that spoke has to travel with the reason: a provider
	// with several of them owes the reader the name of the one that failed.
	if !strings.Contains(row.Reason, "graph.search") {
		t.Errorf("reason = %q, want the implementation name in it", row.Reason)
	}
}
