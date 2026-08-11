package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
)

// The screen half of the silence. The core remembers three states; this is the
// only place a human reads them, so this is where "grey must not read as green"
// is either true or the bug is back.
func TestPrintServersNeverLetsUnknownReadAsHealthy(t *testing.T) {
	var out bytes.Buffer
	printServers(&out, core.Status{Servers: []core.ServerStatus{
		{ID: "headroom", Transport: "stdio", Expose: "off", Where: "headroom mcp serve", State: core.BackendUnknown},
		{ID: "semgrep", Transport: "stdio", Expose: "raw", State: core.BackendOK, LastChecked: time.Now().Add(-8 * time.Second)},
		{
			ID: "context7", Transport: "stdio", Expose: "raw", State: core.BackendFailed,
			Reason: "env: 'node': No such file or directory", LastChecked: time.Now().Add(-time.Minute),
		},
	}})
	text := out.String()

	lines := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		for _, id := range []string{"headroom", "semgrep", "context7"} {
			if strings.Contains(line, id) {
				lines[id] = line
			}
		}
	}

	// An untouched server has no reason and no timestamp in the record, so the
	// row has to supply the sentence itself. Without it the line is a server id
	// next to a word and two dashes, which is what an operator reads as fine.
	unknown := lines["headroom"]
	if !strings.Contains(unknown, "unknown") || !strings.Contains(unknown, "nobody has asked") {
		t.Errorf("unknown row = %q, want the state and the sentence", unknown)
	}
	if strings.Contains(unknown, " ok ") {
		t.Errorf("unknown row = %q, must not carry ok", unknown)
	}

	// A failure has to be loud and carry its cause: FAILED upper-cased, and the
	// server's own words for why. "failed" alone is what cost the hours.
	failed := lines["context7"]
	if !strings.Contains(failed, "FAILED") {
		t.Errorf("failed row = %q, want the state upper-cased", failed)
	}
	if !strings.Contains(failed, "No such file or directory") {
		t.Errorf("failed row = %q, want the cause in the server's own words", failed)
	}

	// And the one that answered is the only one allowed to look calm -- with an
	// age, so a stale reading cannot pass for a fresh one.
	ok := lines["semgrep"]
	if !strings.Contains(ok, "ok") || !strings.Contains(ok, "8s ago") {
		t.Errorf("ok row = %q, want ok with the age of the reading", ok)
	}
}

// An install that declares no servers gets no section at all. The header alone
// over an empty list is noise on every ordinary status call, and this section
// is new enough that a machine which never declared one should not learn it
// exists.
func TestPrintServersStaysSilentWhenNothingIsDeclared(t *testing.T) {
	var out bytes.Buffer
	printServers(&out, core.Status{})
	if out.Len() != 0 {
		t.Errorf("out = %q, want nothing printed", out.String())
	}
}
