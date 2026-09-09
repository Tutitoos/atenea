package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestDashboardDefaultsAreDisabledAndLoopback(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.Enabled {
		t.Fatal("dashboard is enabled in the shipped defaults")
	}
	if cfg.Dashboard.Listen != "127.0.0.1:8788" || cfg.Dashboard.Access != "tailscale" {
		t.Fatalf("dashboard defaults = %+v", cfg.Dashboard)
	}
}

func TestDashboardAcceptsWildcardOnlyForTailscale(t *testing.T) {
	shipped, err := os.ReadFile("default.toml")
	if err != nil {
		t.Fatal(err)
	}
	base := string(shipped)
	for name, tc := range map[string]struct {
		raw  string
		want bool
	}{
		"tailscale wildcard": {raw: strings.Replace(base, "listen = \"127.0.0.1:8788\"", "listen = \"0.0.0.0:4444\"", 1), want: true},
		"loopback wildcard":  {raw: strings.Replace(strings.Replace(base, "listen = \"127.0.0.1:8788\"", "listen = \"0.0.0.0:4444\"", 1), "access = \"tailscale\"", "access = \"loopback\"", 1)},
		"LAN missing TLS":    {raw: strings.Replace(base, "session_ttl = \"12h\"", "session_ttl = \"12h\"\nlan_listen = \"192.168.10.8:8789\"", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "atenea.toml")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path)
			if (err == nil) != tc.want {
				t.Fatalf("Load error = %v, want success %t", err, tc.want)
			}
		})
	}
}

func TestDashboardLANPathsMustBeAbsolute(t *testing.T) {
	shipped, err := os.ReadFile("default.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Replace(string(shipped), "session_ttl = \"12h\"", "session_ttl = \"12h\"\nlan_listen = \"192.168.10.8:8789\"\nlan_cert_file = \"cert.pem\"\nlan_key_file = \"/tmp/key.pem\"\nlan_token_file = \"/tmp/token\"", 1)
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Load error = %v, want absolute-path validation", err)
	}
}
