package checkpoint

import (
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A full disk is not a permission problem, and the bin cannot say so: it stays
// permission_denied because nothing a provider or a caller did caused it, and
// the health record exempts that bin for exactly that reason. So the sentence
// has to carry the fact.
//
// Measured on a real filled disk during the adversarial pass: the receipt
// failed with `permission_denied` and the ENOSPC text trailing at the end of a
// line about a run id, which reads as a mode bit. The first place anybody
// takes permission_denied is `ls -l`, not `df`.
//
// This test is in-package because a full filesystem cannot be produced
// honestly from a unit test without mounting one; the classifier is where the
// decision lives, and every write in this package routes through it.
func TestAFullDiskNamesItselfBeforeAnythingElse(t *testing.T) {
	full := diskFailure(&fs.PathError{Op: "write", Path: "/tmp/x", Err: syscall.ENOSPC},
		"run %s: %v", "20260802T120000-abc123", syscall.ENOSPC)

	if contract.KindOf(full) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied: the bin the health record exempts",
			contract.KindOf(full))
	}
	// Leading, not trailing. Trailing is the shape that sent a reader to the
	// mode bits: the line opens with a run id and the disk is the last clause.
	// The bin prefixes every failure, so "first" means first after it.
	if !strings.HasPrefix(full.Error(), "permission_denied: no space left on device") {
		t.Errorf("error = %q, want the disk named first", full)
	}
	if !strings.Contains(full.Error(), "20260802T120000-abc123") {
		t.Errorf("error = %q, want it to still say which run was lost", full)
	}

	denied := diskFailure(&fs.PathError{Op: "open", Path: "/tmp/x", Err: syscall.EACCES},
		"run %s: %v", "20260802T120000-abc124", syscall.EACCES)
	if strings.Contains(denied.Error(), "no space left on device") {
		t.Errorf("error = %q, want a permission failure to stay a permission failure", denied)
	}
	if contract.KindOf(denied) != contract.FailurePermissionDenied {
		t.Errorf("kind = %v, want permission_denied", contract.KindOf(denied))
	}
}
