//go:build darwin || linux

package readscope

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFullAuditNestedPathErrorsCloseDirectoryHandles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "existing"), 0700); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		n := 0
		for fd := 0; fd < 1024; fd++ {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
				n++
			}
		}
		return n
	}
	old := debug.SetGCPercent(-1)
	defer func() { debug.SetGCPercent(old); runtime.GC() }()
	before := count()
	for i := 0; i < 24; i++ {
		if _, err := ReadFile(root, "existing/missing/file.txt", nil); err == nil {
			t.Fatal("fixture unexpectedly exists")
		}
	}
	after := count()
	if after > before+2 {
		t.Fatalf("24 denied reads retained %d directory descriptors (before=%d after=%d)", after-before, before, after)
	}
}
