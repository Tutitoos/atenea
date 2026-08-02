package contract

import (
	"maps"
	"slices"
	"strings"
)

// Permission is the slice of the user's commission that a step is allowed to
// act on.
//
// It travels ATTACHED to every step. The child never judges for itself whether
// its piece is still inside what the user asked for: the parent stamps the
// permission when it splits the work, and the child simply obeys the stamp.
// Letting each child decide would mean every agent interpreting the boundary
// its own way, and one of them eventually reading it generously.
//
// # Money belongs here, and is not here yet
//
// A spending ceiling is a permission, not a cost: it is the user saying how
// much a piece of work may draw, in the same breath as saying which effects it
// may cause. Cost is what something turned out to be; a grant is what it was
// allowed to be, decided before anything ran.
//
// Today the only ceiling is `budget_usd` on the Claude Code adapter, which
// means each paid adapter would grow its own, none of them would add up, and a
// commission could spend the same ceiling once per provider. Moving it here --
// a grant attached to the commission, spent down as steps close, and refused
// when it runs out -- is its own brick. It is not built.
//
// Until it is, a ceiling reached is reported as FailurePermissionDenied and
// what was actually charged is reported on the receipt, so the gap is visible
// rather than silent.
type Permission struct {
	// Task is the commission the permission came from, kept verbatim so an
	// audit can see what was actually authorized.
	Task string
	// Effects the commission already covers. An effect outside this list is not
	// forbidden forever; it is the point at which Atenea has to stop and ask.
	Effects []Effect
}

// Allows reports whether the commission already covers this effect. When it
// does not, the action is not refused outright: it is the moment to ask.
func (p Permission) Allows(effect Effect) bool { return slices.Contains(p.Effects, effect) }

// Validate checks the stamp.
func (p Permission) Validate() error {
	if strings.TrimSpace(p.Task) == "" {
		return Fail(FailureInvalidInput, "permission: task is required")
	}
	for _, effect := range p.Effects {
		if _, ok := effectNames[effect]; !ok {
			return Fail(FailureInvalidInput, "permission for %q: unknown effect", p.Task)
		}
	}
	return nil
}

// Clone returns a deep copy.
func (p Permission) Clone() Permission {
	p.Effects = slices.Clone(p.Effects)
	return p
}

// Step is one node of the plan: a single capability asked of a single
// repository, with the permission it inherited.
//
// A step names a capability, never a tool. Which implementation answers it is
// settled later by the funnel, per repository, at the moment of dispatch.
type Step struct {
	ID string
	// Capability is what the step wants done, by intent.
	Capability string
	// Repository is the unit of work it runs against.
	Repository string
	// Payload is the capability input, checked against the declared schema
	// before anything runs.
	Payload map[string]any
	// Needs lists the step ids that must finish first. Empty means the step can
	// start in the first wave.
	Needs      []string
	Permission Permission
}

// Validate checks the node itself, not its place in the graph.
func (s Step) Validate() error {
	if !slugID.MatchString(s.ID) {
		return Fail(FailureInvalidInput, "step id %q must be lowercase", s.ID)
	}
	if !capabilityID.MatchString(s.Capability) {
		return Fail(FailureInvalidInput,
			"step %s: capability %q must be dotted lowercase", s.ID, s.Capability)
	}
	if !slugID.MatchString(s.Repository) {
		return Fail(FailureInvalidInput,
			"step %s: repository %q must be lowercase", s.ID, s.Repository)
	}
	if err := s.Permission.Validate(); err != nil {
		return Fail(FailureInvalidInput, "step %s: %v", s.ID, err)
	}
	return nil
}

// Clone returns a deep copy.
func (s Step) Clone() Step {
	s.Payload = maps.Clone(s.Payload)
	s.Needs = slices.Clone(s.Needs)
	s.Permission = s.Permission.Clone()
	return s
}

// Plan is the map of steps for one commission: a directed acyclic graph.
//
// Directed because the work only moves forwards, acyclic because a plan that
// can return to a finished step is a plan that can loop for ever. Chaining is
// what Atenea does; the graph is the drawing of how it chains.
type Plan struct {
	// Task is the commission in the user's own words.
	Task  string
	Steps []Step
}

// Step looks one up by id.
func (p Plan) Step(id string) (Step, bool) {
	for _, step := range p.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return Step{}, false
}

// Append returns a copy of the plan with more steps, validated as a whole.
//
// This is what hybrid planning needs: the first plan is light -- explore, and
// little else -- and the rest of the graph is drawn once exploring has said
// what the project actually looks like.
func (p Plan) Append(steps ...Step) (Plan, error) {
	extended := p.Clone()
	for _, step := range steps {
		extended.Steps = append(extended.Steps, step.Clone())
	}
	if err := extended.Validate(); err != nil {
		return Plan{}, err
	}
	return extended, nil
}

// Validate checks every node and the shape of the graph.
func (p Plan) Validate() error {
	_, err := p.Layers()
	return err
}

// Layers returns every step grouped into waves: everything in one wave can run
// at the same time, and a wave only starts once the one before it is done.
func (p Plan) Layers() ([][]Step, error) { return p.LayersAfter(nil) }

// LayersAfter returns the waves still to run, treating the named steps as
// already finished.
//
// This is what lets one plan be dispatched in phases. The work steps wait on
// the look that came before them, and once the look has happened that
// dependency is satisfied by history rather than by anything left in the
// queue. Without it, dispatching the second half of a graph would look like a
// graph with dangling edges.
//
// The grouping is also the cycle check. A graph with a loop never drains, and
// the steps left over at the end are exactly the ones caught in it.
func (p Plan) LayersAfter(finished []string) ([][]Step, error) {
	byID, err := p.index()
	if err != nil {
		return nil, err
	}
	done := make(map[string]struct{}, len(finished))
	for _, id := range finished {
		if _, ok := byID[id]; !ok {
			return nil, Fail(FailureInvalidInput,
				"plan %q: %s was reported finished but is not a step of this plan", p.Task, id)
		}
		done[id] = struct{}{}
	}

	var layers [][]Step
	for len(done) < len(p.Steps) {
		var wave []Step
		for _, step := range p.Steps {
			if _, closed := done[step.ID]; closed {
				continue
			}
			if ready(step, done) {
				wave = append(wave, step)
			}
		}
		if len(wave) == 0 {
			return nil, Fail(FailureInvalidInput,
				"plan %q: steps form a cycle: %s", p.Task, strings.Join(remaining(p.Steps, done), ", "))
		}
		// Sorting keeps the same plan producing the same waves every time,
		// which is what makes a dispatch trace comparable across runs.
		slices.SortFunc(wave, func(a, b Step) int { return strings.Compare(a.ID, b.ID) })
		for _, step := range wave {
			done[step.ID] = struct{}{}
		}
		layers = append(layers, wave)
	}
	return layers, nil
}

// index validates the graph and returns its steps by id.
func (p Plan) index() (map[string]Step, error) {
	if strings.TrimSpace(p.Task) == "" {
		return nil, Fail(FailureInvalidInput, "plan: task is required")
	}
	if len(p.Steps) == 0 {
		return nil, Fail(FailureInvalidInput, "plan %q: no steps", p.Task)
	}
	byID := make(map[string]Step, len(p.Steps))
	for _, step := range p.Steps {
		if err := step.Validate(); err != nil {
			return nil, err
		}
		if _, dup := byID[step.ID]; dup {
			return nil, Fail(FailureInvalidInput, "plan %q: two steps called %s", p.Task, step.ID)
		}
		byID[step.ID] = step
	}
	for _, step := range p.Steps {
		seen := make(map[string]struct{}, len(step.Needs))
		for _, need := range step.Needs {
			if need == step.ID {
				return nil, Fail(FailureInvalidInput, "plan %q: step %s needs itself", p.Task, step.ID)
			}
			if _, ok := byID[need]; !ok {
				return nil, Fail(FailureInvalidInput,
					"plan %q: step %s needs unknown step %s", p.Task, step.ID, need)
			}
			if _, dup := seen[need]; dup {
				return nil, Fail(FailureInvalidInput,
					"plan %q: step %s needs %s twice", p.Task, step.ID, need)
			}
			seen[need] = struct{}{}
		}
	}
	return byID, nil
}

// ready reports whether every step this one waits on has already finished.
func ready(step Step, done map[string]struct{}) bool {
	for _, need := range step.Needs {
		if _, finished := done[need]; !finished {
			return false
		}
	}
	return true
}

func remaining(steps []Step, done map[string]struct{}) []string {
	out := make([]string, 0, len(steps)-len(done))
	for _, step := range steps {
		if _, finished := done[step.ID]; !finished {
			out = append(out, step.ID)
		}
	}
	slices.Sort(out)
	return out
}

// Clone returns a deep copy.
func (p Plan) Clone() Plan {
	steps := make([]Step, 0, len(p.Steps))
	for _, step := range p.Steps {
		steps = append(steps, step.Clone())
	}
	p.Steps = steps
	return p
}
