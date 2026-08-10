package statusline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/statusline"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// at points the package at a config root of its own. Every test writes into a
// real directory rather than a fake filesystem, because the thing under test is
// what lands on a machine and a fake would not catch a wrong path.
func at(t *testing.T) statusline.Line {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return statusline.New()
}

func TestInstallWritesThePluginAndDeclaresIt(t *testing.T) {
	line := at(t)

	report, err := line.Install()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !report.Wrote || !report.Declared {
		t.Errorf("first install should write and declare, got wrote=%v declared=%v", report.Wrote, report.Declared)
	}

	body, err := os.ReadFile(line.Plugin)
	if err != nil {
		t.Fatalf("plugin: %v", err)
	}
	if !strings.Contains(string(body), "atenea/status") {
		t.Errorf("the installed plugin does not read the status socket:\n%s", firstLines(string(body)))
	}

	if got := declared(t, line); len(got) != 1 || got[0] != line.Declaration() {
		t.Errorf("declared = %q, want exactly [%q]", got, line.Declaration())
	}
}

func TestInstalledPluginIsTheOneTheBinaryCarries(t *testing.T) {
	line := at(t)
	if _, err := line.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	state := line.Status()
	if !state.Present || !state.Declared {
		t.Fatalf("after install: present=%v declared=%v", state.Present, state.Declared)
	}
	if !state.Current {
		t.Errorf("a fresh install should match the binary: shipped=%s on disk=%s", state.Shipped, state.Installed)
	}
}

// The reason the source is embedded at all: an edited or stale copy has to be
// reported, not assumed current because the file exists.
func TestStatusReportsADriftedCopy(t *testing.T) {
	line := at(t)
	if _, err := line.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.WriteFile(line.Plugin, []byte("// an older copy\n"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	state := line.Status()
	if !state.Present {
		t.Fatalf("the file is there")
	}
	if state.Current {
		t.Errorf("a different file must not report as current")
	}
	if state.Shipped == state.Installed {
		t.Errorf("digests should differ: %s vs %s", state.Shipped, state.Installed)
	}
}

func TestInstallTwiceChangesNothingTheSecondTime(t *testing.T) {
	line := at(t)
	if _, err := line.Install(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before, err := os.ReadFile(line.TUIConfig)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	report, err := line.Install()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if report.Declared {
		t.Errorf("the second install should not add a second declaration")
	}

	after, err := os.ReadFile(line.TUIConfig)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("config changed on a repeat install:\n%s\n---\n%s", before, after)
	}
	if got := declared(t, line); len(got) != 1 {
		t.Errorf("declared = %q, want one entry", got)
	}
}

// Somebody else's plugins and somebody else's keys are the whole risk in this
// package: a config it edits has to come back with everything it did not write.
func TestInstallKeepsWhatSomebodyElsePutInTheConfig(t *testing.T) {
	line := at(t)
	writeConfig(t, line, `{
  "theme": "smoke-theme",
  "leader_timeout": 2000,
  "plugin": ["./plugins/mine.tsx", "@someone/plugin@1.2.3"],
  "keybinds": { "leader": "ctrl+x" }
}`)

	if _, err := line.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	var keys map[string]json.RawMessage
	raw, err := os.ReadFile(line.TUIConfig)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("config is no longer valid JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"theme", "leader_timeout", "keybinds"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("%q was dropped:\n%s", key, raw)
		}
	}
	if got, want := string(keys["theme"]), `"smoke-theme"`; got != want {
		t.Errorf("theme = %s, want %s", got, want)
	}
	// Compared as a value, not as bytes: nested objects come back re-indented,
	// which is the same cosmetic rewrite as the key order and is what the package
	// documents. What must survive is the content.
	var keybinds map[string]string
	if err := json.Unmarshal(keys["keybinds"], &keybinds); err != nil {
		t.Fatalf("keybinds no longer parses: %v", err)
	}
	if keybinds["leader"] != "ctrl+x" {
		t.Errorf("keybinds = %v, want leader ctrl+x", keybinds)
	}

	want := []string{"./plugins/mine.tsx", "@someone/plugin@1.2.3", line.Declaration()}
	if got := declared(t, line); !equal(got, want) {
		t.Errorf("declared = %q, want %q", got, want)
	}
}

// A file this code cannot parse belongs to somebody else. Rewriting it from a
// partial parse would destroy comments to save one line of typing, so the
// command refuses and says which line to add.
func TestInstallRefusesAConfigItCannotParse(t *testing.T) {
	line := at(t)
	writeConfig(t, line, "{\n  // a comment this parser does not accept\n  \"plugin\": []\n}")

	_, err := line.Install()
	if err == nil {
		t.Fatalf("install should refuse a config it cannot parse")
	}
	if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
		t.Errorf("failure kind = %v, want %v", kind, contract.FailureInvalidInput)
	}
	if !strings.Contains(err.Error(), line.Declaration()) {
		t.Errorf("the refusal should name the line to add by hand: %v", err)
	}

	// And it must not have half-written the file on the way out.
	body, readErr := os.ReadFile(line.TUIConfig)
	if readErr != nil {
		t.Fatalf("config: %v", readErr)
	}
	if !strings.Contains(string(body), "// a comment") {
		t.Errorf("the refused config was modified:\n%s", body)
	}
}

func TestInstallRefusesAPluginListThatIsNotAList(t *testing.T) {
	line := at(t)
	writeConfig(t, line, `{"plugin": "./plugins/mine.tsx"}`)

	if _, err := line.Install(); err == nil {
		t.Errorf("install should refuse a plugin key that is not a list")
	} else if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
		t.Errorf("failure kind = %v, want %v", kind, contract.FailureInvalidInput)
	}
}

func TestUninstallLeavesOtherPluginsAlone(t *testing.T) {
	line := at(t)
	writeConfig(t, line, `{"theme": "smoke-theme", "plugin": ["./plugins/mine.tsx"]}`)
	if _, err := line.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	report, err := line.Uninstall()
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !report.Removed || !report.Undeclared {
		t.Errorf("uninstall should remove and undeclare, got removed=%v undeclared=%v", report.Removed, report.Undeclared)
	}
	if report.ConfigRemoved {
		t.Errorf("a config holding somebody else's plugin must survive")
	}
	if got := declared(t, line); !equal(got, []string{"./plugins/mine.tsx"}) {
		t.Errorf("declared = %q, want the other plugin alone", got)
	}
	if _, err := os.Stat(line.Plugin); !os.IsNotExist(err) {
		t.Errorf("the plugin file should be gone, stat gave %v", err)
	}
}

// The opposite case, and the reason both are reported separately: when the file
// exists only because install wrote it, uninstall takes it away instead of
// leaving an empty list behind.
func TestUninstallRemovesAConfigItHadWrittenItself(t *testing.T) {
	line := at(t)
	if _, err := line.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	report, err := line.Uninstall()
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !report.ConfigRemoved {
		t.Errorf("a config holding nothing else should be removed")
	}
	if _, err := os.Stat(line.TUIConfig); !os.IsNotExist(err) {
		t.Errorf("config should be gone, stat gave %v", err)
	}
}

func TestUninstallOnAMachineWithNothingInstalledSaysSo(t *testing.T) {
	line := at(t)

	report, err := line.Uninstall()
	if err != nil {
		t.Fatalf("uninstall should not fail on a clean machine: %v", err)
	}
	if report.Removed || report.Undeclared || report.ConfigRemoved {
		t.Errorf("nothing was installed, so nothing should be reported as undone: %+v", report)
	}
}

func TestStatusOnACleanMachineReportsAbsenceAndNotAFailure(t *testing.T) {
	line := at(t)

	state := line.Status()
	if state.Present || state.Declared || state.Current {
		t.Errorf("clean machine: %+v", state)
	}
	if state.Shipped == "" {
		t.Errorf("the shipped digest is known without anything installed")
	}
	if state.Installed != "" {
		t.Errorf("installed digest = %q, want empty", state.Installed)
	}
}

// A file present but undeclared loads nothing, and the two facts are reported
// apart so the remedy can be the right one.
func TestStatusSeparatesPresentFromDeclared(t *testing.T) {
	line := at(t)
	if _, err := line.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.Remove(line.TUIConfig); err != nil {
		t.Fatalf("remove config: %v", err)
	}

	state := line.Status()
	if !state.Present {
		t.Errorf("the file is still there")
	}
	if state.Declared {
		t.Errorf("nothing declares it any more")
	}
}

// Both widgets are asked for by name, and the names are the contract the command
// prints. A typo must not quietly resolve to the default: installing Atenea's
// traffic light when somebody asked for a share is the failure this refuses.
func TestForRefusesAWidgetThisBinaryDoesNotCarry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if _, err := statusline.For("session-shares"); err == nil {
		t.Fatal("a name that does not exist should be refused, not resolved")
	} else {
		if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
			t.Errorf("kind = %v, want %v", kind, contract.FailureInvalidInput)
		}
		// The message has to name the choices: this error is read by somebody who
		// just guessed at a name.
		for _, name := range statusline.Names() {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error does not name %q: %v", name, err)
			}
		}
	}

	for _, name := range statusline.Names() {
		if _, err := statusline.For(name); err != nil {
			t.Errorf("For(%q): %v", name, err)
		}
	}
}

// Every shipped widget has to carry a real embedded source. A widget added to the
// list without adding it to the embed pattern installs an empty file, and the
// client draws nothing -- which looks exactly like a plugin that never loaded.
func TestEveryShippedWidgetInstallsSomething(t *testing.T) {
	for _, widget := range statusline.Widgets() {
		t.Run(widget.Name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			line, err := statusline.For(widget.Name)
			if err != nil {
				t.Fatalf("For: %v", err)
			}
			if _, err := line.Install(); err != nil {
				t.Fatalf("install: %v", err)
			}

			body, err := os.ReadFile(line.Plugin)
			if err != nil {
				t.Fatalf("plugin: %v", err)
			}
			if len(body) == 0 {
				t.Fatal("installed an empty file")
			}
			if !strings.Contains(string(body), "export default") {
				t.Errorf("what landed is not a plugin:\n%s", firstLines(string(body)))
			}
			if !line.Status().Current {
				t.Error("a fresh install does not match the binary")
			}
			if widget.Summary == "" {
				t.Error("a widget with no summary cannot be listed by the command")
			}
		})
	}
}

// Where a widget lands was asked for and then got lost: the first version of both
// of these registered whichever slot this code already knew about -- one on the
// prompt line, one at the bottom of the app -- and neither is where the question
// was. The client's sidebar is an ordered column and its own sections claim round
// numbers, Context at 100 and MCP at 200, so a widget of ours belongs strictly
// between them. This reads the source that ships, because the source is the
// contract: a plugin compiled from a file with the wrong slot name registers
// nothing and draws nothing, which looks exactly like a plugin that never loaded.
func TestEveryWidgetLandsInTheSidebarBetweenContextAndMCP(t *testing.T) {
	const (
		context = 100
		mcp     = 200
	)
	order := regexp.MustCompile(`order:\s*(\d+)`)

	for _, widget := range statusline.Widgets() {
		t.Run(widget.Name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			line, err := statusline.For(widget.Name)
			if err != nil {
				t.Fatalf("For: %v", err)
			}
			if _, err := line.Install(); err != nil {
				t.Fatalf("install: %v", err)
			}
			raw, err := os.ReadFile(line.Plugin)
			if err != nil {
				t.Fatalf("plugin: %v", err)
			}
			body := string(raw)

			if !strings.Contains(body, "sidebar_content:") {
				t.Errorf("does not register sidebar_content, so it cannot appear in the sidebar:\n%s", firstLines(body))
			}
			for _, elsewhere := range []string{"app_bottom:", "session_prompt_right:", "session_prompt:", "home_bottom:"} {
				if strings.Contains(body, elsewhere) {
					t.Errorf("registers %s: that is a slot outside the sidebar", elsewhere)
				}
			}

			found := order.FindStringSubmatch(body)
			if found == nil {
				t.Fatal("no order in the registration: the host would place this by load order")
			}
			n, err := strconv.Atoi(found[1])
			if err != nil {
				t.Fatalf("order %q: %v", found[1], err)
			}
			if n <= context || n >= mcp {
				t.Errorf("order = %d, want strictly between Context (%d) and MCP (%d)", n, context, mcp)
			}
		})
	}
}

// The two widgets share a directory and a config file, so the risk is that one
// verb moves the other's entry. Installing both and removing one is the shape
// that catches it.
func TestWidgetsDoNotDisturbEachOther(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	atenea, err := statusline.For("atenea")
	if err != nil {
		t.Fatalf("For(atenea): %v", err)
	}
	share, err := statusline.For("session-share")
	if err != nil {
		t.Fatalf("For(session-share): %v", err)
	}
	if atenea.Plugin == share.Plugin || atenea.Declaration() == share.Declaration() {
		t.Fatalf("two widgets resolved to one file: %s and %s", atenea.Plugin, share.Plugin)
	}

	if _, err := atenea.Install(); err != nil {
		t.Fatalf("install atenea: %v", err)
	}
	if _, err := share.Install(); err != nil {
		t.Fatalf("install share: %v", err)
	}
	if got := declared(t, atenea); len(got) != 2 {
		t.Fatalf("declared = %q, want both widgets", got)
	}

	if _, err := share.Uninstall(); err != nil {
		t.Fatalf("uninstall share: %v", err)
	}
	if got := declared(t, atenea); len(got) != 1 || got[0] != atenea.Declaration() {
		t.Errorf("declared = %q, want only %q", got, atenea.Declaration())
	}
	if !atenea.Status().Present {
		t.Error("removing one widget took the other's file with it")
	}
	if share.Status().Present {
		t.Error("the uninstalled widget is still on disk")
	}
}

func declared(t *testing.T, line statusline.Line) []string {
	t.Helper()
	raw, err := os.ReadFile(line.TUIConfig)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var parsed struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, raw)
	}
	return parsed.Plugin
}

func writeConfig(t *testing.T, line statusline.Line, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(line.TUIConfig), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(line.TUIConfig, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func firstLines(body string) string {
	lines := strings.SplitN(body, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "\n")
}
