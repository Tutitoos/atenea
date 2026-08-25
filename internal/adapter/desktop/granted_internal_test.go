package desktop

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// In the package rather than beside the others because osNeeds is the subject:
// no capability shipped today declares a permission, so the refusal below
// cannot be reached from outside. It will be reachable the moment a capability
// that reads the tree or the screen lands, and the branch it guards is the one
// where a wrong answer sends somebody to the wrong Settings pane.
func healthSession(t *testing.T, health map[string]any) func(context.Context) (*mcpstdio.Session, error) {
	t.Helper()
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	go func() {
		defer func() { _ = fromServer.Close() }()
		decoder := json.NewDecoder(toServer)
		for {
			var msg map[string]any
			if err := decoder.Decode(&msg); err != nil {
				return
			}
			id, ok := msg["id"]
			if !ok {
				continue
			}
			result := map[string]any{"protocolVersion": "2025-06-18"}
			if msg["method"] == "tools/call" {
				body, _ := json.Marshal(health)
				result = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": string(body)}},
				}
			}
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			_, _ = fromServer.Write(append(out, '\n'))
		}
	}()
	session := mcpstdio.New(fromClient, toClient, mcpstdio.Options{})
	t.Cleanup(func() { _ = session.Close() })
	return func(context.Context) (*mcpstdio.Session, error) { return session, nil }
}

func withNeeds(t *testing.T, capability string, accessibility, screen bool) {
	t.Helper()
	previous, had := osNeeds[capability]
	osNeeds[capability] = struct{ accessibility, screenRecording bool }{accessibility, screen}
	t.Cleanup(func() {
		if had {
			osNeeds[capability] = previous
			return
		}
		delete(osNeeds, capability)
	})
}

func TestAMissingPermissionNamesWhichOneAndWhereItLives(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		needsAX, needsScreen bool
		hasAX, hasScreen     bool
		wantRefused          bool
		wantMentions         string
	}{
		{name: "accessibility missing", needsAX: true, hasScreen: true,
			wantRefused: true, wantMentions: "Accessibility"},
		{name: "screen recording missing", needsScreen: true, hasAX: true,
			wantRefused: true, wantMentions: "Screen Recording"},
		{name: "both missing", needsAX: true, needsScreen: true,
			wantRefused: true, wantMentions: "Accessibility and Screen Recording"},
		// The case the wording has to get right: the machine is missing both,
		// but this capability only asked for one. Naming the other would send
		// somebody to grant a permission nothing here needs, and a permission
		// granted for no reason is the one nobody remembers to take back.
		{name: "only what it needs is named", needsAX: true,
			wantRefused: true, wantMentions: "needs Accessibility, which"},
		{name: "both held", needsAX: true, needsScreen: true, hasAX: true, hasScreen: true},
		// The one that would be easy to get backwards: needing only the
		// screen while Accessibility is absent is not a refusal.
		{name: "needs only what it has", needsScreen: true, hasScreen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withNeeds(t, "desktop.probe", tc.needsAX, tc.needsScreen)
			missing := ""
			switch {
			case !tc.hasAX && !tc.hasScreen:
				missing = "neither Accessibility nor Screen Recording is granted"
			case !tc.hasAX:
				missing = "Accessibility is not granted: System Settings > Privacy & Security > Accessibility"
			case !tc.hasScreen:
				missing = "Screen Recording is not granted: System Settings > Privacy & Security > Screen Recording"
			}
			runner, err := New(Options{
				Session: healthSession(t, map[string]any{
					"accessibility": tc.hasAX, "screen_recording": tc.hasScreen, "missing": missing,
				}),
				Responsible: func() bool { return true },
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = runner.granted(t.Context(), "desktop.probe")
			if !tc.wantRefused {
				if err != nil {
					t.Fatalf("granted = %v, want it to pass", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a capability ran without the permission it declared it needs")
			}
			if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
				t.Errorf("failure = %v, want permission_denied", got)
			}
			if !strings.Contains(err.Error(), tc.wantMentions) {
				t.Errorf("refusal = %q, want it to name %q", err, tc.wantMentions)
			}
		})
	}
}
