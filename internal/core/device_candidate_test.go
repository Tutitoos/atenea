package core

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Tutitoos/atenea/internal/agentdevice"
	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type baselineNoListingDeviceBackend struct{ passthrough.Backend }

func (baselineNoListingDeviceBackend) Version() string { return agentdevice.Version }
func (baselineNoListingDeviceBackend) Tools(context.Context) ([]passthrough.Tool, error) {
	panic("baseline unguarded tool must not request a catalog")
}

func TestBaselineUnguardedCallsKeepTheirListingIndependence(t *testing.T) {
	backend := rawBackend{Backend: baselineNoListingDeviceBackend{}}
	for _, name := range []string{"snapshot", "screenshot", "type", "press", "back", "home", "scroll", "swipe", "close"} {
		if err := validateDeviceContract(t.Context(), backend, name, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
}

func candidateDeviceTools(t *testing.T, names ...string) []passthrough.Tool {
	t.Helper()
	var tools []passthrough.Tool
	for _, name := range names {
		raw, err := os.ReadFile("../agentdevice/testdata/" + name + "-0.21.23.json")
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, passthrough.Tool{Name: name, InputSchema: raw})
	}
	return tools
}

func TestCandidateWorkspaceAndSchemaRefusalsPrecedeDeviceDispatch(t *testing.T) {
	backend := catalogOnlyDeviceBackend{version: agentdevice.CandidateVersion, workspace: "/fixture", tools: candidateDeviceTools(t, "open", "snapshot", "session")}
	for _, tc := range []struct{ name, version, workspace, cwd, want string }{
		{"open", agentdevice.CandidateVersion, "/fixture", "/fixture", ""},
		{"open", agentdevice.CandidateVersion, "", "/fixture", "compatibility_unverified"},
		{"snapshot", agentdevice.CandidateVersion, "/fixture", "/other", "INVALID_ARGS"},
		{"open", "0.21.24", "/fixture", "/fixture", "compatibility_unverified"},
		{"batch", agentdevice.CandidateVersion, "/fixture", "/fixture", "compatibility_unverified"},
	} {
		b := backend
		b.version = tc.version
		b.workspace = tc.workspace
		args := map[string]any{"cwd": tc.cwd, "session": "flow"}
		if tc.name == "open" {
			args["serial"] = "fixture-device"
		}
		err := validateDeviceContract(t.Context(), rawBackend{Backend: b}, tc.name, args)
		if (tc.want == "" && err != nil) || (tc.want != "" && contract.CodeOf(err) != tc.want) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	backend.tools[2].InputSchema = json.RawMessage(`{"type":"object"}`)
	if err := validateDeviceContract(t.Context(), rawBackend{Backend: backend}, "session", deviceSessionListArgs(nil)); contract.CodeOf(err) != "compatibility_unverified" {
		t.Fatalf("session preflight drift accepted: %v", err)
	}
	backend.tools = candidateDeviceTools(t, "snapshot")
	v := &conversation{deviceContext: map[string]any{"session": "flow", "cwd": "/fixture", "stateDir": "old-realm"}}
	if err := v.validateDeviceCall(t.Context(), rawBackend{Backend: backend}, "snapshot", map[string]any{"cwd": "/fixture", "session": "flow"}); contract.CodeOf(err) != "INVALID_ARGS" {
		t.Fatalf("old injected realm accepted: %v", err)
	}
}

func TestCandidateListingWithholdsUnqualifiedToolsAndUnboundWorkspace(t *testing.T) {
	backend := catalogOnlyDeviceBackend{version: agentdevice.CandidateVersion, workspace: "/fixture", tools: candidateDeviceTools(t, "open", "click", "fill", "devices", "snapshot")}
	backend.tools = append(backend.tools, passthrough.Tool{Name: "batch", InputSchema: json.RawMessage(`{"type":"object"}`)}, passthrough.Tool{Name: "action-button", InputSchema: json.RawMessage(`{"type":"object"}`)})
	readings, err := newBackendMemory("")
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{catalog: registry.New(), readings: readings, backends: map[string]rawBackend{"agent-device": {Backend: backend}}}
	v := &conversation{core: c, session: &Session{id: "fixture"}, policy: desktopPolicy{RawCatalogs: map[string]string{"agent-device": "full"}}}
	listed := func() map[string]bool {
		t.Helper()
		answer, rpcErr := v.toolsList(t.Context())
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		names := map[string]bool{}
		for _, row := range answer.(map[string]any)["tools"].([]map[string]any) {
			names[row["name"].(string)] = true
		}
		return names
	}
	first := listed()
	if !first["raw.agent-device.open"] || !first["raw.agent-device.snapshot"] || first["raw.agent-device.batch"] || first["raw.agent-device.action-button"] {
		t.Fatalf("candidate catalog widened or incomplete: %v", first)
	}
	backend.workspace = ""
	c.backends["agent-device"] = rawBackend{Backend: backend}
	if second := listed(); second["raw.agent-device.open"] || second["raw.agent-device.snapshot"] {
		t.Fatal("unbound child workspace advertised")
	}
}
