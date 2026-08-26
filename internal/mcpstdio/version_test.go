package mcpstdio_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
)

// A far side that introduces itself has to be believed over anything written
// down about it, and until now nothing over stdio asked.
//
// internal/mcphttp already captured serverInfo.version on the handshake; the
// stdio transport dropped the initialize result on the floor, so every
// provider reached this way filed its measurements under no version at all.
// The cost of that is not abstract: a comment in internal/adapter/scrapling
// named a version read out of documentation rather than measured, and it was
// wrong by two minor releases. A handshake that is already paid for is the
// cheapest possible place to stop guessing.
func serverSpeaking(t *testing.T, serverInfo any) *mcpstdio.Session {
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
			id, hasID := msg["id"]
			if !hasID {
				continue
			}
			result := map[string]any{"protocolVersion": "2025-06-18"}
			if serverInfo != nil {
				result["serverInfo"] = serverInfo
			}
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			_, _ = fromServer.Write(append(out, '\n'))
		}
	}()

	session := mcpstdio.New(fromClient, toClient, mcpstdio.Options{})
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestTheHandshakeRemembersWhatTheServerCalledItself(t *testing.T) {
	session := serverSpeaking(t, map[string]any{"name": "Scrapling", "version": "0.4.15"})

	// Nothing has been asked yet, so there is nothing to report -- and that is
	// a different state from a server that stayed quiet.
	if got := session.Version(); got != "" {
		t.Errorf("Version() before the handshake = %q, want empty", got)
	}
	if err := session.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := session.Version(); got != "0.4.15" {
		t.Errorf("Version() = %q, want the version the server gave", got)
	}
}

// A server that says nothing about itself leaves this empty, which is a fact
// rather than a failure: contract.Outcome.ToolVersion is documented as empty
// exactly when the far side would not say, and a guessed version is worse
// than none.
func TestAServerThatWillNotSayLeavesTheVersionEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		info any
	}{
		{"no serverInfo at all", nil},
		{"serverInfo with no version", map[string]any{"name": "quiet"}},
		{"an empty version", map[string]any{"name": "quiet", "version": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := serverSpeaking(t, tc.info)
			if err := session.Initialize(context.Background()); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			if got := session.Version(); got != "" {
				t.Errorf("Version() = %q, want empty", got)
			}
		})
	}
}

// A handshake whose shape this cannot read is not a handshake that failed.
// The session still works; it simply has nothing to say about the version.
func TestAnUnreadableIntroductionDoesNotFailTheHandshake(t *testing.T) {
	session := serverSpeaking(t, "not an object at all")
	if err := session.Initialize(context.Background()); err != nil {
		t.Fatalf("a malformed serverInfo broke the handshake: %v", err)
	}
	if got := session.Version(); got != "" {
		t.Errorf("Version() = %q, want empty", got)
	}
}
