//go:build darwin

package platform_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestLaunchdAgentIsRenderedForTheCurrentUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s, err := platform.NewService("/opt/atenea/bin/atenea", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(home, "Library", "LaunchAgents", "atenea.plist")
	if s.Unit != wantPath {
		t.Fatalf("plist path = %q, want %q", s.Unit, wantPath)
	}
	text := s.UnitText()
	for _, want := range []string{
		"<key>Label</key><string>com.tutitoos.atenea</string>",
		"<key>ProgramArguments</key>",
		"<string>/opt/atenea/bin/atenea</string><string>run</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist is missing %q:\n%s", want, text)
		}
	}
}

func TestLaunchdAgentEscapesTheBinaryPath(t *testing.T) {
	s, err := platform.NewService("/opt/atenea/&bin", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.UnitText(); !strings.Contains(got, "/opt/atenea/&amp;bin") {
		t.Fatalf("binary path was not XML escaped:\n%s", got)
	}
}

func TestLaunchdQueryReportsAnAbsentAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	state, err := platform.Query("atenea-absent-by-construction")
	if err != nil && contract.KindOf(err) == contract.FailureUnavailable {
		t.Skipf("launchd is unavailable on this machine: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatal("an absent plist was reported as installed")
	}
	if state.Unit != filepath.Join(home, "Library", "LaunchAgents", "atenea-absent-by-construction.plist") {
		t.Fatalf("unit path = %q", state.Unit)
	}
	if state.Detail == "" {
		t.Fatal("absent agent had no status detail")
	}
}
