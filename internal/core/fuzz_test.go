package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

// The dispatch on the far side of the socket reads bytes Atenea did not
// write. Everything reaching it -- an editor's MCP client, a shell script
// piping JSON at the door, a half-written line from a crashed caller -- is
// third-party input, and until this existed the only inputs it had ever seen
// were the ones somebody typed into a test by hand. The repository had 1,696
// Test functions and no Fuzz at all.
//
// What is asserted is the only thing a decoder of untrusted bytes truly owes:
// it answers or refuses, and it does not take the process down. A panic here
// is not one failed call -- dispatch runs on the goroutine holding a chat's
// connection, so an input that panics is an input that kills the service for
// every chat attached to it.
func FuzzDispatchNeverPanicsOnArbitraryBytes(f *testing.F) {
	// The seeds are the shapes the hand-written tests already use, so the
	// fuzzer starts from valid protocol and mutates outwards rather than
	// spending its budget rediscovering that "{" is JSON.
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"atenea/status"}`,
		`{"jsonrpc":"2.0","id":2,"method":"atenea/detect","params":{"repository":"api"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"omp"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"code.search","arguments":{"query":"x"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"1.0","id":6,"method":"atenea/status"}`,
		`{"id":null,"method":""}`,
		`not json at all`,
		``,
	} {
		f.Add([]byte(seed))
	}

	// Built once. A core per input would make this a benchmark of config
	// loading rather than a fuzz of the decoder, and nothing dispatch does to
	// the core here outlives one message.
	atenea := fuzzCore(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		var req rpcRequest
		if json.Unmarshal(line, &req) != nil {
			// The socket answers a parse error and hangs up, which is a
			// decision this fuzz target has nothing to add to: the bytes
			// never reach dispatch.
			return
		}
		talk := &conversation{core: atenea}
		defer talk.close()
		_ = talk.dispatch(context.Background(), req)
	})
}

// fuzzCore builds one command-role core over a minimal settings file. Command
// rather than Service on purpose: this fuzzes the message decoding, and a
// service would want a socket, an upkeep claim and a clock for it.
func fuzzCore(f *testing.F) *Core {
	f.Helper()
	f.Setenv("XDG_STATE_HOME", f.TempDir())
	path := filepath.Join(f.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(fuzzSettings), 0o600); err != nil {
		f.Fatalf("write settings: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		f.Fatalf("config.Load: %v", err)
	}
	atenea, err := New(cfg, Command)
	if err != nil {
		f.Fatalf("core.New: %v", err)
	}
	return atenea
}

// fuzzSettings is socketSettings from the black-box tests, restated here
// because this file has to be in package core to reach dispatch at all and a
// const cannot cross that line. One catalog, one repository, one local
// implementation: enough for every method dispatch can route to.
const fuzzSettings = `
contract = "4.0.0"

[core]
shutdown_grace = "2s"

[orchestrator]
runners = ["local"]

  [orchestrator.local]
  implementations = ["ripgrep"]

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
effects = ["read"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

  [[capability.output]]
  name = "matches"
  type = "record_list"
  required = true

    [[capability.output.field]]
    name = "path"
    type = "string"
    required = true

    [[capability.output.field]]
    name = "line"
    type = "int"
    required = true

    [[capability.output.field]]
    name = "column"
    type = "int"
    required = true

    [[capability.output.field]]
    name = "snippet"
    type = "string"

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

  [implementation.health]
  state = "alive"

[[repository]]
id = "work"
path = "/tmp"
languages = ["go"]
scale = "small"
`
