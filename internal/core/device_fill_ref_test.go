package core

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/agentdevice"
	"github.com/Tutitoos/atenea/internal/passthrough"
)

type fillRefDispatch struct {
	tool string
	args map[string]any
}

type fillRefDeviceBackend struct {
	passthrough.Backend
	version string
	tools   []passthrough.Tool
	calls   []fillRefDispatch
}

func (b *fillRefDeviceBackend) Version() string        { return b.version }
func (*fillRefDeviceBackend) WorkingDirectory() string { return "/fixture" }
func (b *fillRefDeviceBackend) Tools(context.Context) ([]passthrough.Tool, error) {
	return b.tools, nil
}
func (b *fillRefDeviceBackend) ContractTools(context.Context) (passthrough.CatalogContract, error) {
	return passthrough.CatalogContract{Tools: b.tools, Version: b.version, WorkingDirectory: b.WorkingDirectory()}, nil
}
func (b *fillRefDeviceBackend) CallContract(ctx context.Context, tool string, args map[string]any, _ passthrough.CatalogContract) (json.RawMessage, error) {
	return b.Call(ctx, tool, args)
}
func (b *fillRefDeviceBackend) Call(_ context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	b.calls = append(b.calls, fillRefDispatch{tool, args})
	if tool == "session" {
		return json.RawMessage(`{"structuredContent":{"sessions":[{"name":"flow","device":{"id":"fixture-device","name":"Fixture phone","platform":"ios"}}]}}`), nil
	}
	return json.RawMessage(`{"content":[],"structuredContent":{"ok":true}}`), nil
}

func newFillRefConversation(t *testing.T, version string) (*conversation, *fillRefDeviceBackend) {
	t.Helper()
	backend := &fillRefDeviceBackend{version: version}
	names := []string{"fill", "click"}
	if agentdevice.IsCandidate(version) {
		names = append(names, "session")
	}
	for _, name := range names {
		schema, err := os.ReadFile("../agentdevice/testdata/" + name + "-" + version + ".json")
		if err != nil {
			t.Fatal(err)
		}
		backend.tools = append(backend.tools, passthrough.Tool{Name: name, InputSchema: schema})
	}
	readings, err := newBackendMemory("")
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{readings: readings, backends: map[string]rawBackend{"agent-device": {Backend: backend}}}
	v := &conversation{core: c, session: &Session{id: "fixture-conversation"}}
	c.deviceOwners = map[string]*deviceOwner{
		deviceRealm(nil) + "flow": {conversation: v.session.ID(), device: "fixture-device"},
	}
	return v, backend
}

func TestDeviceFillRefLabelsStopBeforeRawDispatch(t *testing.T) {
	for _, version := range []string{agentdevice.Version, agentdevice.CandidateVersion} {
		t.Run(version, func(t *testing.T) {
			for _, ref := range []string{"@e12", "@e12~s4"} {
				for _, label := range []any{"fixture-private-label", "", nil, false, float64(7)} {
					v, backend := newFillRefConversation(t, version)
					args := map[string]any{
						"session": "flow", "cwd": "/fixture", "text": "fixture-private-text",
						"target": map[string]any{"kind": "ref", "ref": ref, "label": label},
					}
					raw, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					answer, rpcErr := v.rawCall(t.Context(), "agent-device", "fill", toolsCallParams{Name: "raw.agent-device.fill", Arguments: raw})
					if rpcErr != nil {
						t.Fatal(rpcErr)
					}
					result := answer.(map[string]any)
					if result["isError"] != true || result["structuredContent"].(map[string]any)["error_code"] != "INVALID_ARGS" {
						t.Fatalf("fill label was not refused as INVALID_ARGS: %v", result)
					}
					encoded, err := json.Marshal(answer)
					if err != nil || !strings.Contains(string(encoded), "omit target.label") || strings.Contains(string(encoded), "fixture-private-") {
						t.Fatal("fill label refusal missing hint or leaked a private value")
					}
					if len(backend.calls) != 0 {
						t.Fatal("invalid fill reached a session probe or action dispatch")
					}
				}
			}
		})
	}
}

func TestDeviceFillRefWithoutLabelAndClickLabelDispatchUnchanged(t *testing.T) {
	for _, version := range []string{agentdevice.Version, agentdevice.CandidateVersion} {
		t.Run(version, func(t *testing.T) {
			for _, tc := range []struct {
				tool, text string
				target     map[string]any
			}{
				{"fill", "fixture-private-text", map[string]any{"kind": "ref", "ref": "@e12"}},
				{"fill", "", map[string]any{"kind": "ref", "ref": "@e12~s4"}},
				{"click", "", map[string]any{"kind": "ref", "ref": "@e12~s4", "label": "fixture-label"}},
			} {
				v, backend := newFillRefConversation(t, version)
				args := map[string]any{"session": "flow", "cwd": "/fixture", "target": tc.target}
				if tc.tool == "fill" {
					args["text"] = tc.text
				}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				answer, rpcErr := v.rawCall(t.Context(), "agent-device", tc.tool, toolsCallParams{Name: "raw.agent-device." + tc.tool, Arguments: raw})
				if rpcErr != nil || answer.(map[string]any)["isError"] == true {
					t.Fatalf("valid %s did not reach action dispatch: %v %v", tc.tool, answer, rpcErr)
				}
				if len(backend.calls) != 2 || backend.calls[0].tool != "session" || backend.calls[1].tool != tc.tool {
					t.Fatalf("valid %s did not follow session preflight then action", tc.tool)
				}
				if agentdevice.IsCandidate(version) {
					delete(args, "cwd") // The existing candidate-only wire adaptation.
				}
				if !reflect.DeepEqual(backend.calls[1].args, args) {
					t.Fatal("valid action arguments changed beyond the existing cwd adaptation")
				}
			}
		})
	}
}
