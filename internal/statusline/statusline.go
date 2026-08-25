// Package statusline puts Atenea's own status line on a client's screen.
//
// The line is a plugin for opencode's terminal UI: it reads Atenea's status
// socket -- the same door the CLI knocks on -- and reports the traffic light,
// the version actually running, and unread incidents. Nothing here talks to the
// service. This package only writes a file onto the machine and declares it in
// the client's configuration, which is the same job `service install` does for a
// systemd unit and is why it wears the same three verbs.
//
// The plugin source is embedded in the binary rather than read from a checkout,
// so `status` can say whether the file on disk is the one this binary ships.
// Installing from a path would make that question unanswerable, which is the
// failure this repository has already paid for twice: a tool that reports on a
// copy nobody is running.
package statusline

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

//go:embed opencode/atenea.tsx opencode/session-share.tsx opencode/limits.tsx
var shipped embed.FS

const (
	// The client is named in one place. Only opencode has a slot to hang a line
	// in today; a second client gets a flag on that day and not before, because
	// a flag with one value is a promise the code cannot keep.
	clientName = "opencode"

	// Where the plugin lands. Each widget names its own file inside it, and the
	// declaration stays relative: the client resolves a relative entry against
	// its own config directory, so an absolute path would break the moment that
	// directory is copied or XDG_CONFIG_HOME moves.
	pluginDir = "plugins-tui"

	// The client's TUI configuration, and the key inside it that lists plugins.
	tuiConfigFile = "tui.json"
	pluginKey     = "plugin"
)

// Widget is one line this binary can hang on a client's screen.
//
// There are three, and they are installed separately on purpose: somebody who
// wants Atenea's traffic light has not thereby asked for a per-model share of
// their session, and two of the three read no Atenea at all. Bundling them under
// one verb would make "install the status line" mean three unrelated readings.
type Widget struct {
	// Name is how the widget is asked for on the command line.
	Name string
	// Summary is the one line the command prints beside the name.
	Summary string

	source string
	file   string
}

var widgets = []Widget{
	{
		Name:    "atenea",
		Summary: "Atenea's traffic light, the version running and unread incidents",
		source:  "opencode/atenea.tsx",
		file:    "atenea.tsx",
	},
	{
		Name:    "session-share",
		Summary: "which model did what share of this session's tokens",
		source:  "opencode/session-share.tsx",
		file:    "session-share.tsx",
	},
	{
		Name:    "limits",
		Summary: "how much of each provider's live rate-limit window is used",
		source:  "opencode/limits.tsx",
		file:    "limits.tsx",
	},
}

// Widgets is what this binary carries, in the order the command lists them.
func Widgets() []Widget { return slices.Clone(widgets) }

// Names is every widget name, for messages that have to spell out the choices.
func Names() []string {
	out := make([]string, 0, len(widgets))
	for _, w := range widgets {
		out = append(out, w.Name)
	}
	return out
}

// Line is where one widget goes on this machine.
type Line struct {
	// Widget is which line this is: what gets written, and under what name.
	Widget Widget
	// ConfigDir is the client's configuration directory, not Atenea's.
	ConfigDir string
	// Plugin is the absolute path of the file this package writes.
	Plugin string
	// TUIConfig is the absolute path of the file that declares it.
	TUIConfig string
}

// Declaration is how this widget is named inside the client's config, printed by
// the command so somebody editing the file by hand copies the same string.
func (l Line) Declaration() string { return "./" + pluginDir + "/" + l.Widget.file }

// State is what is on the machine right now.
type State struct {
	Line
	// Present is true when the plugin file exists.
	Present bool
	// Declared is true when the client's config lists it.
	Declared bool
	// Current is true when the file on disk is byte for byte the one this binary
	// ships. False with Present true is a drift: an older or edited copy is what
	// the client would load, and the version on the screen would be reporting a
	// file this binary never saw.
	Current bool
	// Shipped and Installed are the two digests behind Current, printed so a
	// mismatch can be told apart from a missing file at a glance.
	Shipped   string
	Installed string
}

// New resolves where Atenea's own line lives for the user running this binary.
// It is the default because it is the one this repository is about.
func New() Line { return lineFor(widgets[0]) }

// For resolves a named widget, and refuses a name it does not carry rather than
// installing something the caller did not ask for.
func For(name string) (Line, error) {
	for _, w := range widgets {
		if w.Name == name {
			return lineFor(w), nil
		}
	}
	return Line{}, contract.Fail(contract.FailureInvalidInput,
		"unknown widget %q: this binary carries %s", name, strings.Join(Names(), " and "))
}

// All is every widget's line, for a command that reports on the whole screen.
func All() []Line {
	out := make([]Line, 0, len(widgets))
	for _, w := range widgets {
		out = append(out, lineFor(w))
	}
	return out
}

func lineFor(w Widget) Line {
	dir := filepath.Join(platform.ConfigHome(), clientName)
	return Line{
		Widget:    w,
		ConfigDir: dir,
		Plugin:    filepath.Join(dir, pluginDir, w.file),
		TUIConfig: filepath.Join(dir, tuiConfigFile),
	}
}

// Install writes the plugin and declares it, and is safe to run twice: the file
// is replaced with the shipped copy and the declaration is added only if it is
// not already there.
func (l Line) Install() (Report, error) {
	source, err := shipped.ReadFile(l.Widget.source)
	if err != nil {
		// Unreachable while the embed directive above matches a real file, and
		// worth reporting rather than panicking: a binary built with a broken
		// embed should say so instead of dying in a config directory.
		return Report{}, contract.Fail(contract.FailureUnavailable,
			"this binary does not carry the status line source: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(l.Plugin), 0o755); err != nil {
		return Report{}, contract.Fail(contract.FailureUnavailable,
			"cannot create %s: %v", filepath.Dir(l.Plugin), err)
	}
	if err := os.WriteFile(l.Plugin, source, 0o644); err != nil {
		return Report{}, contract.Fail(contract.FailureUnavailable,
			"cannot write %s: %v", l.Plugin, err)
	}

	declared, err := l.declare()
	if err != nil {
		return Report{}, err
	}
	return Report{Plugin: l.Plugin, TUIConfig: l.TUIConfig, Wrote: true, Declared: declared}, nil
}

// Uninstall takes the line off the machine: the declaration first, so a config
// that survives never points at a file that does not.
func (l Line) Uninstall() (Report, error) {
	undeclared, removedConfig, err := l.undeclare()
	if err != nil {
		return Report{}, err
	}

	removed := true
	if err := os.Remove(l.Plugin); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return Report{}, contract.Fail(contract.FailureUnavailable,
				"cannot remove %s: %v", l.Plugin, err)
		}
		removed = false
	}
	// The directory goes only when this was the last thing in it. Somebody
	// else's plugin living beside ours is not ours to delete.
	_ = os.Remove(filepath.Dir(l.Plugin))

	return Report{
		Plugin:        l.Plugin,
		TUIConfig:     l.TUIConfig,
		Removed:       removed,
		Undeclared:    undeclared,
		ConfigRemoved: removedConfig,
	}, nil
}

// Status reports and does not fail on a file it cannot read: a machine where
// something is wrong with the config is exactly where somebody is looking at
// this output, and an error would replace the reading with nothing.
func (l Line) Status() State {
	state := State{Line: l}

	if source, err := shipped.ReadFile(l.Widget.source); err == nil {
		state.Shipped = digest(source)
	}
	if installed, err := os.ReadFile(l.Plugin); err == nil {
		state.Present = true
		state.Installed = digest(installed)
	}
	state.Current = state.Present && state.Shipped != "" && state.Shipped == state.Installed

	entries, _, err := l.entries()
	if err == nil {
		state.Declared = slices.Contains(entries, l.Declaration())
	}
	return state
}

// Report is what changed, so the command can print facts instead of a verb.
type Report struct {
	Plugin    string
	TUIConfig string

	Wrote    bool
	Declared bool

	Removed       bool
	Undeclared    bool
	ConfigRemoved bool
}

// entries reads the declared plugin list along with every other key in the file,
// which is what lets the file be written back without losing anything.
//
// A missing file is not an error: it is the ordinary state of a machine where
// nobody has configured the client's TUI yet.
func (l Line) entries() ([]string, map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(l.TUIConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, nil, contract.Fail(contract.FailureUnavailable,
			"cannot read %s: %v", l.TUIConfig, err)
	}

	keys := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &keys); err != nil {
		// Refused rather than repaired. This file belongs to the client and may
		// carry comments or a shape this code has never seen; rewriting it from
		// a partial parse would destroy work to save a keystroke.
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"%s is not readable as JSON, so it will not be edited: %v\nadd %q to its %q list by hand",
			l.TUIConfig, err, l.Declaration(), pluginKey)
	}

	listed, ok := keys[pluginKey]
	if !ok {
		return nil, keys, nil
	}
	var entries []string
	if err := json.Unmarshal(listed, &entries); err != nil {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"%s has a %q that is not a list of paths, so it will not be edited: %v",
			l.TUIConfig, pluginKey, err)
	}
	return entries, keys, nil
}

// declare adds the plugin to the client's list, and reports whether it had to.
func (l Line) declare() (bool, error) {
	entries, keys, err := l.entries()
	if err != nil {
		return false, err
	}
	if slices.Contains(entries, l.Declaration()) {
		return false, nil
	}
	entries = append(entries, l.Declaration())
	if err := l.writeConfig(entries, keys); err != nil {
		return false, err
	}
	return true, nil
}

// undeclare removes the plugin from the list, and removes the file when taking
// our entry out leaves nothing behind. It reports both, because "the config is
// gone" and "the config no longer mentions us" are different states of the
// machine and a caller printing one for the other would be lying quietly.
func (l Line) undeclare() (undeclared bool, removedConfig bool, err error) {
	entries, keys, err := l.entries()
	if err != nil {
		return false, false, err
	}
	kept := slices.DeleteFunc(slices.Clone(entries), func(entry string) bool { return entry == l.Declaration() })
	if len(kept) == len(entries) {
		return false, false, nil
	}

	if len(kept) == 0 && len(keys) == 1 {
		// The only key was the list, and the only entry in it was ours: the file
		// exists because this command wrote it, so this command takes it away.
		if err := os.Remove(l.TUIConfig); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, false, contract.Fail(contract.FailureUnavailable,
				"cannot remove %s: %v", l.TUIConfig, err)
		}
		return true, true, nil
	}
	if err := l.writeConfig(kept, keys); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// writeConfig puts the file back with every other key intact.
//
// Keys come out in alphabetical order, because a JSON object read into a map has
// no order left to preserve. That is a cosmetic change to somebody else's file
// and the reason this code refuses anything it cannot parse cleanly: reordering
// keys is survivable, dropping a comment is not. Nested values come back
// re-indented for the same reason, and for the same price.
func (l Line) writeConfig(entries []string, keys map[string]json.RawMessage) error {
	listed, err := json.Marshal(entries)
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot encode the plugin list: %v", err)
	}
	keys[pluginKey] = listed

	body, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot encode %s: %v", l.TUIConfig, err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(l.TUIConfig)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"cannot create %s: %v", dir, err)
	}
	return replaceFile(l.TUIConfig, body)
}

// replaceFile puts body at path in one step, the way internal/platform writes
// a unit file: a temporary file beside it, then a rename.
//
// This file belongs to the client, and everything in it that is not the plugin
// list belongs to whoever put it there. os.WriteFile truncates in place, so an
// interruption between the truncate and the last byte -- a full disk, a killed
// process, a machine losing power -- leaves the client's configuration short or
// empty, and the keys this package went out of its way to carry through are
// gone anyway. A rename cannot half-happen: a reader sees the old file or the
// new one.
func replaceFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot write %s: %v", path, err)
	}
	tmp := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailureUnavailable, "cannot write %s: %v", path, err)
	}
	// CreateTemp makes the file 0600. The client's own config is an ordinary
	// readable file, and a rename carries the temporary file's mode with it, so
	// this is set before the rename rather than after -- a config that arrives
	// unreadable to the tools that share this directory is a different failure
	// from the one this function exists to prevent.
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailureUnavailable, "cannot write %s: %v", path, err)
	}
	if err := temp.Close(); err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot write %s: %v", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot write %s: %v", path, err)
	}
	keep = true
	return nil
}

// Client is the name of the client this line is for, for messages.
func Client() string { return clientName }

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
