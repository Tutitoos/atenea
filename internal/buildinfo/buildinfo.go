// Package buildinfo carries the version of the running binary.
package buildinfo

import (
	"runtime/debug"
	"strings"
	"sync"
)

// Version is the Atenea product version: the number a release is tagged with.
//
// It follows three-number SemVer and is independent from the contract version
// in pkg/contract: the product is in alpha (0.x.y) and reaches 1.0.0 only when
// it goes stable, while the wire contract adapters compile against is already a
// commitment.
//
// It is a constant rather than a link-time variable on purpose. A version
// injected with -ldflags is one somebody has to remember to inject, and the
// build that forgets does not fail -- it ships claiming to be whatever the
// source said, which is the one error nobody notices. Here the source is the
// only answer, and the release workflow refuses a tag that disagrees with it.
const Version = "0.10.0"

// Full is Version plus what this particular build knows about where it came
// from, as SemVer build metadata.
//
// The number alone cannot tell a release apart from a working tree that
// happens to sit on the same commit, and a crash report from the second one is
// worth much less without saying so. A binary built from a checkout appends its
// revision, and a dirty tree says `modified` -- it is not a release, whatever
// the number claims.
//
// Build metadata is ignored when SemVer versions are compared, which is the
// right meaning: this IS 0.1.0, built from that tree.
var Full = sync.OnceValue(func() string { return stamp(vcs()) })

// vcs reads where this build came from. It answers empty for the normal shape
// of a release artifact: `go install atenea@v0.1.0` and a build from an
// unpacked source archive both land here, and neither has anything truthful to
// add to the number.
func vcs() (revision string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return revision, modified
}

// stamp renders what a build reports. It is separate from vcs because a Go
// test binary carries no VCS stamp of its own: read through the real thing,
// this rendering is never reached from a test and every assertion about it
// passes by never running. Handed its inputs, it can be held to its contract.
func stamp(revision string, modified bool) string {
	if revision == "" {
		return Version
	}
	var out strings.Builder
	out.Grow(len(Version) + 18)
	out.WriteString(Version)
	out.WriteByte('+')
	out.WriteString(shortRevision(revision))
	if modified {
		out.WriteString(".modified")
	}
	return out.String()
}

// shortRevision trims a git object name to the length people actually quote.
// The full forty characters carry no more meaning on a status screen and push
// the rest of the line off narrow terminals.
func shortRevision(revision string) string {
	const short = 7
	if len(revision) > short {
		return revision[:short]
	}
	return revision
}
