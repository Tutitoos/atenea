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

func TestDashboardRejectsNonLoopbackOrIncompleteLAN(t *testing.T) {
	shipped, err := os.ReadFile("default.toml")
	if err != nil {
		t.Fatal(err)
	}
	base := string(shipped)
	cases := map[string]string{
		"public listener": strings.Replace(base, "listen = \"127.0.0.1:8788\"", "listen = \"0.0.0.0:8788\"", 1),
		"LAN missing TLS": strings.Replace(base, "session_ttl = \"12h\"", "session_ttl = \"12h\"\nlan_listen = \"192.168.10.8:8789\"", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "atenea.toml")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path); err == nil {
				t.Fatal("invalid dashboard configuration loaded")
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
