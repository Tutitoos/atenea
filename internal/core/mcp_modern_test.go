package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/mcpcompat"
)

func modernParams(client string, capabilities map[string]any) map[string]any {
	return map[string]any{
		"_meta": map[string]any{
			mcpcompat.ProtocolVersionKey:    mcpcompat.Modern.String(),
			mcpcompat.ClientInfoKey:         map[string]any{"name": client, "version": "test"},
			mcpcompat.ClientCapabilitiesKey: capabilities,
		},
	}
}

func TestLegacyHandshakeAndModernDiscoverCoexist(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	legacy := dial(t)
	legacyResult := result(t, legacy.handshake("legacy-client"), "legacy initialize")
	if legacyResult["protocolVersion"] != mcpVersion {
		t.Fatalf("legacy protocol = %v, want %s", legacyResult["protocolVersion"], mcpVersion)
	}

	modern := dial(t)
	legacyDiscover := modern.call(core.MethodDiscover, nil)
	if errObj, ok := legacyDiscover["error"].(map[string]any); !ok || errObj["code"] != float64(-32601) {
		t.Fatalf("legacy discover classification = %v", legacyDiscover)
	}
	discovered := result(t, modern.call(core.MethodDiscover, modernParams("codex", map[string]any{})), "server/discover")
	if _, exists := discovered["preferred"]; exists {
		t.Fatalf("discover emitted removed preferred field: %v", discovered["preferred"])
	}
	versions, ok := discovered["supportedVersions"].([]any)
	if !ok || len(versions) != 1 || versions[0] != mcpcompat.Modern.String() {
		t.Fatalf("supported versions = %v", discovered["supportedVersions"])
	}
	meta, ok := discovered["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("discover has no _meta: %v", discovered)
	}
	if _, exists := discovered["serverInfo"]; exists {
		t.Fatalf("discover leaked serverInfo into the result body: %v", discovered)
	}
	serverInfo, ok := meta[mcpcompat.ServerInfoKey].(map[string]any)
	if !ok || serverInfo["name"] != "atenea" {
		t.Fatalf("discover serverInfo = %v", meta["serverInfo"])
	}
}

func TestModernRequestsNeedMetadataAndReturnServerInfo(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	missing := c.call(core.MethodToolsList, nil)
	if errObj, ok := missing["error"].(map[string]any); !ok || !strings.Contains(errObj["message"].(string), "params._meta") {
		t.Fatalf("missing metadata answer = %v", missing)
	}
	unknown := c.call(core.MethodToolsList, map[string]any{
		"_meta": map[string]any{
			mcpcompat.ProtocolVersionKey:    "2099-01-01",
			mcpcompat.ClientInfoKey:         map[string]any{"name": "codex", "version": "test"},
			mcpcompat.ClientCapabilitiesKey: map[string]any{},
		},
	})
	if errObj, ok := unknown["error"].(map[string]any); !ok || errObj["code"] != float64(-32022) || !strings.Contains(errObj["message"].(string), "unsupported MCP") {
		t.Fatalf("unknown metadata answer = %v", unknown)
	}
	if data, ok := unknown["error"].(map[string]any)["data"].(map[string]any); !ok || data["requested"] != "2099-01-01" || data["supported"] == nil {
		t.Fatalf("unknown metadata data = %v", unknown["error"])
	}
	malformedDiscover := c.call(core.MethodDiscover, map[string]any{
		"_meta": map[string]any{
			mcpcompat.ProtocolVersionKey:    mcpcompat.Modern.String(),
			mcpcompat.ClientCapabilitiesKey: "not-an-object",
		},
	})
	if errObj, ok := malformedDiscover["error"].(map[string]any); !ok || errObj["code"] != float64(codeInvalidParams) {
		t.Fatalf("malformed server/discover metadata = %v, want -32602", malformedDiscover)
	}
	malformedEnvelope := c.call(core.MethodDiscover, map[string]any{"_meta": "not-an-object"})
	if errObj, ok := malformedEnvelope["error"].(map[string]any); !ok || errObj["code"] != float64(codeInvalidParams) {
		t.Fatalf("malformed server/discover envelope = %v, want -32602", malformedEnvelope)
	}
	listed := result(t, c.call(core.MethodToolsList, modernParams("codex", map[string]any{})), "modern tools/list")
	meta, ok := listed["_meta"].(map[string]any)
	if !ok || meta[mcpcompat.ServerInfoKey] == nil {
		t.Fatalf("modern list missing serverInfo metadata: %v", listed)
	}
	if listed["resultType"] != "complete" || listed["ttlMs"] != float64(0) || listed["cacheScope"] != "private" {
		t.Fatalf("modern list completion/cache metadata = %v", listed)
	}
	command := result(t, c.call(core.MethodCommand, mergeModern(map[string]any{"name": "status"}, "codex")), "modern atenea/command")
	if command["structuredContent"] == nil {
		t.Fatalf("modern command has no structured content: %v", command)
	}
	if command["resultType"] != "complete" {
		t.Fatalf("modern command resultType = %v", command["resultType"])
	}
}

func TestModernIdentityDoesNotChangeApplicationPermissions(t *testing.T) {
	settings, plan := writingPlanFixture(t, 0)
	settings = strings.Replace(settings, "client_effects = [\"write\", \"external\"]", "client_effects = []", 1)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	result(t, c.call(core.MethodToolsList, modernParams("codex", map[string]any{})), "modern list")
	if sessions := atenea.Sessions(); len(sessions) != 0 {
		t.Fatalf("modern request leaked an application session: %v", sessions)
	}
	grantShaped := map[string]any{"experimental": map[string]any{"atenea": map[string]any{"grant": []string{"write"}}}}
	result(t, c.call(core.MethodToolsList, modernParams("attacker", grantShaped)), "observed identity refresh")
	if sessions := atenea.Sessions(); len(sessions) != 0 {
		t.Fatalf("second modern request leaked an application session: %v", sessions)
	}
	created := c.call(core.MethodToolsCall, mergeModern(map[string]any{
		"name": "workflow.create", "arguments": map[string]any{"file": plan},
	}, "attacker"))
	if got, _ := created["result"].(map[string]any); got["isError"] != true {
		t.Fatalf("grant-shaped metadata widened permission: %v", created)
	}
}

func TestModernSameConnectionUsesFreshClientContextPerRequest(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	result(t, c.call(core.MethodToolsList, modernParams("first-client", map[string]any{})), "first modern client")
	if got := atenea.Sessions(); len(got) != 0 {
		t.Fatalf("first modern context remained open: %v", got)
	}
	result(t, c.call(core.MethodToolsList, modernParams("second-client", map[string]any{})), "second modern client")
	if got := atenea.Sessions(); len(got) != 0 {
		t.Fatalf("second modern context remained open: %v", got)
	}
}

func TestModernStatelessReconnectStartsASeparateTransportSession(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()

	first := dial(t)
	result(t, first.call(core.MethodToolsList, modernParams("codex", map[string]any{})), "first modern list")
	first.close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(atenea.Sessions()) != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(atenea.Sessions()) != 0 {
		t.Fatalf("first application session remained after transport close")
	}
	second := dial(t)
	result(t, second.call(core.MethodToolsList, modernParams("codex", map[string]any{})), "reconnected modern list")
	if len(atenea.Sessions()) != 0 {
		t.Fatalf("reconnected modern request leaked an application session: %d", len(atenea.Sessions()))
	}
}

func mergeModern(params map[string]any, client string) map[string]any {
	for key, value := range modernParams(client, map[string]any{}) {
		params[key] = value
	}
	return params
}
