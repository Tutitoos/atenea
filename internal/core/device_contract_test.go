package core

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type contractOnlyDeviceBackend struct {
	passthrough.Backend
	tool    string
	schema  json.RawMessage
	version string
}

func (b contractOnlyDeviceBackend) Tools(context.Context) ([]passthrough.Tool, error) {
	return []passthrough.Tool{{Name: b.tool, InputSchema: b.schema}}, nil
}
func (b contractOnlyDeviceBackend) Version() string { return b.version }
func (b contractOnlyDeviceBackend) Call(context.Context, string, map[string]any) (json.RawMessage, error) {
	panic("invalid contract reached device dispatch")
}

func TestDeviceOpenClickFillInvalidContractsStopBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"open", map[string]any{"session": "flow", "cwd": "/fixture"}},
		{"click", map[string]any{"session": "flow", "cwd": "/fixture", "target": map[string]any{"kind": "ref", "ref": "old"}}},
		{"fill", map[string]any{"session": "flow", "cwd": "/fixture", "target": map[string]any{"kind": "ref", "ref": "@e1"}}},
	} {
		schema, err := os.ReadFile("../agentdevice/testdata/" + tc.tool + "-0.20.10.json")
		if err != nil {
			t.Fatal(err)
		}
		v := &conversation{}
		backend := contractOnlyDeviceBackend{tool: tc.tool, schema: schema, version: "0.20.10"}
		if err := v.validateDeviceCall(t.Context(), rawBackend{Backend: backend}, tc.tool, tc.args); contract.CodeOf(err) != "INVALID_ARGS" {
			t.Fatalf("%s invalid contract = %v", tc.tool, err)
		}
		if _, present := tc.args["device"]; present {
			t.Fatal("validation changed arguments")
		}
		backend.schema = json.RawMessage(`{"type":"object"}`)
		if err := v.validateDeviceCall(t.Context(), rawBackend{Backend: backend}, tc.tool, tc.args); contract.CodeOf(err) != "compatibility_unverified" {
			t.Fatalf("%s drift = %v", tc.tool, err)
		}
	}
}

type catalogOnlyDeviceBackend struct {
	passthrough.Backend
	tools              []passthrough.Tool
	version, workspace string
}

func (b catalogOnlyDeviceBackend) Tools(context.Context) ([]passthrough.Tool, error) {
	return b.tools, nil
}
func (b catalogOnlyDeviceBackend) Version() string {
	if b.version == "" {
		return "0.20.10"
	}
	return b.version
}
func (b catalogOnlyDeviceBackend) WorkingDirectory() string { return b.workspace }
func (b catalogOnlyDeviceBackend) ContractTools(context.Context) (passthrough.CatalogContract, error) {
	return passthrough.CatalogContract{Tools: b.tools, Version: b.Version(), WorkingDirectory: b.workspace}, nil
}
func (catalogOnlyDeviceBackend) CallContract(context.Context, string, map[string]any, passthrough.CatalogContract) (json.RawMessage, error) {
	panic("fixture contract must not dispatch an action")
}
func (catalogOnlyDeviceBackend) Call(context.Context, string, map[string]any) (json.RawMessage, error) {
	panic("tools/list fixture must not dispatch a device action")
}

func TestDeviceToolsListAdvertisesOnlyVerifiedContract(t *testing.T) {
	tools := make([]passthrough.Tool, 0, 4)
	for _, name := range []string{"open", "click", "fill"} {
		schema, err := os.ReadFile("../agentdevice/testdata/" + name + "-0.20.10.json")
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, passthrough.Tool{Name: name, Description: "upstream " + name, InputSchema: schema})
	}
	unrelated := json.RawMessage(`{"type":"object","properties":{"probe":{"type":"string"}},"required":["probe"]}`)
	tools = append(tools, passthrough.Tool{Name: "devices", Description: "upstream devices", InputSchema: unrelated})
	backend := catalogOnlyDeviceBackend{tools: tools}
	readings, err := newBackendMemory("")
	if err != nil {
		t.Fatal(err)
	}
	core := &Core{
		catalog: registry.New(), readings: readings,
		backends: map[string]rawBackend{"agent-device": {Backend: backend}},
	}
	v := &conversation{core: core, session: &Session{id: "fixture-flow"}, policy: desktopPolicy{RawCatalogs: map[string]string{"agent-device": "core"}}}
	listed := func() map[string]map[string]any {
		t.Helper()
		value, rpcErr := v.toolsList(t.Context())
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		rows := value.(map[string]any)["tools"].([]map[string]any)
		byName := map[string]map[string]any{}
		for _, row := range rows {
			byName[row["name"].(string)] = row
		}
		return byName
	}
	first := listed()
	for _, name := range []string{"open", "click", "fill"} {
		entry := first["raw.agent-device."+name]
		if entry == nil {
			t.Fatalf("%s absent from tools/list", name)
		}
		schema := entry["inputSchema"].(map[string]any)
		required := schema["required"].([]any)
		if !containsSchemaField(required, "session") || !containsSchemaField(required, "cwd") {
			t.Fatalf("%s omits session/cwd: %v", name, required)
		}
		if name == "open" && schema["anyOf"] == nil {
			t.Fatal("open omits explicit device selector")
		}
		if name == "fill" && !containsSchemaField(required, "text") {
			t.Fatal("fill omits text")
		}
	}
	wantUnrelated := normalizeDesktopSchema(unrelated)
	if got := first["raw.agent-device.devices"]; got == nil || got["description"] != "upstream devices" || !reflect.DeepEqual(got["inputSchema"], wantUnrelated) {
		t.Fatalf("unrelated raw tool changed: %v", got)
	}
	// A single upstream change affects only that tool's listing; no call is made.
	tools[1].InputSchema = json.RawMessage(`{"type":"object"}`)
	second := listed()
	if second["raw.agent-device.click"] != nil {
		t.Fatal("drifted click was advertised")
	}
	for _, name := range []string{"open", "fill", "devices"} {
		if second["raw.agent-device."+name] == nil {
			t.Fatalf("%s disappeared after click drift", name)
		}
	}
}

func containsSchemaField(fields []any, name string) bool {
	for _, field := range fields {
		if field == name {
			return true
		}
	}
	return false
}
