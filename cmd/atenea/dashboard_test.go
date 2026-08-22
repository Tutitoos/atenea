package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dashboardSettings(t *testing.T, url string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	body := settings + "\n[[mcp_server]]\nid = \"headroom\"\ncommand = [\"sh\", \"-c\", \"true\"]\n" +
		"dashboard = \"" + url + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

func TestDashboardCheckDoesNotOpenBrowser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	out, err := cli(t, "--config", dashboardSettings(t, server.URL), "dashboard", "headroom", "--check")
	if err != nil {
		t.Fatalf("dashboard --check: %v", err)
	}
	if !strings.Contains(out, "dashboard headroom accessible") {
		t.Fatalf("out = %q", out)
	}
}

func TestKivgraphDashboardCheckUsesOrchestratorDeclaration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "atenea.toml")
	body := settings + "\n[orchestrator.kivgraph]\ndashboard = \"" + server.URL + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	out, err := cli(t, "--config", path, "dashboard", "kivgraph", "--check")
	if err != nil {
		t.Fatalf("kivgraph dashboard --check: %v", err)
	}
	if !strings.Contains(out, "dashboard kivgraph accessible") {
		t.Fatalf("out = %q", out)
	}
}

func TestDashboardOpenUsesLauncherSeam(t *testing.T) {
	old := dashboardOpen
	var opened string
	dashboardOpen = func(rawURL string) error {
		opened = rawURL
		return nil
	}
	t.Cleanup(func() { dashboardOpen = old })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	out, err := cli(t, "--config", dashboardSettings(t, server.URL), "dashboard", "headroom")
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if opened != server.URL || !strings.Contains(out, "opened dashboard headroom") {
		t.Fatalf("opened=%q out=%q", opened, out)
	}
}

func TestDashboardHostsDryRunPreservesForeignLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o600); err != nil {
		t.Fatalf("write hosts: %v", err)
	}
	old := dashboardHostsPath
	dashboardHostsPath = path
	t.Cleanup(func() { dashboardHostsPath = old })

	out, err := cli(t, "--config", dashboardSettings(t, "http://127.0.0.1:8787"), "dashboard", "hosts", "--dry-run")
	if err != nil {
		t.Fatalf("dashboard hosts --dry-run: %v", err)
	}
	if !strings.Contains(out, "127.0.0.1 localhost") || !strings.Contains(out, "127.0.0.1 headroom") {
		t.Fatalf("out = %q", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hosts: %v", err)
	}
	if string(got) != "127.0.0.1 localhost\n" {
		t.Fatalf("dry-run modified hosts: %q", got)
	}
}
