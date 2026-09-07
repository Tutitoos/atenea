package mcpcompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDiscoveryRequiresOfficialCacheableShape(t *testing.T) {
	valid := json.RawMessage(`{"resultType":"complete","supportedVersions":["2040-01-01","2026-07-28"],"capabilities":{},"ttlMs":0,"cacheScope":"private"}`)
	if got, err := ParseDiscovery(valid); err != nil || got.SupportedVersions[0] != Modern || got.CacheScope != "private" {
		t.Fatalf("valid discovery = %#v, %v", got, err)
	}
	for _, raw := range []string{
		`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{},"ttlMs":0,"cacheScope":"shared"}`,
		`{"resultType":"complete","supportedVersions":["2025-06-18"],"capabilities":{},"ttlMs":0,"cacheScope":"private"}`,
		`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{},"ttlMs":0,"cacheScope":"private","preferred":"2026-07-28"}`,
		`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":null,"ttlMs":0,"cacheScope":"private"}`,
		`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":[],"ttlMs":0,"cacheScope":"private"}`,
		`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":null},"ttlMs":0,"cacheScope":"private"}`,
		`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":null}},"ttlMs":0,"cacheScope":"private"}`,
	} {
		if _, err := ParseDiscovery(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid discovery %s", raw)
		}
	}
	tooLarge := `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{},"ttlMs":9223372036855,"cacheScope":"private"}`
	if _, err := ParseDiscovery(json.RawMessage(tooLarge)); err == nil {
		t.Fatal("accepted cache ttl that overflows time.Duration")
	}
}

func TestParseDiscoveryTypesCapabilitiesAndTracksTools(t *testing.T) {
	base := `{"resultType":"complete","supportedVersions":["2026-07-28"],"ttlMs":0,"cacheScope":"private"}`
	for _, test := range []struct {
		name       string
		capability string
		wantTools  bool
		wantErr    bool
	}{
		{name: "absent", capability: `{}`, wantTools: false},
		{name: "present", capability: `{"tools":{}}`, wantTools: true},
		{name: "false is malformed", capability: `{"tools":false}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(base, `"cacheScope":"private"`, `"capabilities":`+test.capability+`,"cacheScope":"private"`, 1))
			got, err := ParseDiscovery(raw)
			if test.wantErr {
				if err == nil {
					t.Fatal("accepted non-object tools capability")
				}
				return
			}
			if err != nil || got.SupportsTools() != test.wantTools {
				t.Fatalf("discovery = %#v, err=%v", got, err)
			}
		})
	}
}
