package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestResolveByIDAndSkipServersWithoutDashboard(t *testing.T) {
	servers := []config.MCPServer{
		{ID: "headroom", Dashboard: "http://127.0.0.1:8787"},
		{ID: "plain"},
	}
	entry, err := Resolve(servers, "headroom")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if entry.Alias != "headroom" || entry.URL != "http://127.0.0.1:8787" {
		t.Fatalf("entry = %+v", entry)
	}
	if _, err := Resolve(servers, "plain"); err == nil || !strings.Contains(err.Error(), "no dashboard") {
		t.Fatalf("plain error = %v", err)
	}
	if _, err := Resolve(servers, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing error = %v", err)
	}
}

func TestResolveConfigIncludesKivgraphViewer(t *testing.T) {
	cfg := config.Config{
		MCPServers: []config.MCPServer{{ID: "headroom", Dashboard: "http://127.0.0.1:8787"}},
	}
	cfg.Orchestrator.Kivgraph.Dashboard = "http://127.0.0.1:7777"
	entry, err := ResolveConfig(cfg, "kivgraph")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if entry.ID != "kivgraph" || entry.Alias != "kivgraph" || entry.URL != "http://127.0.0.1:7777" {
		t.Fatalf("entry = %+v", entry)
	}
	entries, err := AllConfig(cfg)
	if err != nil {
		t.Fatalf("AllConfig: %v", err)
	}
	if len(entries) != 2 || entries[1].ID != "kivgraph" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestCheckAcceptsLiveNon5xxDashboard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	if err := Check(context.Background(), server.URL); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestDiscoverSerenaMatchesTheActiveProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "atenea")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_config_overview" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active_project": map[string]string{"name": "atenea", "path": root},
		})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := discoverSerena(context.Background(), root, port, 1)
	if err != nil {
		t.Fatalf("discoverSerena: %v", err)
	}
	if entry.URL != "http://127.0.0.1:"+strconv.Itoa(port)+"/dashboard/index.html" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestPlanHostsPreservesForeignEntriesAndIsIdempotent(t *testing.T) {
	existing := "127.0.0.1 localhost\n# a user entry\n127.0.0.1 old\n" +
		managedBegin + "\n127.0.0.1 stale\n" + managedEnd + "\n"
	entries := []Entry{{ID: "headroom", Alias: "headroom", URL: "http://127.0.0.1:8787"}}
	first, err := PlanHosts(existing, entries, true)
	if err != nil {
		t.Fatalf("PlanHosts: %v", err)
	}
	if !strings.Contains(first.Content, "127.0.0.1 old") || strings.Contains(first.Content, "stale") {
		t.Fatalf("foreign/obsolete entries wrong:\n%s", first.Content)
	}
	second, err := PlanHosts(first.Content, entries, true)
	if err != nil {
		t.Fatalf("second PlanHosts: %v", err)
	}
	if second.Changed {
		t.Fatalf("second plan changed an already managed file:\n%s", second.Content)
	}
}

func TestPlanHostsRejectsForeignAliasConflict(t *testing.T) {
	_, err := PlanHosts("127.0.0.1 headroom\n", []Entry{{Alias: "headroom"}}, false)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("err = %v, want alias conflict", err)
	}
}
