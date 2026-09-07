package workflow

import (
	"context"

	"github.com/Tutitoos/atenea/internal/sourceidentity"
)

// sourceFingerprint is the workflow-facing compatibility seam for the shared
// source identity. Keeping this wrapper preserves old receipts and tests while
// making cache and workflow invalidation use exactly the same evidence.
func sourceFingerprint(root string) (string, error) {
	identity, err := sourceidentity.Discover(context.Background(), root)
	if err != nil {
		return "", err
	}
	return identity.Fingerprint, nil
}
