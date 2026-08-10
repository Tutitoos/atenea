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

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

//go:embed opencode/atenea.tsx
var shipped embed.FS

const (
	// The client is named in one place. Only opencode has a slot to hang a line
	// in today; a second client gets a flag on that day and not before, because
	// a flag with one value is a promise the code cannot keep.
	clientName = "opencode"

	sourcePath = "opencode/atenea.tsx"

	// Where the plugin lands, and how it is named in the config. The client
	// resolves a relative entry against its own config directory, so the
	// declaration stays relative: an absolute path would break the moment a
	// config directory is copied or XDG_CONFIG_HOME moves.
	pluginDir   = "plugins-tui"
	pluginFile  = "atenea.tsx"
	declaration = "./" + pluginDir + "/" + pluginFile

	// The client's TUI configuration, and the key inside it that lists plugins.
	tuiConfigFile = "tui.json"
	pluginKey     = "plugin"
)

// Line is where the status line goes on this machine.
type Line struct {
	// ConfigDir is the client's configuration directory, not Atenea's.
	ConfigDir string
	// Plugin is the absolute path of the file this package writes.
	Plugin string
	// TUIConfig is the absolute path of the file that declares it.
	TUIConfig string
}

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

// New resolves where the status line lives for the user running this binary.
func New() Line {
	dir := filepath.Join(platform.ConfigHome(), clientName)
	return Line{
		ConfigDir: dir,
		Plugin:    filepath.Join(dir, pluginDir, pluginFile),
		TUIConfig: filepath.Join(dir, tuiConfigFile),
	}
}

// Install writes the plugin and declares it, and is safe to run twice: the file
// is replaced with the shipped copy and the declaration is added only if it is
// not already there.
func (l Line) Install() (Report, error) {
	source, err := shipped.ReadFile(sourcePath)
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

	if source, err := shipped.ReadFile(sourcePath); err == nil {
		state.Shipped = digest(source)
	}
	if installed, err := os.ReadFile(l.Plugin); err == nil {
		state.Present = true
		state.Installed = digest(installed)
	}
	state.Current = state.Present && state.Shipped != "" && state.Shipped == state.Installed

	entries, _, err := l.entries()
	if err == nil {
		state.Declared = slices.Contains(entries, declaration)
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
			l.TUIConfig, err, declaration, pluginKey)
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
	if slices.Contains(entries, declaration) {
		return false, nil
	}
	entries = append(entries, declaration)
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
	kept := slices.DeleteFunc(slices.Clone(entries), func(entry string) bool { return entry == declaration })
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

	if err := os.MkdirAll(filepath.Dir(l.TUIConfig), 0o755); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"cannot create %s: %v", filepath.Dir(l.TUIConfig), err)
	}
	if err := os.WriteFile(l.TUIConfig, body, 0o644); err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot write %s: %v", l.TUIConfig, err)
	}
	return nil
}

// Client is the name of the client this line is for, for messages.
func Client() string { return clientName }

// Declaration is how the plugin is named inside the client's config, printed by
// the command so somebody editing the file by hand copies the same string.
func Declaration() string { return declaration }

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
