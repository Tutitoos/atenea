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
	// Run executes one step with the implementation the funnel chose.
	Run(ctx context.Context, req RunRequest) (Outcome, error)
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
