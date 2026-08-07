// Package contract defines the wire contract shared by the Atenea core and its
// client adapters (omp, Claude Code, OpenCode).
//
// Adapters are dumb translators: they compile against this package and nothing
// else. No internal package may leak into it, and nothing here may know how any
// concrete CLI or tool behaves.
package contract

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a three-number SemVer.
type Version struct {
	Major uint64
	Minor uint64
	Patch uint64
}

// Current is the version of this contract.
//
// It is versioned independently from the Atenea product version: the product is
// in alpha (0.x.y) while the format adapters compile against is already a
// commitment. Major bumps break adapters; minor bumps add fields without
// breaking them; patch is cosmetic.
//
// What that promise covers is the data: the types above and below travel
// between a core and an adapter, and a field added to one is additive by
// construction. The Runner interface is the other half of this package and is
// NOT covered the same way, because it cannot be: a required method added to
// an interface breaks every implementer, and there is no version of "additive"
// that avoids it. Today that costs nobody anything -- adapters are selected by
// name in internal/core, so every Runner in existence is in this repository
// and gets edited in the same commit. It stops being free the moment an
// adapter can be supplied from outside, and that change is the one that has to
// come with a major bump, not the method addition that follows it.
//
// 1.2.0 added the `canceled` failure bin and the `canceled` verdict. Both are
// additive: a core can now say a thing it could not say before, and an adapter
// built against 1.1.0 goes on compiling and never has to send either.
//
// 1.3.0 added `Outcome.Notices`: a channel for an adapter to flag a caveat on
// an otherwise-successful call -- its own index may be behind, say -- without
// failing it. Additive the same way: an adapter built against 1.2.0 never
// populates it, and a core speaking 1.3.0 reads a zero value as "nothing to
// add," never as a claim that nothing needed adding.
//
// 1.4.0 added the `process` effect and `Permission.Grant`. Additive again: a
// capability that always spawned a binary always did so before this, just
// without a name for it; a core speaking 1.4.0 grants it the same way it
// already granted read, and an adapter built against 1.3.0 goes on compiling
// because Effect was already an open uint8, not a closed set it exhausted.
//
// 1.5.0 added `Raw` to `Failure` and `Health`. A generic-bin failure had
// always carried the provider's own untranslated text as far as the error
// value that produced it, and no further -- every adapter already computed
// it, in the one string every classifier trims down to a message and
// discards. Additive the same way as the rest: `Fail`/`WithRaw` is a call an
// adapter opts into, an adapter built against 1.4.0 that never makes it
// leaves `Raw` at its zero value, and a core speaking 1.5.0 reads that as
// nothing more to show, never as a missing answer.
//
// 1.6.0 added `Constraints.RequiresVCS` and `Repository.VCS`. A repository
// nobody has said either way about reads as VCSUnspecified, which never
// disqualifies -- the same reading Scale already gave an unclassified
// repository, kept for the same reason: a fact retrofitted onto a provider
// that already worked must not silently drop every repository that has not
// yet declared it. An adapter built against 1.5.0 goes on compiling because
// it never reads either field; the funnel is what acts on them, not the far
// side of Run.
//
// 1.7.0 added `Repository.SerenaEndpoint`. Empty keeps today's single-URL
// behavior (the adapter default, retarget via activate_project). A set URL
// pins that repository to its own Serena process so two projects stay warm
// without tearing each other down. Additive: an adapter built against 1.6.0
// never reads the field; a core speaking 1.7.0 that sees it empty does what
// it always did.
//
// 1.8.0 added the `IndexProber` interface and `Repository.SetIndexed`. A
// repository's declared `indexed_by` is a starting point typed by hand, and
// nothing before this checked it against the provider it names; IndexProber
// is a runner's optional way to answer "do you already have one" without
// being asked to build it, and SetIndexed is where the answer corrects the
// belief -- in memory only, the same exception SetHealth already is for an
// implementation's own liveness. Additive: implementing IndexProber is
// optional, so an adapter built against 1.7.0 goes on compiling and simply
// never answers the probe; nothing about the shape of a request or an
// outcome changed underneath it.
//
// 1.9.0 added `Implementation.ScopeGuarantee`. The `code.search` scope input
// was already enforced two different ways by two different implementations
// -- ripgrep confines what it can read, an agentic reader like Claude Code
// was only ever asked nicely -- and nothing before this let a caller tell
// which promise they got. ScopeUnspecified reads as the weakest claim, the
// same convention VCS and Scale already use for a fact nobody has declared
// yet. Additive: an adapter built against 1.8.0 goes on compiling and simply
// never sets the field; a core speaking 1.9.0 that sees it unspecified
// treats the implementation as making no claim, not as confined.
//
// Between 1.9.0 and 1.10.0 the Runner interface gained a required
// `Capabilities() []string`, so the core can refuse at load a settings file
// naming an implementation the adapter has no code path for. It carries no
// version number of its own for the reason given above: no number in this
// scheme describes it honestly, and every implementer lives in this
// repository and was edited in the same commit.
//
// 1.10.0 added `Outcome.OutOfScope`. An adapter that cannot confine its own
// search already dropped the matches that strayed and said so in a Notice,
// but a sentence is read once and scored by nothing, so a provider that
// wandered on every call paid nothing for it. The count now travels as a
// number the core records. Additive: an adapter built against 1.9.0 goes on
// compiling and leaves it zero, which reads as "nothing strayed" -- the same
// thing it means for a provider confined by construction.
//
// 1.11.0 added `Constraints.MaxInput`. Every constraint before it asked
// whether a provider could work on this repository; this one asks whether it
// can be asked this question, bounding a declared integer input by name.
// Additive: an adapter built against 1.10.0 goes on compiling and leaves the
// map nil, which bounds nothing -- the same answer the funnel gave before the
// field existed.
//
// 2.0.0 removed `Health.ObservedAt`, and it is the first bump here that is
// not additive. Every entry above adds; this one takes away, so it is major
// by the rule at the top rather than by how much code it moved. An adapter
// built against 1.x that named the field in a composite literal stops
// compiling, and `Supports` refuses the whole 1.x line rather than letting a
// peer discover the gap one field at a time.
//
// What it removed was a field nothing wrote and nothing read. A Health value
// cannot outlive its evidence: `Fault.Health` and `Baseline.Health` both take
// `now` and refuse to speak once FaultWindow or SuccessWindow has passed, so
// a Health that exists at all is one still inside its window. The timestamp
// was a second mechanism for a job the windows already do, and two mechanisms
// for one job is how they drift apart. Deleting it is cheap today -- every
// Runner in existence is in this repository -- and the cost of keeping it was
// a field an outside adapter would eventually populate, believing the core
// read it.
//
// 2.1.0 added `Field.Enum`, closing a declared string input to a fixed set of
// values. Every field type before it said what shape a value has; this one
// says which values exist. Additive: an adapter built against 2.0.0 goes on
// compiling and leaves the slice nil, which closes nothing -- the same answer
// the validator gave before the field existed.
//
// It was added for a caller that cannot be asked. Prose already named these
// sets -- "incoming", "outgoing" or "both" -- and a human reading the summary
// inferred the boundary correctly. A machine building a request from a
// generated schema cannot infer it, and finds the edge by being refused. A
// refusal is a round trip and a confused caller, so the set is now declared
// where a schema can carry it rather than only where a person can read it.
var Current = Version{Major: 2, Minor: 1, Patch: 0}

// ParseVersion reads a MAJOR.MINOR.PATCH string.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", s)
	}
	var v Version
	for i, dst := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(parts[i], 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: field %d is not a number", s, i+1)
		}
		*dst = n
	}
	return v, nil
}

func (v Version) String() string {
	return strconv.FormatUint(v.Major, 10) + "." +
		strconv.FormatUint(v.Minor, 10) + "." +
		strconv.FormatUint(v.Patch, 10)
}

// Supports reports whether a peer speaking version other can talk to a core
// speaking v.
//
// A peer that lags behind by minor versions is accepted on purpose: an adapter
// must not die the moment the core gains a field it does not know about. A peer
// ahead of the core is refused, because the core cannot honor a field it has
// never heard of.
func (v Version) Supports(other Version) bool {
	return v.Major == other.Major && other.Minor <= v.Minor
}
