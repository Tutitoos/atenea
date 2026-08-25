// Package contract defines the wire contract shared by the Atenea core and its
// adapters: omp, Claude Code and Codex for client CLIs, Serena for symbols,
// and Kivgraph and Tokensave for graph and context operations. OpenCode is an
// opt-in model backend rather than an adapter, and does not compile against
// this package.
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
// stable at 1.x.y and the format adapters compile against is its own separate
// commitment, which is why the two numbers do not move together. Major bumps
// break adapters; minor bumps add fields without breaking them; patch is
// cosmetic.
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
// not additive. (It came back in 3.2.0, for a job the windows below turned out
// not to do; the entry there says why. Read on its own this paragraph sends a
// reader looking for a field that exists.) Every entry above adds; this one
// takes away, so it is major by the rule at the top rather than by how much
// code it moved. An adapter built against 1.x that named the field in a
// composite literal stops compiling, and `Supports` refuses the whole 1.x
// line rather than letting a peer discover the gap one field at a time.
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
//
// 2.2.0 added `Capability.InputSchema` and `Capability.OutputSchema`, which
// state a capability's declared shape as JSON Schema. Additive: an adapter
// built against 2.1.0 goes on compiling and simply never calls them.
//
// The generator existed before this, inside the Claude Code adapter, wired to
// the outputs because a model has to be told what form to answer in. Only
// half the reason was ever adapter-specific. The other half is that a caller
// building a request needs the same statement about the inputs, and the one
// place that can honestly make it is the package that also refuses the
// payload: `ValidateInput` and `InputSchema` are one declaration read in two
// directions, and a schema that advertises a shape the validator then rejects
// is worse than none, because the caller did as it was told.
//
// 2.3.0 added the reserved `raw.` namespace and the receipt's funnel record.
// Two changes, one release, because they are two halves of the same seam: a
// tool reached without a funnel needs a name that says so and a receipt that
// says why. Additive in both directions -- `ReservedNamespace` refuses ids no
// catalog could honestly have used, and an adapter built against 2.2.0 goes
// on compiling with a `Funnel` field it never reads.
//
// The namespace half is a refusal rather than a feature, which is the only
// reason it fits in a minor: nothing that was valid became invalid except a
// capability id claiming the one segment now spoken for.
//
// It also gave `Effect` a JSON form. It is a uint8, so a list of them was
// written as base64 -- a receipt read back `"effects":"AAM="`, which is not a
// record of anything. They now write as names and read back from either, so
// a receipt filed before this still loads.
//
// 3.0.0 removed `Repository.SerenaEndpoint`, and like 2.0.0 it is major
// because it takes away rather than adds.
//
// The field existed to name a second Serena that somebody else had already
// started for one repository -- on this machine, a hand-written systemd unit
// on `:9121` beside the one on `:40010`, identical but for a port and a
// `--project`. That is a per-repository instance policy typed out by hand,
// once per repository, in machine configuration Atenea could not see. Declaring
// `instance = "per_repository"` on the managed process makes it a rule
// instead: Atenea starts one Serena per repository, on demand, picks the
// ports, and stops them when it stops. There is nothing left for a
// per-repository URL to say that the declaration does not already say better,
// and leaving the field would leave two ways to answer one question -- the
// second of which points at a process Atenea does not own.
//
// An adapter that never read the field goes on compiling. One that set it was
// pointing the core at a Serena with a second owner, which is the arrangement
// this removes.
//
// 3.1.0 added the agent contract proper: `Assignment`, the card one agent
// carries for one execution, and `Report`, the three things it hands back.
// With them came `AgentSpecialized`, `VerdictIncomplete`, `Task`, `Limits`,
// `Reason` and `AgentTypeSpec`. Additive: every existing type is untouched,
// and an adapter built against 3.0.0 goes on compiling because none of this
// is on the Runner seam -- it is the seam between a parent agent and its
// child, which had no type before.
//
// The two enum values are the ones worth reading twice. `specialized` is a
// value on the existing kind rather than a second card, so there is still
// exactly one agent contract. `incomplete` is a verdict a core can now reach
// that it could not before, and it is deliberately not a shade of `failed`:
// failed says discard this, incomplete says keep it and continue, and an
// adapter built against 3.0.0 never sends either.
//
// 3.2.0 is one entry for four additions, and it is one entry because none of
// them was ever announced.
//
// They landed while this constant stayed at 3.1.0, which means the number
// 3.1.0 as published covers two different shapes of this package: the one the
// entry above describes, and the one an adapter compiling against `main`
// actually gets. Splitting them into 3.2.0 through 3.5.0 now would invent a
// history no adapter ever saw. 3.2.0 is the first number that means what it
// says, and what it covers is:
//
//   - `Health.ObservedAt`, back, together with `Health.Stale`. What 2.0.0
//     removed it for was a job the Fault and Baseline windows already did --
//     and they do, for health a core computes from its own measurements. They
//     do not cover health a core was TOLD: a handshake result persisted across
//     a restart has no window to sit inside, and a reading with no date is one
//     nothing can age out. The field is the date; Stale is the question.
//   - `RedactRaw` and the bounded raw string. A generic-bin failure carries
//     the provider's own text (1.5.0), and that text reaches disk. Redaction
//     at the boundary rather than at each writer is the only arrangement where
//     a new writer cannot forget.
//   - `CostObserver`, `WithCostObserver` and `CostUpdate`. The optional seam an
//     adapter uses to report spend mid-turn and be told to stop. Optional in
//     the literal sense: it travels on the context, so an adapter that does
//     not look for it is unaffected, which is what keeps this additive on a
//     seam that is otherwise not.
//   - `Assignment.CommissionUSD` and `Assignment.Route`. The first tells a
//     step what the whole run was granted, which is a different fact from its
//     own share and the one an agent drawing a graph has to divide; without it
//     the shipped planner divided its own share under the name of the
//     commission's. The second records the model, capability and provider a
//     decision resolved to, so a resume cannot silently fall back to whatever
//     the machine's defaults say today.
//
// `Limits.Validate` also stopped requiring a positive `max_tokens`, which is a
// relaxation and therefore additive: every file that validated before still
// validates, and zero now reads as "no bound declared" consistently with
// `Limits.Fits`, which had always read it that way.
// 3.3.0 added `Outcome.SpentUSDKnown`, which says whether `Outcome.SpentUSD`
// is a measurement.
//
// Without it a zero there was two facts wearing one face: a provider that
// charges nothing, and a provider that said nothing. This package already kept
// them apart twice -- `Charge.USD` with a pointer, `CostUpdate.Known` with a
// bool -- and `Outcome`, the one on the Runner seam, did neither. The receipt
// on disk inherited the collapse: `spent_usd,omitempty` writes nothing for
// both.
//
// A bool rather than a pointer, matching CostUpdate rather than Charge, and
// the seam is the reason. Outcome travels on the Runner interface, which
// cannot be extended without breaking every implementer, so the shape that
// costs least is the one that leaves the existing field alone. An adapter
// built against 3.2.0 goes on compiling and leaves it false, which reads as
// "nobody said" -- the honest answer for an adapter that never had a way to
// say otherwise.
//
// The core enforces it: a charge reported without it is refused rather than
// spent, because a figure with no measurement behind it is a number and not a
// price.
var Current = Version{Major: 3, Minor: 3, Patch: 0}

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
