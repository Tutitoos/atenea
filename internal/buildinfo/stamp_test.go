package buildinfo

import "testing"

// stamp is tested from inside the package on purpose. A Go test binary carries
// no VCS stamp, so every one of these cases is unreachable through Full(): an
// assertion written against it would pass by never running the code it names.
func TestWhatABuildReportsAboutWhereItCameFrom(t *testing.T) {
	const revision = "9b34dd0215c098a22a0ff7bd6e2be40b2aacac02"

	cases := []struct {
		name     string
		revision string
		modified bool
		want     string
		why      string
	}{
		{
			name: "a release artifact has nothing to add",
			want: Version,
			why:  "no checkout means no revision worth quoting",
		},
		{
			name:     "a clean checkout says which commit",
			revision: revision,
			want:     Version + "+9b34dd0",
			why:      "the reader can check that revision against the tag",
		},
		{
			name:     "a dirty tree says so",
			revision: revision,
			modified: true,
			want:     Version + "+9b34dd0.modified",
			why:      "whatever the number claims, this is not a release",
		},
		{
			name:     "a revision shorter than the trim survives whole",
			revision: "9b34d",
			want:     Version + "+9b34d",
			why:      "trimming must never reach past the end of the name",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stamp(c.revision, c.modified); got != c.want {
				t.Fatalf("stamp(%q, %v) = %q, want %q -- %s",
					c.revision, c.modified, got, c.want, c.why)
			}
		})
	}
}

// The separator is the whole difference between build metadata and a
// pre-release tag. `0.1.0+abc` is 0.1.0 built from abc; `0.1.0-abc` is a
// version that sorts BEFORE 0.1.0 and is a different release.
func TestTheRevisionIsBuildMetadataAndNotAPreRelease(t *testing.T) {
	got := stamp("9b34dd0215c098a22a0ff7bd6e2be40b2aacac02", false)
	if got[len(Version)] != '+' {
		t.Fatalf("stamp() = %q: %q separates a pre-release, not build metadata",
			got, got[len(Version)])
	}
}
