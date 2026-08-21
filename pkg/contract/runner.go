package contract

import "context"

// Runner is the seam between deciding and doing.
//
// Atenea decides and delegates; it never executes. Everything on the far side
// of this interface belongs to somebody else: in production a client adapter,
// which is a dumb translator with no logic of its own. The core keeps the
// funnel, picks the implementation, stamps the permission, and hands the job
// across. What comes back is an Outcome or one of the failure bins -- never
// vendor error text.
//
// One interface, several possible far sides: the omp adapter, a Claude Code
// adapter when it exists, or a local stand-in on a machine where no client is
// installed. Swapping one for another changes nothing above this line, which
// is the point.
type Runner interface {
	// ID names the runner, so a status screen can say who is actually behind
	// the catalog rather than implying that something is.
	ID() string
	// Serves reports whether this runner can execute that implementation. A
	// runner that cannot is not a failure: it is a provider that is simply not
	// reachable from here, which is exactly what health and fallback are for.
	Serves(implementationID string) bool
	// Implementations lists every implementation this runner declares itself
	// the far side of. Serves answers the same question one id at a time; this
	// is the whole list, which is what lets the wiring above catch two clients
	// claiming the same work before either of them runs any.
	Implementations() []string
	// Capabilities lists every capability this runner can actually execute.
	//
	// It is not Implementations by another name, and the difference is the
	// whole reason it exists. Implementations is what a settings file told
	// this runner to answer for; Capabilities is what its code can dispatch.
	// Nothing used to compare the two, so a settings file could name an
	// implementation the adapter had no case for and be accepted: the status
	// screen said the adapter served it, the funnel chose it, and only the
	// call itself discovered there was nothing behind it. The wiring above
	// checks one against the other before anything runs.
	Capabilities() []string
	// Run executes one step with the implementation the funnel chose.
	Run(ctx context.Context, req RunRequest) (Outcome, error)
}

// SurfaceReporter is an optional status detail for runners that have more
// than one executable surface, such as a terminal CLI and an app-bundled CLI.
// The string must not contain credentials or environment values.
type SurfaceReporter interface {
	Surface() string
}

// IndexProber is implemented by a runner that can say whether it already
// holds a ready index for a repository, without being asked to build one.
//
// It is optional, not part of Runner itself: not every provider has index
// state to report. A capability answered correctly today does not prove an
// index backed the answer, and only the provider's own process knows which
// it was -- this is the one seam that can ask it directly rather than infer
// it from the outside. A runner that implements it is one detection can
// correct indexed_by against; one that does not is simply left out of the
// sweep, the same as health once was before anything probed it.
type IndexProber interface {
	// ProbeIndex reports whether root already has a ready index this runner
	// can answer from.
	//
	// hint explains a false ready in words a user can act on -- it is
	// empty exactly when ready is true. err is reserved for the probe
	// itself failing to reach a verdict at all (a crashed binary, a
	// canceled context): the caller must not touch indexed_by on a
	// non-nil err, because "could not tell" and "confirmed absent" would
	// otherwise correct the catalog on a guess.
	ProbeIndex(ctx context.Context, root string) (ready bool, hint string, err error)
}

// RunRequest is everything the far side needs and nothing it does not.
//
// It carries the capability rather than just its id because the far side has
// to check the payload against the declared schema, and it carries the
// permission because a step never re-derives what it is allowed to do.
type RunRequest struct {
	Capability     Capability
	Implementation Implementation
	Repository     Repository
	Payload        map[string]any
	Permission     Permission
}

// Validate checks the request before anything runs: the implementation really
// answers this capability, the payload matches the declared inputs, and the
// permission is well formed.
func (r RunRequest) Validate() error {
	if r.Implementation.Capability != r.Capability.ID {
		return Fail(FailureInvalidInput,
			"implementation %s answers %s, not %s",
			r.Implementation.ID, r.Implementation.Capability, r.Capability.ID)
	}
	if err := r.Repository.Validate(); err != nil {
		return err
	}
	if err := r.Permission.Validate(); err != nil {
		return err
	}
	return r.Capability.ValidateInput(r.Payload)
}

// Allowed reports whether the stamped permission already covers every effect
// the capability causes. When it does not, the missing effect is returned: it
// names the point at which Atenea has to stop and ask the user.
func (r RunRequest) Allowed() (Effect, bool) {
	for _, effect := range r.Capability.Effects {
		if !r.Permission.Allows(effect) {
			return effect, false
		}
	}
	return 0, true
}
