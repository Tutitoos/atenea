package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCommandMarkdownUsesChatFriendlySections(t *testing.T) {
	response := CommandResponse{
		Command: "metrics",
		Status:  "ok",
		Summary: "1 measurement row(s)",
		Data: []metrics.Row{{
			Capability: "code.search", Implementation: "ripgrep", Provider: "ripgrep",
			Repository: "api", Successes: 3, Failures: 1,
		}},
	}
	got := commandMarkdown(response)
	for _, want := range []string{"## Atenea: metrics", "**Estado:** `ok`", "| Capacidad |", "| code.search |", "| 3 | 1 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown missing %q:\n%s", want, got)
		}
	}
}

func TestCommandMarkdownEscapesTableContent(t *testing.T) {
	got := commandMarkdown(CommandResponse{
		Command: "metrics", Status: "ok", Summary: "summary",
		Data: []metrics.Row{{Capability: "a|b", Implementation: "impl", Provider: "p", Repository: "r"}},
	})
	if strings.Contains(got, "| a|b |") || !strings.Contains(got, `a\|b`) {
		t.Fatalf("table content was not escaped: %s", got)
	}
}

func TestCommandTextPreservesContentWhileRemovingFormatting(t *testing.T) {
	got := commandText("## Atenea: status\n\n**Estado:** `ok`\n")
	if got != "Atenea: status\n\nEstado: ok\n" {
		t.Fatalf("plain command output = %q", got)
	}
}

func TestCommandHelpSupportsAllOutputFormats(t *testing.T) {
	var service *Core
	for _, format := range []string{"", "markdown", "text", "json"} {
		response, err := service.Command(context.Background(), CommandRequest{Name: " help ", Format: format})
		if err != nil {
			t.Fatalf("format %q: %v", format, err)
		}
		if response.Command != "help" || response.Status != "ok" || response.Markdown == "" {
			t.Fatalf("format %q returned %#v", format, response)
		}
		if !json.Valid([]byte(CommandJSON(response))) {
			t.Fatalf("format %q returned invalid JSON", format)
		}
	}
}

func TestCommandRejectsUnknownFormatAndNegativeLimit(t *testing.T) {
	var service *Core
	for _, request := range []CommandRequest{
		{Name: "unknown"},
		{Name: "help", Format: "yaml"},
		{Name: "help", Limit: -1},
	} {
		if _, err := service.Command(context.Background(), request); err == nil {
			t.Fatalf("request %#v was accepted", request)
		}
	}
	if !throughZero().IsZero() {
		t.Fatal("throughZero must return the zero time")
	}
}

func TestCommandReadsCoreBackedViews(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	service, err := New(config.Config{}, Command)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	for _, name := range []string{"status", "metrics", "traces", "catalog", "doctor", "incidents", "floor", "config"} {
		response, err := service.Command(context.Background(), CommandRequest{
			Name: name, Client: "claude", Profile: "claude",
		})
		if err != nil {
			t.Fatalf("command %s: %v", name, err)
		}
		if response.Command != name || response.Status != "ok" || response.Markdown == "" {
			t.Fatalf("command %s returned %#v", name, response)
		}
	}
}

func TestCommandMarkdownCoversReadOnlyDataShapes(t *testing.T) {
	cases := []struct {
		name string
		data any
		want string
	}{
		{name: "strings", data: []string{"help"}, want: "/atenea help"},
		{name: "status", data: Status{}, want: "**Luz:**"},
		{name: "empty metrics", data: []metrics.Row{}, want: "No hay mediciones"},
		{name: "traces", data: []trace.Row{{TypeName: "worker", Objective: "goal"}}, want: "| Started |"},
		{name: "floor", data: []floor.Measurement{{}}, want: "| Repository |"},
		{name: "empty incidents", data: notebook.Read{}, want: "No hay incidencias"},
		{name: "commands map", data: map[string]any{"commands": []string{"status"}}, want: "/atenea status"},
		{name: "capabilities map", data: map[string]any{"capabilities": []contract.Capability{}}, want: "## Atenea"},
		{name: "generic map", data: map[string]any{"value": "safe"}, want: "```json"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := commandMarkdown(CommandResponse{Command: test.name, Status: "ok", Summary: "summary", Data: test.data})
			if !strings.Contains(got, test.want) {
				t.Fatalf("Markdown missing %q:\n%s", test.want, got)
			}
		})
	}
}

func TestPromptsListAndGetUseStrictTypedArguments(t *testing.T) {
	conv := &conversation{session: &Session{}}
	listed, listErr := conv.promptsList()
	if listErr != nil {
		t.Fatalf("prompts/list: %v", listErr)
	}
	prompts, ok := listed.(map[string]any)["prompts"].([]map[string]any)
	if !ok || len(prompts) != len(commandPrompts()) {
		t.Fatalf("unexpected prompts list: %#v", listed)
	}

	got, getErr := conv.promptsGet(json.RawMessage(`{"name":"traces","arguments":{"id":"run-1","type":"worker","verdict":"ok","open":"true","since":"24h","limit":"4"}}`))
	if getErr != nil {
		t.Fatalf("prompts/get: %v", getErr)
	}
	payload := got.(map[string]any)
	messages := payload["messages"].([]any)
	content := messages[0].(map[string]any)["content"].(map[string]any)
	if !strings.Contains(content["text"].(string), `"name":"traces"`) {
		t.Fatalf("prompt did not encode the command request: %#v", got)
	}

	for _, raw := range []string{
		`{"name":"missing"}`,
		`{"name":"status","arguments":{"unknown":"x"}}`,
		`{"name":"incidents","arguments":{"all":"sometimes"}}`,
		`{"name":"traces","arguments":{"limit":"many"}}`,
		`{"name":"status"} {}`,
	} {
		if _, err := conv.promptsGet(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid prompt request was accepted: %s", raw)
		}
	}

	var uninitialized conversation
	if _, err := uninitialized.promptsList(); err == nil {
		t.Fatal("prompts/list was accepted before initialize")
	}
	if _, err := uninitialized.promptsGet(json.RawMessage(`{"name":"status"}`)); err == nil {
		t.Fatal("prompts/get was accepted before initialize")
	}
}
