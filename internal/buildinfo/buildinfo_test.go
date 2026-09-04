package buildinfo_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The release tag is the constant with a `v` in front, and that one character
// is the whole reason this test exists: a constant that grows the prefix, or a
// tag that loses it, is a mismatch nothing else in the build would notice.
func TestTheVersionIsAPlainThreeNumberSemVer(t *testing.T) {
	if strings.HasPrefix(buildinfo.Version, "v") {
		t.Fatalf("Version = %q: the tag carries the v, the constant does not", buildinfo.Version)
	}
	if _, err := contract.ParseVersion(buildinfo.Version); err != nil {
		t.Fatalf("Version = %q: %v", buildinfo.Version, err)
	}
}

// Reaching 1.0.0 is an explicit product decision. Keep the test so a future
// accidental downgrade cannot silently turn a stable release back into alpha.
func TestTheProductIsStable(t *testing.T) {
	version, err := contract.ParseVersion(buildinfo.Version)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if version.Major != 1 {
		t.Fatalf("Version = %q: stable releases must have major version 1", buildinfo.Version)
	}
}

// Full may add where the build came from and may not change what it is. A
// suffix that is not SemVer build metadata would make the reported version
// compare as a different release from the one it was built from.
func TestFullOnlyEverAddsBuildMetadata(t *testing.T) {
	full := buildinfo.Full()
	if full == buildinfo.Version {
		return
	}
	suffix, found := strings.CutPrefix(full, buildinfo.Version+"+")
	if !found {
		t.Fatalf("Full() = %q, want %q or %q plus +metadata",
			full, buildinfo.Version, buildinfo.Version)
	}
	if suffix == "" {
		t.Fatalf("Full() = %q: a + with nothing after it is not metadata", full)
	}
	for _, r := range suffix {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '.':
		default:
			t.Fatalf("Full() = %q: %q is not allowed in build metadata", full, r)
		}
	}
}

// The status screen and every crash entry print this. Reading build info costs
// a walk of the settings table, so it happens once and the answer is kept --
// and a memoised value that is not stable is a memoised value that is wrong.
func TestFullAnswersTheSameThingTwice(t *testing.T) {
	if first, second := buildinfo.Full(), buildinfo.Full(); first != second {
		t.Fatalf("Full() = %q then %q", first, second)
	}
}

// Documentation that does not say which version it describes is documentation
// the reader has to date by guessing. These three name it, so bumping the
// constant without touching them fails here rather than on somebody's screen.
func TestTheDocumentationNamesThisVersion(t *testing.T) {
	for _, path := range []string{
		"../../README.md",
		"../../CHANGELOG.md",
		"../../docs/content/_index.md",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(body), buildinfo.Version) {
			t.Errorf("%s never names version %s", path, buildinfo.Version)
		}
	}
}

// Some files do not merely mention the version, they declare one: a reader --
// or a packager -- takes the number from them. Those cannot be checked by
// "contains", because a file that still says 1.0.1 contains a version perfectly
// well. docs/hugo.toml said 1.0.1 for four releases and put "Atenea 1.0.1" in
// the footer of every published page whose body said 1.0.5.
func TestEveryDeclaredVersionIsThisOne(t *testing.T) {
	for _, declaration := range []struct {
		path    string
		pattern *regexp.Regexp
	}{
		// params.version, which the theme prints in the site footer.
		{"../../docs/hugo.toml", regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]+)"`)},
		// The Claude Desktop extension manifest, which the packer copies
		// verbatim and whose number the build script compares the binary to.
		{"../../packaging/claude-desktop/manifest.json", regexp.MustCompile(`"version"\s*:\s*"([^"]+)"`)},
		// The README describes the source version independently of published
		// artifacts. Its installer asks the reader for an existing release:
		// requiring a URL for this constant would advertise a missing asset
		// whenever the checkout is ahead of the last publication.
		{"../../README.md", regexp.MustCompile("(?m)^\\*\\*Source version:\\*\\* `([0-9]+\\.[0-9]+\\.[0-9]+)`")},
		// The status line as getting-started draws it. A reader compares their
		// own screen against this, so a stale number reads as their install
		// being wrong.
		{"../../docs/content/getting-started.md", regexp.MustCompile(`atenea ([0-9]+\.[0-9]+\.[0-9]+)\s+contract`)},
	} {
		body, err := os.ReadFile(declaration.path)
		if err != nil {
			t.Errorf("read %s: %v", declaration.path, err)
			continue
		}
		found := declaration.pattern.FindSubmatch(body)
		if found == nil {
			t.Errorf("%s declares no version at all", declaration.path)
			continue
		}
		if got := string(found[1]); got != buildinfo.Version {
			t.Errorf("%s declares version %s at /%s/, this build is %s",
				declaration.path, got, declaration.pattern, buildinfo.Version)
		}
	}
}

// A changelog with no entry for the version being shipped is the failure mode
// worth catching: the release happens, the notes are written "later", and
// later is never.
func TestTheChangelogHasAnEntryForThisVersion(t *testing.T) {
	body, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	heading := "## [" + buildinfo.Version + "]"
	if !strings.Contains(string(body), heading) {
		t.Fatalf("CHANGELOG.md has no %q section", heading)
	}
}

// The comments in this package illustrate with a version number, and an
// illustration nobody re-reads rots. They said 0.1.0 -- "this IS 0.1.0, built
// from that tree" and `go install atenea@v0.1.0` -- through every release from
// 1.0.0 to 1.0.5, telling the next reader the package belongs to a 0.x product
// while the constant a few lines above them said otherwise. Every three-number
// version written into this file is meant to be the one being shipped, so the
// bump that forgets an example fails here.
func TestThePackageCommentsIllustrateWithThisVersion(t *testing.T) {
	body, err := os.ReadFile("buildinfo.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	written := regexp.MustCompile(`\d+\.\d+\.\d+`).FindAllString(string(body), -1)
	if len(written) == 0 {
		t.Fatal("buildinfo.go names no version at all; this test would pass for an empty file")
	}
	for _, version := range written {
		if version != buildinfo.Version {
			t.Errorf("buildinfo.go illustrates with %s, this build is %s",
				version, buildinfo.Version)
		}
	}
}
