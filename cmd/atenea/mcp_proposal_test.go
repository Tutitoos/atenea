package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPProposalNeedsEvidenceAndExplicitActivation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["method"] != "server/discover" {
			t.Fatalf("method = %v", request["method"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}, "ttlMs": 0, "cacheScope": "private", "_meta": map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": "fixture", "version": "1"}}}})
	}))
	defer server.Close()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settings := settingsFile(t)
	var out bytes.Buffer
	if err := cmdMCPProposal(settings, []string{"propose", "--id", "fixture-new", "--url", server.URL, "--protocol", "modern-pin", "--expose", "raw", "--tool", "search", "--effect", "read"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "functional validation: not tested") || !strings.Contains(out.String(), "Descriptions were not used") {
		t.Fatalf("proposal output = %s", out.String())
	}
	if err := cmdMCPProposal(settings, []string{"activate", "fixture-new"}, &out); err == nil {
		t.Fatal("activation without explicit authorization succeeded")
	}
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdMCPProposal(settings, []string{"activate", "fixture-new", "--authorize"}, &out); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) || !bytes.Contains(after, []byte(`id = "fixture-new"`)) || !bytes.Contains(after, []byte(`tools = ["search"]`)) {
		t.Fatalf("settings were not append-only:\n%s", after)
	}
	if err := cmdMCPProposal(settings, []string{"activate", "fixture-new", "--authorize"}, &out); err == nil {
		t.Fatal("existing integration was replaced")
	}
}

func TestFailedProposalDoesNotCreateActivationState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdMCPProposal("", []string{"propose", "--id", "dead", "--url", "http://127.0.0.1:1/mcp", "--protocol", "modern-pin"}, &out)
	if err == nil {
		t.Fatal("unreachable endpoint produced a proposal")
	}
	if _, statErr := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "atenea", "mcp-proposals.json")); !os.IsNotExist(statErr) {
		t.Fatalf("proposal state exists after failed discovery: %v", statErr)
	}
}
