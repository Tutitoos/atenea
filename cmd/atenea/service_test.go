package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Three words and nothing else. install and uninstall change the machine, so a
// typo that fell through to one of them would be a real edit made by accident;
// the only safe reading of a word this command does not know is that the
// operator meant something it cannot see.
func TestServiceRefusesAWordItDoesNotKnow(t *testing.T) {
	for _, args := range [][]string{
		{"service"},
		{"service", "conjure"},
		{"service", "instal"}, //nolint:misspell // the near-miss of a real verb is the point
	} {
		_, err := cli(t, args...)
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("%v was answered with %v, want invalid_input", args, got)
		}
	}
}

// The status screen reports; it never fails. A machine with nothing installed,
// or with no service manager at all, is the normal state of somebody who is
// mid-setup and wants to know where they stand -- an exit code there hides the
// answer behind the question.
//
// It also has to name the unit before there is one. Where the unit would go is
// knowable without asking systemd anything, and "install it where?" is the
// first thing the operator needs.
func TestServiceStatusReportsOnAMachineWithNothingInstalled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	out, err := cli(t, "service", "status")
	if err != nil {
		t.Fatalf("a status screen must not fail: %v", err)
	}
	if want := filepath.Join(root, "systemd", "user", "atenea.service"); !strings.Contains(out, want) {
		t.Errorf("the screen never says where the unit goes (%s):\n%s", want, out)
	}
}

// The command is on the front page. Running with the system is the whole point
// of installing Atenea on a machine, and a command nobody can find is a
// command nobody runs.
func TestTheUsageMentionsTheService(t *testing.T) {
	out, err := cli(t, "help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(out, "service install") || !strings.Contains(out, "starts") {
		t.Errorf("usage does not mention the background service:\n%s", out)
	}
}

// A "no" with no remedy is a nag. Lingering is the half of "starts with the
// system" that Atenea cannot switch on for itself, so the one line that fixes
// it has to be on the screen where somebody works out why nothing came up
// after a reboot -- not only in the install output they saw once, weeks ago.
func TestAnInstalledServiceWithoutLingeringIsToldHowToFixIt(t *testing.T) {
	if platform.LingerCommand() == "" {
		t.Skip("no lingering concept on this platform")
	}
	// The text and the remedy are pinned together: a screen that warned
	// without the command, or printed a command that names the wrong user,
	// would both read as helpful and be useless.
	if !strings.Contains(platform.LingerCommand(), "enable-linger") {
		t.Errorf("the remedy does not enable lingering: %q", platform.LingerCommand())
	}
}
