// Package buildinfo carries the version of the running binary.
package buildinfo

import (
	"encoding/hex"
	"runtime/debug"
	"strings"
	"sync"
)

// Version is the Atenea product version: the number a release is tagged with.
//
// It follows three-number SemVer and is independent from the contract version
// in pkg/contract: the product is stable at 1.x.y, while the wire contract
// adapters compile against is an independent commitment.
//
// It is a constant rather than a link-time variable on purpose. A version
// injected with -ldflags is one somebody has to remember to inject, and the
// build that forgets does not fail -- it ships claiming to be whatever the
// source said, which is the one error nobody notices. Here the source is the
// only answer, and the release workflow refuses a tag that disagrees with it.
const Version = "1.1.0"

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
// right meaning: this IS 1.1.0, built from that tree.
var Full = sync.OnceValue(func() string { return stamp(vcs()) })

// certificationRevision is set only by the documented P30 build command on
// toolchains that omit VCS settings. It must remain the full Git object id.
var certificationRevision string

// Source returns the full VCS revision embedded by Go and whether the build
// contained uncommitted files. Certification uses it instead of the caller's
// current directory so a moved binary cannot be attributed to another repo.
func Source() (revision string, modified bool) {
	revision, modified = vcs()
	revision, modified, _ = source(revision, modified, certificationRevision)
	return revision, modified
}

// CertificationSource additionally reports whether the revision came from the
// link-time fallback. Callers must verify that build against the clean checkout.
func CertificationSource() (revision string, modified, fallback bool) {
	revision, modified = vcs()
	return source(revision, modified, certificationRevision)
}

func source(revision string, modified bool, fallback string) (string, bool, bool) {
	if revision != "" {
		return revision, modified, false
	}
	stamped := strings.TrimSpace(fallback)
	if (len(stamped) == 40 || len(stamped) == 64) && validHex(stamped) {
		return stamped, false, true
	}
	return "", false, false
}

func validHex(value string) bool { _, err := hex.DecodeString(value); return err == nil }

// vcs reads where this build came from. It answers empty for the normal shape
// of a release artifact: `go install atenea@v1.1.0` and a build from an
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
