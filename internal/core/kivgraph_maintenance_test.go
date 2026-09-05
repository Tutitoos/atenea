package core

import (
	"os"
	"runtime"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestUnavailableUserCacheDoesNotBecomeRelativeMaintenancePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves its user cache from different environment variables")
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if directory, err := os.UserCacheDir(); err == nil {
		t.Fatalf("fixture still has user cache directory %q", directory)
	}
	if got := kivgraphMaintenanceDirectoryFor(config.Config{}); got != "" {
		t.Fatalf("maintenance directory = %q, want unavailable signal", got)
	}
}
