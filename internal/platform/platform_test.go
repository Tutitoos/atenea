package platform_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
)

func TestTheStateRootFollowsTheConvention(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	if got, want := platform.StateDir(), "/tmp/state/atenea"; got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

func TestTheConfigRootFollowsTheConvention(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/cfg")
	if got, want := platform.ConfigDir(), "/tmp/cfg/atenea"; got != want {
		t.Errorf("ConfigDir = %q, want %q", got, want)
	}
}

// The one claim the backup location has to keep. A copy under the tree it
// copies recurses into itself, and dies with the tree it exists to survive: an
// `rm -rf` of the state root would take every backup with it.
func TestBackupsLiveBesideTheStateRootAndNotInsideIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	state, backups := platform.StateDir(), platform.BackupDir()

	if strings.HasPrefix(backups, state+string(filepath.Separator)) || backups == state {
		t.Fatalf("backups at %q are inside the state root %q", backups, state)
	}
	if filepath.Dir(backups) != filepath.Dir(state) {
		t.Errorf("backups at %q are not beside the state root %q", backups, state)
	}
}

// Without a home and without the variables Atenea still has to be a usable
// command. Refusing to start over the location of a file nobody has written
// yet would be worse than writing it under the working directory.
func TestAMachineWithNoHomeStillGetsAPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	for name, got := range map[string]string{
		"StateDir":  platform.StateDir(),
		"ConfigDir": platform.ConfigDir(),
		"BackupDir": platform.BackupDir(),
	} {
		if got == "" {
			t.Errorf("%s returned nothing", name)
		}
	}
}

func TestConfigHomeAndBackupServiceInputsStayDeterministic(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/config")
	if got := platform.ConfigHome(); got != "/tmp/config" {
		t.Fatalf("ConfigHome = %q, want /tmp/config", got)
	}
	service, err := platform.NewService("/opt/atenea", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if service.StopGrace != 0 || service.Name != platform.ServiceName {
		t.Fatalf("service = %+v, want clamped grace and canonical name", service)
	}
}
