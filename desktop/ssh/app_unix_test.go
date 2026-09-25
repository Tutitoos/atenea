//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/sshcontrol/controller"
)

func TestControllerStatusReportsLongSocketPathBeforeStateCreation(t *testing.T) {
	home := filepath.Join(t.TempDir(), strings.Repeat("x", 104))
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_CONFIG_HOME", home)
	} else {
		t.Setenv("HOME", home)
	}
	root, err := controller.Root()
	if err != nil {
		t.Fatal(err)
	}
	view := (&App{}).ControllerStatus()
	if view.State != "error" || !strings.Contains(view.Detail, "demasiado larga") {
		t.Fatalf("unexpected long-path status: %#v", view)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("long endpoint created user state: %v", err)
	}
}
