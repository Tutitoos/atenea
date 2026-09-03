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
	// endpoint alongside the dashboard: writing anything under
	// [orchestrator.kivgraph] makes the table explicit, and an explicit table
	// has to say where the server is -- a dashboard is a viewer, not a far side.
	body := settings + "\n[orchestrator.kivgraph]\nendpoint = \"http://127.0.0.1:7788/mcp\"\ndashboard = \"" + server.URL + "\"\n"
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

func TestAteneaDashboardPublishIsDryRunUnlessApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	body := settings + `
[dashboard]
enabled = true
listen = "127.0.0.1:8799"
access = "tailscale"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := dashboardPublishRun
	var calls [][]string
	dashboardPublishRun = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte("applied\n"), nil
	}
	t.Cleanup(func() { dashboardPublishRun = old })

	out, err := cli(t, "--config", path, "dashboard", "publish", "tailscale")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(calls) != 0 || !strings.Contains(out, "dry-run") || !strings.Contains(out, "127.0.0.1:8799") {
		t.Fatalf("dry-run calls=%v out=%q", calls, out)
	}
	out, err = cli(t, "--config", path, "dashboard", "publish", "tailscale", "--apply")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(calls) != 1 || calls[0][0] != "tailscale" || !strings.Contains(out, "applied") {
		t.Fatalf("apply calls=%v out=%q", calls, out)
	}
}

func TestAteneaDashboardCheckRefusesDisabledPanel(t *testing.T) {
	path := dashboardSettings(t, "http://127.0.0.1:1")
	if _, err := cli(t, "--config", path, "dashboard", "atenea", "--check"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled panel error = %v", err)
	}
}
