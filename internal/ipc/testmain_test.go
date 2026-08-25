package ipc_test

import (
	"os"
	"testing"

	"github.com/Tutitoos/atenea/internal/testroot"
)

// TestMain pins the temporary root before any test runs. This package is the
// one that owns maxPath, so a suite that cannot bind here proves nothing about
// the limit it exists to enforce.
func TestMain(m *testing.M) {
	if _, err := testroot.Pin(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
