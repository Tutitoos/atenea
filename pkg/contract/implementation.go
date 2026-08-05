package contract

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Scale is the size bracket of a repository. It is coarse on purpose: the point
// is to keep a tool that only pays off on a big codebase away from a tiny one,
// not to measure anything precisely.
type Scale uint8

// The size brackets a repository can fall into.
const (
	// ScaleUnspecified means nobody has classified the repository yet. It never
	// disqualifies an implementation: an unknown size is not a proven mismatch.
	ScaleUnspecified Scale = iota
	ScaleSmall
	ScaleMedium
	ScaleLarge
)

var (
	scaleNames = map[Scale]string{
		ScaleUnspecified: "",
		ScaleSmall:       "small",
		ScaleMedium:      "medium",
		ScaleLarge:       "large",
	}
	scaleByName = map[string]Scale{
		"":       ScaleUnspecified,
		"small":  ScaleSmall,
		"medium": ScaleMedium,
		"large":  ScaleLarge,
	}
)

func (s Scale) String() string {
	if name, ok := scaleNames[s]; ok {
		return name
	}
	return fmt.Sprintf("scale(%d)", uint8(s))
}

// ParseScale reads a scale name. The empty string is unspecified.
func ParseScale(s string) (Scale, error) {
	if v, ok := scaleByName[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("unknown scale %q: want small, medium or large", s)
}

// VCS says whether a repository sits under version control, as far as anyone
// has declared. Coarse on purpose, the same reason Scale is: the point is to
// catch a provider that would fail on the filesystem before it is asked, not
// to name which system.
type VCS uint8

// The states a repository's version control can be found in.
const (
	// VCSUnspecified means nobody has said either way. It never disqualifies
	// an implementation: an unmeasured fact is not a proven mismatch, the
	// same reading Scale gives an unclassified repository.
	VCSUnspecified VCS = iota
	VCSPresent
	VCSAbsent
)

var (
	vcsNames = map[VCS]string{
		VCSUnspecified: "",
		VCSPresent:     "present",
		VCSAbsent:      "absent",
	}
	vcsByName = map[string]VCS{
		"":        VCSUnspecified,
		"present": VCSPresent,
		"absent":  VCSAbsent,
	}
)

func (v VCS) String() string {
	if name, ok := vcsNames[v]; ok {
		return name
	}
	return fmt.Sprintf("vcs(%d)", uint8(v))
}

// ParseVCS reads a vcs state name. The empty string is unspecified.
func ParseVCS(s string) (VCS, error) {
	if v, ok := vcsByName[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("unknown vcs %q: want present or absent", s)
}

// Constraints is block 2 of an Implementation: what has to be true before this
// provider can work at all.
//
// Everything here is measured per repository, never per workspace. A monorepo
// is many repositories, and one global index over all of them is the thing the
// design refused: the selector asks "is there an index for THIS repository?".
type Constraints struct {
	// Languages the provider understands. Empty means language-agnostic.
	Languages []string
	// RequiresIndex means the provider is useless until the repository has been
	// indexed by its provider.
	RequiresIndex bool
	// RequiresVCS means the provider needs the repository to sit under version
	// control -- it measures against a point in history, and there is none
	// without one.
	RequiresVCS bool
	// MinScale and MaxScale bracket the repository sizes this provider is worth
	// using on. ScaleUnspecified on either end means unbounded.
	MinScale Scale
	MaxScale Scale
}

// Sample is one cost observation: what a call spent.
//
// Three legs, because two of them can hide the third: a tool that is quick and
// quiet while paging a machine into swap is not cheap, it is only cheap on the
// axes anyone bothered to look at.
type Sample struct {
	Duration time.Duration
	Tokens   int
	// PeakRSS is the high-water mark of resident memory, in bytes, of the
	// process the far side ran in. Zero means nobody measured it rather than
	// zero bytes: an adapter talking to a server over HTTP has no process of
	// its own to weigh, and the design would rather record that gap than
	// estimate over it.
	PeakRSS int64
}

// Cost is block 3 of an Implementation.
//
// Cost is not a fixed property: the same provider is cheap with a warm index
// and expensive without one. So it is hybrid. An estimate gets things moving on
// day one, and the moment real measurements exist they take over.
type Cost struct {
	// Estimated is the declared guess, used until measurements exist.
	Estimated Sample
	// Measured is the rolling average of real calls.
	Measured Sample
	// Samples counts the measurements behind Measured. Only calls that WORKED
	// leave a sample: a failure is an attempt with no price.
	Samples int
	// Attempts counts every dispatch, priced or not, and exists so the funnel
	// can tell a provider on its first outing from one that has been given
	// chances and produced nothing.
	//
	// Without it the two are indistinguishable -- both sit at zero samples --
	// and the break-in rotation, which promotes whoever owes the base numbers,
	// rewards a provider for failing. Measured: seven straight failures at
	// roughly a minute and thirty cents each, each one leaving the count at
	// zero, each one therefore winning the next dispatch.
	Attempts int
	// ToolVersion is the provider version those measurements belong to. When the
	// tool is upgraded the old baseline is set aside rather than averaged in:
	// otherwise the slow numbers of the old version drag the average down and
	// the selector takes weeks to notice the improvement.
	ToolVersion string
}

// HasMeasurements reports whether at least atLeast real samples back Measured.
func (c Cost) HasMeasurements(atLeast int) bool {
	if atLeast < 1 {
		atLeast = 1
	}
	return c.Samples >= atLeast
}

// Barren reports whether this implementation has been given at least atLeast
// dispatches and has no measurement to show for any of them.
//
// It is the difference between a provider on its first outing and one whose
// turn has been paid for and delivered nothing. Both read as zero samples,
// which is why the funnel needs this to tell them apart: the rotation that
// promotes unmeasured providers is buying measurements, and a purchase that
// has failed atLeast times over is not going to complete on the next one.
//
// Partway through counts as still going: an implementation holding some of the
// samples it owes is being measured successfully and has not gone barren.
func (c Cost) Barren(atLeast int) bool {
	if atLeast < 1 {
		atLeast = 1
	}
	return c.Samples == 0 && c.Attempts >= atLeast
}

// Effective returns the cost figure to reason with: the measurement when there
// is enough of it, the estimate otherwise.
func (c Cost) Effective(minSamples int) Sample {
	if c.HasMeasurements(minSamples) {
		return c.Measured
	}
	return c.Estimated
}

// HealthState is block 4 of an Implementation: whether the provider can be used
// right now.
type HealthState uint8

// The states a provider can be found in.
const (
	// HealthUnknown means nobody has looked yet. It is not the same as down: an
	// unprobed provider is still a candidate, just a less trustworthy one.
	HealthUnknown HealthState = iota
	HealthAlive
	// HealthDegraded means usable but not at full strength: slow, or working
	// without its index. It survives the funnel and ranks below alive.
	HealthDegraded
	HealthDown
)

var (
	healthNames = map[HealthState]string{
		HealthUnknown:  "unknown",
		HealthAlive:    "alive",
		HealthDegraded: "degraded",
		HealthDown:     "down",
	}
	healthByName = map[string]HealthState{
		"unknown":  HealthUnknown,
		"alive":    HealthAlive,
		"degraded": HealthDegraded,
		"down":     HealthDown,
	}
)

func (h HealthState) String() string {
	if name, ok := healthNames[h]; ok {
		return name
	}
	return "unknown"
}

// ParseHealthState reads a health state name. The empty string is unknown.
func ParseHealthState(s string) (HealthState, error) {
	if s == "" {
		return HealthUnknown, nil
	}
	if v, ok := healthByName[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("unknown health state %q: want alive, degraded, down or unknown", s)
}

// Rank orders health states for the funnel: lower is better.
func (h HealthState) Rank() int {
	switch h {
	case HealthAlive:
		return 0
	case HealthDegraded:
		return 1
	case HealthUnknown:
		return 2
	default:
		return 3
	}
}

// Health carries the state plus a comparable number.
//
// A plain on/off switch is enough when there is only one option. With several
// valid providers at once the nuance is what lets the selector compare instead
// of merely filtering.
type Health struct {
	State HealthState
	// Score is 0..1 within the state. It breaks ties between two providers that
	// are both alive or both degraded.
	Score  float64
	Reason string
	// Raw is the untranslated provider text behind Reason, when the record
	// that produced this Health kept one. It travels with the state rather
	// than living only on the attempt that caused it, because a provider the
	// funnel is refusing right now is exactly the one whose evidence a human
	// most needs without having to go dispatch a fresh failing call to see it.
	Raw        string
	ObservedAt time.Time
}

// Usable reports whether the funnel keeps this provider.
func (h Health) Usable() bool { return h.State != HealthDown }

// Implementation is the "who and how" behind a capability: ripgrep, Serena, a
// language server.
//
// Four blocks: the capability it answers, its constraints, its cost and its
// health. Those are exactly the facts Atenea needs to pick one when several
// would do.
type Implementation struct {
	ID string
	// Provider is the tool behind this implementation. Several implementations
	// may share one provider, and an index belongs to the provider rather than
	// to any single implementation of it.
	Provider string
	// Capability is block 1: which capability this answers.
	Capability  string
	Constraints Constraints
	Cost        Cost
	Health      Health
}

var slugID = regexp.MustCompile(`^[a-z][a-z0-9]*([._-][a-z0-9]+)*$`)

// Validate checks the implementation definition itself.
func (i Implementation) Validate() error {
	if !slugID.MatchString(i.ID) {
		return Fail(FailureInvalidInput,
			"implementation id %q must be lowercase, e.g. ripgrep or serena.search", i.ID)
	}
	if !slugID.MatchString(i.Provider) {
		return Fail(FailureInvalidInput,
			"implementation %s: provider %q must be lowercase", i.ID, i.Provider)
	}
	if !capabilityID.MatchString(i.Capability) {
		return Fail(FailureInvalidInput,
			"implementation %s: capability %q must be dotted lowercase", i.ID, i.Capability)
	}
	for _, lang := range i.Constraints.Languages {
		if strings.TrimSpace(lang) == "" || lang != strings.ToLower(lang) {
			return Fail(FailureInvalidInput,
				"implementation %s: language %q must be non-empty lowercase", i.ID, lang)
		}
	}
	lo, hi := i.Constraints.MinScale, i.Constraints.MaxScale
	if lo != ScaleUnspecified && hi != ScaleUnspecified && lo > hi {
		return Fail(FailureInvalidInput,
			"implementation %s: min_scale %s is above max_scale %s", i.ID, lo, hi)
	}
	if i.Cost.Samples < 0 {
		return Fail(FailureInvalidInput, "implementation %s: negative sample count", i.ID)
	}
	if i.Health.Score < 0 || i.Health.Score > 1 {
		return Fail(FailureInvalidInput,
			"implementation %s: health score %v is outside 0..1", i.ID, i.Health.Score)
	}
	return nil
}

// Clone returns a deep copy.
func (i Implementation) Clone() Implementation {
	i.Constraints.Languages = slices.Clone(i.Constraints.Languages)
	return i
}
