package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func TestDashboardCatalogRejectsMissingAndDuplicateDeclarations(t *testing.T) {
	if _, err := ResolveConfig(config.Config{}, "kivgraph"); err == nil || !strings.Contains(err.Error(), "no dashboard") {
		t.Fatalf("missing Kivgraph dashboard error = %v", err)
	}
	servers := []config.MCPServer{
		{ID: "same", Dashboard: "http://127.0.0.1:1"},
		{ID: "same", Dashboard: "http://127.0.0.1:2"},
	}
	if _, err := All(servers); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("duplicate dashboard error = %v", err)
	}
	cfg := config.Config{MCPServers: []config.MCPServer{{ID: KivgraphID, Dashboard: "http://127.0.0.1:1"}}}
	cfg.Orchestrator.Kivgraph.Dashboard = "http://127.0.0.1:2"
	if _, err := AllConfig(cfg); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("Kivgraph alias conflict = %v", err)
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

func TestCheckRejectsServerErrorsAndMalformedURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	if err := Check(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("server error = %v", err)
	}
	if err := Check(context.Background(), "://bad"); err == nil {
		t.Fatal("malformed URL unexpectedly passed")
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

func TestDiscoverSerenaSkipsOtherProjects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "atenea")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active_project": map[string]string{"name": "other", "path": filepath.Join(t.TempDir(), "other")},
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
	if _, err := discoverSerena(context.Background(), root, port, 1); err == nil || !strings.Contains(err.Error(), "no active Serena") {
		t.Fatalf("missing project error = %v", err)
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

func TestPlanHostsRejectsMalformedBlocksAndDuplicateAliases(t *testing.T) {
	if _, err := PlanHosts(managedBegin+"\n127.0.0.1 stale\n", nil, false); !errors.Is(err, ErrInvalidHosts) {
		t.Fatalf("unterminated block error = %v", err)
	}
	_, err := PlanHosts("", []Entry{{Alias: "same"}, {Alias: "same"}}, false)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("duplicate alias error = %v", err)
	}
}

func TestPlanHostsKeepsUnremovedManagedEntriesAndHostsRoundTrip(t *testing.T) {
	existing := managedBegin + "\n127.0.0.1 old\n" + managedEnd + "\n"
	plan, err := PlanHosts(existing, []Entry{{Alias: "new"}}, false)
	if err != nil {
		t.Fatalf("PlanHosts: %v", err)
	}
	if !strings.Contains(plan.Content, "127.0.0.1 old") || !strings.Contains(plan.Content, "127.0.0.1 new") {
		t.Fatalf("managed entries were not preserved:\n%s", plan.Content)
	}
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteHosts(path, plan.Content); err != nil {
		t.Fatalf("WriteHosts: %v", err)
	}
	got, err := ReadHosts(path)
	if err != nil {
		t.Fatalf("ReadHosts: %v", err)
	}
	if got != plan.Content {
		t.Fatalf("hosts round trip = %q, want %q", got, plan.Content)
	}
}
