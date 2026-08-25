package core_test

import (
	"os"
	"testing"

	"github.com/Tutitoos/atenea/internal/testroot"
)

// TestMain pins the temporary root before any test runs.
//
// This package binds a unix socket in nearly every test, under a directory
// t.TempDir() names after the test itself. The names here are sentences on
// purpose, so the path is long before the socket is appended to it, and on a
// machine whose inherited temporary root is already 49 bytes the bind fails
// with a message about the kernel rather than about the test.
func TestMain(m *testing.M) {
	if _, err := testroot.Pin(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
