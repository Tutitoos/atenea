package passthrough_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
	"github.com/Tutitoos/atenea/internal/passthrough"
)

func modernStdioCommand(fallback bool) []string {
	discover := `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28","2025-06-18"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"modern","version":"2"}}}}`
	if fallback {
		discover = `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"legacy"}}`
	}
	return []string{"sh", "-c", fmt.Sprintf(`while read -r line; do id=$(printf '%%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p'); case "$line" in *server/discover*) printf '%%s\n' '%s' | sed "s/\"id\":1/\"id\":$id/" ;; *initialize*) printf '%%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"legacy","version":"1"}}}' | sed "s/\"id\":1/\"id\":$id/" ;; *tools/list*) printf '%%s\n' '{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","ttlMs":0,"cacheScope":"private","tools":[{"name":"allowed","inputSchema":{"type":"object"}}]}}' | sed "s/\"id\":1/\"id\":$id/" ;; *tools/call*) printf '%%s\n' '{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","content":[{"type":"text","text":"ok"}],"isError":false}}' | sed "s/\"id\":1/\"id\":$id/" ;; esac; done`, discover)}
}

func TestModernStdioPassthroughUsesDiscoverAndPreservesAllowList(t *testing.T) {
	b := passthrough.New(passthrough.Spec{
		ID: "modern", Command: modernStdioCommand(false), Allowed: []string{"allowed"},
		ProtocolMode: passthrough.ProtocolModernPin,
	})
	defer b.Close()
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if _, err := b.Call(t.Context(), "allowed", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if b.Allows("forbidden") {
		t.Fatal("modern stdio changed the allow-list")
	}
	if got := b.(interface{ ObservedProtocolVersion() string }).ObservedProtocolVersion(); got != mcpcompat.Modern.String() {
		t.Fatalf("observed protocol = %q", got)
	}
}

func TestAutoStdioUsesDisposableProbeThenDefinitiveLegacyChild(t *testing.T) {
	b := passthrough.New(passthrough.Spec{
		ID: "auto", Command: modernStdioCommand(true), Allowed: []string{"allowed"},
		ProtocolMode: passthrough.ProtocolAuto,
	})
	defer b.Close()
	tools, err := b.Tools(t.Context())
	if err != nil || len(tools) != 1 || tools[0].Name != "allowed" {
		t.Fatalf("Tools = %#v, %v", tools, err)
	}
	if got := b.(interface{ ObservedProtocolVersion() string }).ObservedProtocolVersion(); got != mcpcompat.Legacy.String() {
		t.Fatalf("observed protocol = %q", got)
	}
	if strings.TrimSpace(tools[0].Name) != "allowed" {
		t.Fatal("legacy definitive child did not serve catalog")
	}
}
