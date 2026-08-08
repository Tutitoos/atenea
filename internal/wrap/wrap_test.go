package wrap_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/wrap"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const okResult = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",` +
	`"serverInfo":{"name":"fake","version":"9.9.9"}}}`

// live returns the URL of a server that completes the handshake.
func live(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, okResult)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// dead returns the URL of a server that is not there. Started and stopped so
// the port is real and unclaimed, which is the shape of the failure this
// whole package exists for -- a declaration that was true once.
func dead(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func payload(t *testing.T, plan wrap.Plan) map[string]map[string]any {
	t.Helper()
	raw, err := plan.OpenCodePayload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	var out struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("payload is not JSON: %v: %s", err, raw)
	}
	return out.MCP
}

// The safety property this package rests on. Atenea declining to vouch for a
// server must not be the reason the client loses one: OpenCode deep-merges
// the payload, so an absent key leaves the user's own entry alone, while an
// `enabled:false` would reach in and switch off something that might work
// perfectly well without Atenea in the picture.
func TestARefusedServerIsAbsentRatherThanDisabled(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "gone", URL: dead(t), Timeout: 2 * time.Second},
	})
	if len(plan.Refused) != 1 {
		t.Fatalf("refused = %d, want the dead server refused", len(plan.Refused))
	}
	servers := payload(t, plan)
	if entry, ok := servers["gone"]; ok {
		t.Errorf("the refused server is in the payload as %v; it must be absent", entry)
	}
}

// The other half: a server that answered is declared, and it is declared as
// the endpoint that answered rather than anything reconstructed afterwards.
func TestALiveServerIsDeclaredAtTheAddressThatAnswered(t *testing.T) {
	url := live(t)
	plan := wrap.Check(t.Context(), []config.MCPServer{{ID: "up", URL: url}})
	if len(plan.Declared) != 1 {
		t.Fatalf("declared = %d, want 1: %v", len(plan.Declared), plan.Refused)
	}
	entry, ok := payload(t, plan)["up"]
	if !ok {
		t.Fatal("the live server is missing from the payload")
	}
	if entry["type"] != "remote" || entry["url"] != url {
		t.Errorf("entry = %v, want a remote at %s", entry, url)
	}
}

// A raw backend is the one case where handing a client the address would undo
// the work: the client would reach it directly, under no allow list and no
// effect check, and the budget would be routed around by the very command
// that exists to apply it. It is checked and reported, and it stays out of
// the payload whether or not it answered.
func TestARawBackendIsHeldRatherThanHandedToTheClient(t *testing.T) {
	url := live(t)
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "pointer", URL: url},
		{
			ID: "held", URL: url, Expose: config.ExposeRaw,
			Tools: []string{"scan"}, Effects: []contract.Effect{contract.EffectRead},
		},
	})
	if len(plan.Held) != 1 || plan.Held[0].Server.ID != "held" {
		t.Fatalf("held = %v, want the raw backend", plan.Held)
	}
	if len(plan.Declared) != 1 || plan.Declared[0].Server.ID != "pointer" {
		t.Fatalf("declared = %v, want only the pointer", plan.Declared)
	}
	servers := payload(t, plan)
	if entry, ok := servers["held"]; ok {
		t.Errorf("a raw backend was handed to the client as %v: the budget is bypassable", entry)
	}
	if _, ok := servers["pointer"]; !ok {
		t.Error("the pointer stopped being declared; only raw backends are held")
	}
	// An operator diffing this report against their client's tool list has
	// to find the server they declared, not an unexplained absence.
	var report strings.Builder
	plan.Report(&report, "opencode")
	if got := report.String(); !strings.Contains(got, "held") || !strings.Contains(got, "raw.held.<tool>") {
		t.Errorf("report does not account for the held backend:\n%s", got)
	}
}

// A merge is key by key, so a key present with a zero value is not the same
// as a key absent: `"command": []` would overwrite a real command in the
// user's own config with nothing. The unused half of the shape must not be
// serialized at all.
func TestTheUnusedHalfOfAnEntryIsNotSerialized(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{{ID: "up", URL: live(t)}})
	entry := payload(t, plan)["up"]
	for _, key := range []string{"command", "environment"} {
		if _, ok := entry[key]; ok {
			t.Errorf("a remote entry carries %q; a merge would overwrite the user's value", key)
		}
	}
}

// A stdio server is declared by the command that was run, not by a name that
// happens to be on PATH somewhere else at launch time.
//
// The fake reads the request before answering, the way a server does. One
// that answers and exits immediately loses a race with the probe's own write
// and fails with a broken pipe -- correctly, since a server that closed
// stdin before being asked cannot be asked, but it is not the thing under
// test here.
//
// Its reply comes from the declared env rather than from the command line,
// so the child could not answer at all unless the environment reached it.
func TestAStdioServerIsDeclaredAsLocalWithItsCommand(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{{
		ID:      "sh",
		Command: []string{"sh", "-c", `read -r _; printf '%s\n' "$REPLY_JSON"; cat >/dev/null`},
		Env:     map[string]string{"K": "v", "REPLY_JSON": okResult},
	}})
	if len(plan.Declared) != 1 {
		t.Fatalf("declared = %d, want the stdio server: %v", len(plan.Declared), plan.Refused)
	}
	entry := payload(t, plan)["sh"]
	if entry["type"] != "local" {
		t.Errorf("type = %v, want local", entry["type"])
	}
	command, _ := entry["command"].([]any)
	if len(command) != 3 || command[0] != "sh" {
		t.Errorf("command = %v, want the declared argv", entry["command"])
	}
	env, _ := entry["environment"].(map[string]any)
	if env["K"] != "v" {
		t.Errorf("environment = %v, want the declared env", entry["environment"])
	}
}

// The reason is the only thing in the report that is not already in the
// settings file. A refusal that names the server but not why it was refused
// is the silent-warning failure this replaces, one indent deeper.
func TestARefusalCarriesItsReason(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{
		{ID: "gone", URL: dead(t), Timeout: 2 * time.Second},
	})
	var report strings.Builder
	plan.Report(&report, "opencode")
	got := report.String()
	if !strings.Contains(got, "refused") || !strings.Contains(got, "gone") {
		t.Fatalf("report does not name the refusal:\n%s", got)
	}
	reason := plan.Refused[0].Result.Err.Error()
	if !strings.Contains(got, reason) {
		t.Errorf("report omits the reason %q:\n%s", reason, got)
	}
}

// The all-green run is where the word `declared` does the most work and gets
// the least scrutiny: "5 declared, 0 refused" reads as "everything works",
// and what was actually measured is one handshake per server. A server can
// answer initialize and have every tool fail on the first call -- that
// happened to semgrep on the machine this was built on, for days, while it
// reported healthy. So the qualification is not conditional on bad news.
func TestTheReportSaysWhatDeclaredAttestsToEvenWhenNothingFailed(t *testing.T) {
	plan := wrap.Check(t.Context(), []config.MCPServer{{ID: "up", URL: live(t)}})
	if len(plan.Refused) != 0 {
		t.Fatalf("wanted an all-green plan, got refusals: %v", plan.Refused)
	}
	var report strings.Builder
	plan.Report(&report, "opencode")
	got := report.String()
	if !strings.Contains(got, "handshake") {
		t.Errorf("an all-green report does not say what it checked:\n%s", got)
	}
	if !strings.Contains(got, "not that its tools work") {
		t.Errorf("an all-green report does not say what it did NOT check:\n%s", got)
	}
}

// Two runs against one machine must produce one payload. A diff between wrap
// runs should mean the world changed, never that a map was walked twice.
func TestTheOrderDoesNotDependOnDeclarationOrder(t *testing.T) {
	a, b := live(t), live(t)
	forward := wrap.Check(t.Context(), []config.MCPServer{{ID: "aa", URL: a}, {ID: "bb", URL: b}})
	backward := wrap.Check(t.Context(), []config.MCPServer{{ID: "bb", URL: b}, {ID: "aa", URL: a}})
	if forward.Declared[0].Server.ID != "aa" || backward.Declared[0].Server.ID != "aa" {
		t.Errorf("order = %q then %q, want both sorted",
			forward.Declared[0].Server.ID, backward.Declared[0].Server.ID)
	}
	first, _ := forward.OpenCodePayload()
	second, _ := backward.OpenCodePayload()
	if first != second {
		t.Errorf("payloads differ by declaration order:\n%s\n%s", first, second)
	}
}

// Declaring nothing is a legitimate state, and it has to be said out loud:
// silence would read as "wrap ran and everything is fine" when what happened
// is that wrap had nothing to check.
func TestNothingDeclaredSaysTheClientIsUnchanged(t *testing.T) {
	plan := wrap.Check(t.Context(), nil)
	var report strings.Builder
	plan.Report(&report, "opencode")
	if !strings.Contains(report.String(), "unchanged") {
		t.Errorf("report = %q, want it to say the client is unchanged", report.String())
	}
	raw, err := plan.OpenCodePayload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if raw != `{"mcp":{}}` {
		t.Errorf("payload = %s, want an empty mcp object that merges to nothing", raw)
	}
}
