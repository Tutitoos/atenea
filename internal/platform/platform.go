// Package platform is the single corner that knows which machine this is.
//
// Three things are genuinely operating-system specific: where data lives, where
// backups live, and how Atenea starts in the background. Everything else in
// Atenea crosses unchanged, so everything else is forbidden from asking. Nothing
// here is imported by the brain -- the packages that own a file ask this for the
// root and join their own name onto it.
//
// The rule this enforces is small and easy to break by accident: resolving the
// state root is eight lines, so a fifth copy of it costs nothing to write and
// silently disagrees with the other four the first time one of them is fixed.
// There is one copy, and it is here.
package platform

import (
	"os"
	"path/filepath"
)

// stateHome, configHome and the fallbacks below follow the XDG base directory
// convention, which is what Linux expects and what macOS tolerates. A machine
// with neither the variable nor a home directory gets a relative path rather
// than an error: a core that cannot find its home is still a usable command,
// and refusing to start over the location of a file nobody has written yet
// would be worse than writing it in the working directory.
const (
	dirName    = "atenea"
	backupName = "atenea-backups"
)

// StateDir is where Atenea keeps what it has learned: the measurement base, the
// run receipts and the crash notebook.
func StateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, dirName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "state", dirName)
	}
	return filepath.Join(home, ".local", "state", dirName)
}

// ConfigDir is where the one settings file lives.
func ConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, dirName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", dirName)
	}
	return filepath.Join(home, ".config", dirName)
}

// BackupDir is where copies of the history go: a folder of its own, beside the
// state root and never inside it.
//
// Beside rather than inside for two reasons, and the second one is the one that
// decides it. A copy under the tree it copies would recurse into itself. And a
// copy under the tree it copies dies with that tree -- an `rm -rf` of the state
// root, or a disk that loses it, takes every backup along with the thing they
// exist to survive. A backup inside what it backs up is not a backup.
func BackupDir() string {
	return filepath.Join(filepath.Dir(StateDir()), backupName)
}
