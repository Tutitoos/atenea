//go:build darwin

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
