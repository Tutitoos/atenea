// Package acceptancefixture lets product conformance tests consume the exact
// fixture bytes selected by the sealed acceptance snapshot.
package acceptancefixture

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
)

// Fixture is part of ATENEA's public orchestration contract.
type Fixture struct {
	ID, Language, Check, Path, SHA256 string
	Bytes                             []byte
}

// Load is part of ATENEA's public orchestration contract.
func Load(allowed ...string) (Fixture, bool, error) {
	id := os.Getenv("ATENEA_ACCEPTANCE_ID")
	if id == "" {
		return Fixture{}, false, nil
	}
	if !slices.Contains(allowed, id) {
		return Fixture{}, true, errors.New("unexpected acceptance scenario " + id)
	}
	path, want := os.Getenv("ATENEA_ACCEPTANCE_FIXTURE"), os.Getenv("ATENEA_ACCEPTANCE_SHA256")
	if path == "" || want == "" {
		return Fixture{}, true, errors.New("acceptance fixture identity is incomplete")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, true, err
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return Fixture{}, true, errors.New("acceptance fixture digest mismatch")
	}
	return Fixture{ID: id, Language: os.Getenv("ATENEA_ACCEPTANCE_LANGUAGE"), Check: os.Getenv("ATENEA_ACCEPTANCE_CHECK"), Path: path, SHA256: got, Bytes: raw}, true, nil
}
