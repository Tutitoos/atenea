package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The status screen is where the sharp edge stays visible. A settings file that
// never named a client floor has one anyway -- a copy of the operator's -- and
// the copy moves whenever the original does. Printing two identical lists with
// nothing between them would show a decision that was never made, so the screen
// has to say which of the two it is looking at.

// floorSettings writes the fixture with an orchestrator policy in front of it.
// The table goes first because everything after it in the fixture is an array
// of tables, and a bare key landing inside one of those belongs to it.
func floorSettings(t *testing.T, policy string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.toml")
	const anchor = "contract = \"3.0.0\"\n"
	body := strings.Replace(settings, anchor, anchor+"\n[orchestrator]\n"+policy+"\n", 1)
	if body == settings {
		t.Fatal("the fixture no longer declares its contract on one line")
	}
	body += "\n[metrics]\npath = \"" + filepath.Join(dir, "base.duckdb") + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestTheScreenSaysWhenClientsInheritTheOperatorsFloor(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err := cli(t, "--config", floorSettings(t, "effects = [\"process\", \"write\"]"), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	line := lineWith(t, out, "clients")
	// Both halves matter. The list, because a screen that only said
	// "inherited" would make the reader go and find the other line; and the
	// note, because without it the reader has no way to tell a copy from a
	// choice, which is the only thing this line was added to answer.
	for _, want := range []string{"process", "write", "inherited"} {
		if !strings.Contains(line, want) {
			t.Errorf("clients line = %q, want it to mention %q", line, want)
		}
	}
}

func TestTheScreenDoesNotCallAWrittenFloorInherited(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := floorSettings(t, "effects = [\"process\", \"write\"]\nclient_effects = [\"process\"]")
	out, err := cli(t, "--config", path, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	line := lineWith(t, out, "clients")
	if strings.Contains(line, "inherited") {
		t.Errorf("clients line = %q: a floor the operator wrote is not inherited", line)
	}
	if strings.Contains(line, "write") {
		t.Errorf("clients line = %q: the operator's write is not the clients'", line)
	}
	if !strings.Contains(line, "process") {
		t.Errorf("clients line = %q, want it to name the floor that was written", line)
	}
	// The operator's own line is unchanged beside it, which is what makes the
	// two readable as two: a screen showing only the narrower one would look
	// like the console had been narrowed too.
	if standing := lineWith(t, out, "standing"); !strings.Contains(standing, "write") {
		t.Errorf("standing line = %q, want it to still name write", standing)
	}
}

// lineWith returns the one line of the screen that starts with a label, and
// fails loudly when there is none: a test that silently matched an empty
// string would pass for a screen that stopped printing the line at all.
func lineWith(t *testing.T, out, label string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label+" ") {
			return line
		}
	}
	t.Fatalf("no %q line on the screen:\n%s", label, out)
	return ""
}
