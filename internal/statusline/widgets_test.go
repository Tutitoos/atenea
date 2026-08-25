package statusline_test

// The widgets are 1,173 lines of TSX that this binary carries and writes onto
// somebody's machine, and nothing in this repository compiles them: there is no
// package.json, no tsconfig.json and no test runner for them. The client is the
// first thing that ever reads them, and what it does with a file it cannot parse
// is draw nothing and say nothing.
//
// A Go test cannot type-check TSX. What it can do is refuse to ship a widget
// that is empty or has lost the parts the client reaches for by name -- the
// plugin export, the slot registration, the JSX pragma the client's runtime
// needs -- which is what a truncated embed, a botched merge or a deleted
// default export look like from here. Anything finer belongs in a CI step that
// runs a real TypeScript parser over these three files.

import (
	"os"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/statusline"
)

// Every widget, as it lands on the machine, has to carry the shape the client
// loads it by. These four markers are what the client itself looks for.
func TestEveryShippedWidgetCarriesThePartsTheClientLoadsItBy(t *testing.T) {
	for _, line := range installedWidgets(t) {
		t.Run(line.Widget.Name, func(t *testing.T) {
			body, err := os.ReadFile(line.Plugin)
			if err != nil {
				t.Fatalf("reading the installed widget: %v", err)
			}
			source := string(body)
			if len(strings.TrimSpace(source)) == 0 {
				t.Fatal("the widget this binary carries is empty")
			}
			for _, marker := range []string{
				// The client mounts JSX with its own runtime; without the
				// pragma the file compiles against React and renders nothing.
				"/** @jsxImportSource @opentui/solid */",
				// A plugin is a default-exported object. No export, no plugin.
				"export default {",
				// And a plugin that registers no slot occupies none of the
				// screen, which is indistinguishable from not being installed.
				"api.slots.register(",
			} {
				if !strings.Contains(source, marker) {
					t.Errorf("the widget carries no %q, so the client would load it and draw nothing", marker)
				}
			}
		})
	}
}

// The atenea widget is the one that reads Atenea, over a socket path it builds
// itself. Both halves of that reading are literals, and either one silently
// rewritten leaves a line that draws "sin lectura" against a healthy service.
func TestTheAteneaWidgetStillReadsTheStatusSocketThisBinaryServes(t *testing.T) {
	for _, line := range installedWidgets(t) {
		if line.Widget.Name != "atenea" {
			continue
		}
		body, err := os.ReadFile(line.Plugin)
		if err != nil {
			t.Fatalf("reading the installed widget: %v", err)
		}
		for _, marker := range []string{"atenea/run/core.sock", `"method":"atenea/status"`, "sidebar_footer"} {
			if !strings.Contains(string(body), marker) {
				t.Errorf("the atenea widget no longer carries %q", marker)
			}
		}
		return
	}
	t.Fatal("this binary carries no widget named atenea")
}

// installedWidgets installs every widget into a config root of its own and
// returns where each one landed. Read from disk after an install rather than
// from the embed directly, because what a client parses is the file this
// package wrote, and a widget that never reaches disk is the same failure.
func installedWidgets(t *testing.T) []statusline.Line {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lines := statusline.All()
	for _, line := range lines {
		if _, err := line.Install(); err != nil {
			t.Fatalf("installing %s: %v", line.Widget.Name, err)
		}
	}
	return lines
}
