//go:build darwin

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestDarwinLaunchdSurfaceIsEscapedAndPure(t *testing.T) {
	service, err := NewService("/tmp/atenea&copy", -time.Second)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	text := service.UnitText()
	if !strings.Contains(text, "com.tutitoos.atenea") || !strings.Contains(text, "/tmp/atenea&amp;copy") {
		t.Fatalf("launchd text is not escaped: %s", text)
	}
	if got := LingerCommand(); got != "" {
		t.Fatalf("LingerCommand = %q", got)
	}
	if got := launchdTarget(); !strings.HasPrefix(got, "gui/") || !strings.HasSuffix(got, "/com.tutitoos.atenea") {
		t.Fatalf("launchd target = %q", got)
	}
	if filepath.Base(service.Unit) != "atenea.plist" {
		t.Fatalf("unit = %q", service.Unit)
	}
}

func TestDarwinPlistWriteAndMissingQueryAreRecoverable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "LaunchAgents", "atenea.plist")
	if err := writePlist(path, "plist"); err != nil {
		t.Fatalf("writePlist: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "plist" {
		t.Fatalf("plist = %q, err=%v", got, err)
	}
	state, err := Query("atenea-test-definitely-absent")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if state.Installed || !strings.Contains(state.Detail, "no plist file") {
		t.Fatalf("missing query state = %+v", state)
	}
}

func TestDarwinServiceRejectsRelativeExecutablesAndNormalizesGrace(t *testing.T) {
	for _, executable := range []string{"", "atenea", "./atenea", "bin/atenea"} {
		if _, err := NewService(executable, time.Second); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("NewService(%q) error = %v", executable, err)
		}
	}
	service, err := NewService("/tmp/atenea", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if service.StopGrace != 0 {
		t.Fatalf("negative grace normalized to %v, want zero", service.StopGrace)
	}
	if !strings.Contains(service.UnitText(), "/tmp/atenea") || !strings.Contains(service.UnitText(), "<string>run</string>") {
		t.Fatalf("unit text does not describe executable: %s", service.UnitText())
	}
}

func TestDarwinPlistWriteCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "atenea.plist")
	if err := writePlist(path, "plist"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plist permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestDarwinInstallUninstallAndQueryUseLaunchctl(t *testing.T) {
	launchctl := filepath.Join(t.TempDir(), "launchctl")
	if err := os.WriteFile(launchctl, []byte("#!/bin/sh\necho 'state = running'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(launchctl)+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := Service{Name: "atenea-test", Unit: filepath.Join(t.TempDir(), "atenea.plist"), Exec: "/tmp/atenea", StopGrace: time.Second}
	if err := service.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(service.Unit); err != nil {
		t.Fatalf("installed plist: %v", err)
	}
	if err := service.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(service.Unit); !os.IsNotExist(err) {
		t.Fatalf("plist after uninstall: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	unit := filepath.Join(home, "Library", "LaunchAgents", "atenea-test.plist")
	if err := writePlist(unit, "plist"); err != nil {
		t.Fatal(err)
	}
	state, err := Query("atenea-test")
	if err != nil || !state.Installed || !state.Enabled || !state.Active {
		t.Fatalf("active query = %+v, err=%v", state, err)
	}
}

func TestDarwinLaunchctlFailureIsReported(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "launchctl")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\necho unavailable >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(launcher))
	if _, err := launchctl("print", "gui"); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("launchctl error = %v", err)
	}
}

func TestDarwinInstallAndUninstallSurfaceLaunchctlFailures(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "launchctl")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(launcher))
	service := Service{Name: "atenea-test", Unit: filepath.Join(t.TempDir(), "atenea.plist"), Exec: "/tmp/atenea"}
	if err := service.Install(); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("Install error = %v", err)
	}
	if err := os.WriteFile(service.Unit, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	// This used to assert the opposite -- that Uninstall removed the plist and
	// returned nil however bootout had failed -- which is the defect, not the
	// contract: a bootout that fails for any reason other than "no such job"
	// leaves the agent running, and deleting its plist then reports a stop
	// that did not happen and takes away the only handle left to stop it by.
	if err := service.Uninstall(); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("Uninstall error = %v, want the launchctl failure it hit", err)
	}
	if _, err := os.Stat(service.Unit); err != nil {
		t.Fatalf("the plist of a job that is still loaded was removed anyway: %v", err)
	}
}

// The one bootout refusal an uninstall may ignore: there was no such job
// loaded, so removing the plist is all that was left to do. Without this the
// ordinary second `atenea service uninstall` -- or the first one after a
// logout -- would fail on a machine where nothing is wrong.
func TestDarwinUninstallStillFinishesWhenNoJobWasLoaded(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "launchctl")
	script := "#!/bin/sh\necho 'Could not find specified service' >&2\nexit 3\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(launcher))
	service := Service{Name: "atenea-test", Unit: filepath.Join(t.TempDir(), "atenea.plist"), Exec: "/tmp/atenea"}
	if err := os.WriteFile(service.Unit, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Uninstall(); err != nil {
		t.Fatalf("Uninstall of an installed but unloaded agent: %v", err)
	}
	if _, err := os.Stat(service.Unit); !os.IsNotExist(err) {
		t.Fatalf("the plist of an unloaded agent survived uninstall: %v", err)
	}
}

func TestDarwinPlistWriteRejectsFileAsParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePlist(filepath.Join(parent, "atenea.plist"), "plist"); err == nil {
		t.Fatal("writePlist accepted a file as parent")
	}
}

func TestDarwinPlistWriteRejectsDirectoryTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "atenea.plist")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writePlist(target, "plist"); err == nil {
		t.Fatal("writePlist replaced a directory target")
	}
}

func TestDarwinUninstallReportsRemovalFailure(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "launchctl")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(launcher))
	directory := filepath.Join(t.TempDir(), "atenea.plist")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "child"), []byte("child"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := Service{Unit: directory}
	if err := service.Uninstall(); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("Uninstall error = %v", err)
	}
}
