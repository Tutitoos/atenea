package contract_test

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestParseVersionRoundTrip(t *testing.T) {
	v, err := contract.ParseVersion("2.13.4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Major != 2 || v.Minor != 13 || v.Patch != 4 {
		t.Fatalf("got %+v", v)
	}
	if got := v.String(); got != "2.13.4" {
		t.Fatalf("String() = %q", got)
	}
}

func TestParseVersionRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "1", "1.2", "1.2.3.4", "1.x.3", "v1.2.3", "-1.2.3"} {
		if _, err := contract.ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) should fail", in)
		}
	}
}

// An adapter that lags behind by a minor version must keep working, and one
// that runs ahead of the core must not: those two asymmetric rules are the
// whole point of versioning the contract.
func TestSupportsAcceptsOlderMinorAndRefusesNewer(t *testing.T) {
	core := contract.Version{Major: 1, Minor: 3, Patch: 2}
	cases := []struct {
		peer string
		want bool
	}{
		{"1.3.2", true},
		{"1.3.9", true},  // patch never matters
		{"1.0.0", true},  // adapter lagging behind
		{"1.4.0", false}, // adapter ahead of the core
		{"2.0.0", false}, // breaking change
		{"0.9.0", false},
	}
	for _, tc := range cases {
		peer, err := contract.ParseVersion(tc.peer)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.peer, err)
		}
		if got := core.Supports(peer); got != tc.want {
			t.Errorf("%s.Supports(%s) = %v, want %v", core, peer, got, tc.want)
		}
	}
}
