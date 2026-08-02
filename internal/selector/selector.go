// Package selector picks which implementation answers a capability for a given
// repository.
//
// It is a funnel. Constraints say who CAN work here, health says who is
// AVAILABLE right now, and the last stage picks among whoever is left. That
// last stage ranks on cost, and cost is hybrid: an implementation is ranked on
// its own measurements once the base holds enough of them, and on the estimate
// declared in the settings file until then.
//
// Cost only ever orders. Nobody leaves the funnel for being expensive, and a
// standing user rule outranks the whole ranking.
//
// While an implementation still owes the base its first measurements the
// funnel hands it the turn, so the numbers it is missing get made rather than
// waited for. The trace says which of the three settled each choice.
package selector

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Stage names, in funnel order.
const (
	StageConstraints = "constraints"
	StageReach       = "reach"
	StageHealth      = "health"
	StageChoice      = "choice"
)

// Rule is a standing user preference. A rule outranks any automatic ranking:
// the user's word comes before Atenea's opinion.
type Rule struct {
	Capability string
	// Repository narrows the rule to one unit of work. Empty means every
	// repository.
	Repository string
	// Prefer is the implementation id to use when it survives the funnel.
	Prefer string
}

func (r Rule) specificity() int {
	if r.Repository != "" {
		return 2
	}
	return 1
}

// Config is the selector's declarative half.
type Config struct {
	Rules []Rule
}

// Selector is stateless and safe for concurrent use.
type Selector struct {
	rules []Rule
}

// New validates the rules and returns a selector.
func New(cfg Config) (*Selector, error) {
	seen := make(map[string]struct{}, len(cfg.Rules))
	rules := make([]Rule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if strings.TrimSpace(rule.Capability) == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"selector rule: capability is required")
		}
		if strings.TrimSpace(rule.Prefer) == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"selector rule for %s: prefer is required", rule.Capability)
		}
		key := rule.Capability + "\x00" + rule.Repository
		if _, dup := seen[key]; dup {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"selector: two rules for capability %s and repository %q",
				rule.Capability, rule.Repository)
		}
		seen[key] = struct{}{}
		rules = append(rules, rule)
	}
	return &Selector{rules: rules}, nil
}

// Rules returns the configured rules.
func (s *Selector) Rules() []Rule { return slices.Clone(s.rules) }

// Request is one selection.
type Request struct {
	Capability string
	Repository contract.Repository
	Candidates []contract.Implementation
	// Reachable lists the implementations the attached far side can actually
	// execute, and it is required: a funnel that did not know would happily
	// choose a provider nothing can invoke, and the step would die on dispatch
	// instead of falling back to whoever could have answered.
	//
	// It is its own stage rather than a health verdict because the two are
	// different kinds of fact. Health is what a probe found -- running a step
	// is what updates it -- while reach is a wiring decision that was made
	// before anything ran. Filing one under the other would mean a settings
	// change had to wait for a probe to be believed.
	Reachable []string
	// Measuring says whether a measurement base is feeding this funnel. It is
	// what makes break-in mode meaningful: a turn handed to an unmeasured
	// implementation only pays for itself if the call it earns is written
	// down somewhere. With the base switched off nothing can ever be earned,
	// so the funnel ranks on the declared estimates and says so rather than
	// rotating forever between providers to fill a ledger nobody keeps.
	Measuring bool
}

// Drop records one implementation leaving the funnel, and why.
type Drop struct {
	Implementation string
	Reason         string
}

// Stage is one step of the funnel, kept so every decision can be explained
// afterwards without re-running it.
type Stage struct {
	Name    string
	In      []string
	Out     []string
	Dropped []Drop
}

// Decision is the outcome plus the trace that justifies it.
type Decision struct {
	Capability string
	Repository string
	Chosen     contract.Implementation
	// Reason names the stage that settled the choice.
	Reason string
	// Notices carry things the user should know but that did not stop the
	// selection, such as a preferred implementation being skipped.
	Notices []string
	Stages  []Stage
}

// Select runs the funnel.
func (s *Selector) Select(req Request) (Decision, error) {
	decision := Decision{
		Capability: req.Capability,
		Repository: req.Repository.ID,
	}
	if len(req.Candidates) == 0 {
		return decision, contract.Fail(contract.FailureNotFound,
			"capability %s has no registered implementations", req.Capability)
	}

	fitting, constraintStage := s.filterConstraints(req, req.Candidates)
	decision.Stages = append(decision.Stages, constraintStage)
	if len(fitting) == 0 {
		return decision, contract.Fail(contract.FailureNotFound,
			"no implementation of %s fits repository %s", req.Capability, req.Repository.ID)
	}

	within, reachStage := filterReach(req, fitting)
	decision.Stages = append(decision.Stages, reachStage)
	if len(within) == 0 {
		return decision, contract.Fail(contract.FailureUnavailable,
			"no attached runner serves any implementation of %s", req.Capability)
	}

	usable, healthStage := filterHealth(within)
	decision.Stages = append(decision.Stages, healthStage)
	if len(usable) == 0 {
		return decision, contract.Fail(contract.FailureUnavailable,
			"every implementation of %s is down for repository %s", req.Capability, req.Repository.ID)
	}

	chosen, reason, notices := s.choose(req, usable)
	decision.Chosen = chosen
	decision.Reason = reason
	decision.Notices = notices
	decision.Stages = append(decision.Stages, Stage{
		Name: StageChoice,
		In:   ids(usable),
		Out:  []string{chosen.ID},
	})
	return decision, nil
}

// filterReach drops what no attached far side can execute.
//
// It runs after constraints so the trace carries the most useful reason it
// can: a provider that needs an index it does not have should be told so, not
// merely reported as unwired. Only what could otherwise have worked here is
// left to fail on wiring.
func filterReach(req Request, candidates []contract.Implementation) ([]contract.Implementation, Stage) {
	stage := Stage{Name: StageReach, In: ids(candidates)}
	kept := make([]contract.Implementation, 0, len(candidates))
	for _, impl := range candidates {
		if !slices.Contains(req.Reachable, impl.ID) {
			stage.Dropped = append(stage.Dropped, Drop{
				Implementation: impl.ID,
				Reason:         "no attached runner serves it",
			})
			continue
		}
		kept = append(kept, impl)
	}
	stage.Out = ids(kept)
	return kept, stage
}

func (s *Selector) filterConstraints(req Request, candidates []contract.Implementation) ([]contract.Implementation, Stage) {
	stage := Stage{Name: StageConstraints, In: ids(candidates)}
	kept := make([]contract.Implementation, 0, len(candidates))
	for _, impl := range candidates {
		if reason, ok := fits(impl, req.Repository); !ok {
			stage.Dropped = append(stage.Dropped, Drop{Implementation: impl.ID, Reason: reason})
			continue
		}
		kept = append(kept, impl)
	}
	stage.Out = ids(kept)
	return kept, stage
}

// fits reports whether an implementation can work on this repository at all.
func fits(impl contract.Implementation, repo contract.Repository) (string, bool) {
	c := impl.Constraints
	if !repo.SpeaksAny(c.Languages) {
		return fmt.Sprintf("speaks %s, repository has %s",
			list(c.Languages), list(repo.Languages)), false
	}
	if c.RequiresIndex && !repo.IndexedBy(impl.Provider) {
		return fmt.Sprintf("needs an index from provider %s, repository has none", impl.Provider), false
	}
	// An unclassified repository never disqualifies anyone: an unknown size is
	// not a proven mismatch.
	if repo.Scale != contract.ScaleUnspecified {
		if c.MinScale != contract.ScaleUnspecified && repo.Scale < c.MinScale {
			return fmt.Sprintf("needs a %s repository or bigger, this one is %s", c.MinScale, repo.Scale), false
		}
		if c.MaxScale != contract.ScaleUnspecified && repo.Scale > c.MaxScale {
			return fmt.Sprintf("needs a %s repository or smaller, this one is %s", c.MaxScale, repo.Scale), false
		}
	}
	return "", true
}

func filterHealth(candidates []contract.Implementation) ([]contract.Implementation, Stage) {
	stage := Stage{Name: StageHealth, In: ids(candidates)}
	kept := make([]contract.Implementation, 0, len(candidates))
	for _, impl := range candidates {
		if !impl.Health.Usable() {
			reason := impl.Health.Reason
			if reason == "" {
				reason = "reported down"
			}
			stage.Dropped = append(stage.Dropped, Drop{Implementation: impl.ID, Reason: reason})
			continue
		}
		kept = append(kept, impl)
	}
	stage.Out = ids(kept)
	return kept, stage
}

// BreakInSamples is how many real measurements an implementation needs before
// its own numbers are believed over its declared estimate.
//
// The design calls the state before that "modo de rodaje": on day one there is
// no baseline, so the estimate is all there is. Two rather than one because a
// single call can be a cold cache.
const BreakInSamples = 2

// inBreakIn reports whether an implementation still owes the base measurements
// before its own numbers can be believed.
func inBreakIn(impl contract.Implementation) bool {
	return !impl.Cost.HasMeasurements(BreakInSamples)
}

// choose settles the survivors: a user rule outright, then the healthiest one,
// then the break-in turn while anybody still owes the base its samples, then
// the cheaper of two equals, and the implementation id last so the same
// catalog always produces the same answer.
func (s *Selector) choose(req Request, survivors []contract.Implementation) (contract.Implementation, string, []string) {
	var notices []string
	if rule, ok := s.ruleFor(req.Capability, req.Repository.ID); ok {
		if idx := slices.IndexFunc(survivors, func(i contract.Implementation) bool {
			return i.ID == rule.Prefer
		}); idx >= 0 {
			return survivors[idx], fmt.Sprintf("user rule prefers %s", rule.Prefer), nil
		}
		// The manual choice is scaffolding, not dogma: Atenea moves on rather
		// than stopping. But changing it silently would betray what the user
		// asked for, so it is announced.
		notices = append(notices, fmt.Sprintf(
			"user rule prefers %s, which did not survive the funnel; falling back", rule.Prefer))
	}
	ranked := slices.Clone(survivors)
	slices.SortFunc(ranked, rankWith(req.Measuring))
	return ranked[0], reasonFor(ranked, req.Measuring), notices
}

// reasonFor names what actually settled the choice, so the trace never claims
// more certainty than it has. Three words carry that: "measured" means the
// numbers came from the base, "estimated" means no measurement exists yet and
// the figure is the one somebody typed into the settings file, and "break-in
// turn" means cost did not decide this at all.
func reasonFor(ranked []contract.Implementation, measuring bool) string {
	if len(ranked) < 2 {
		return "the only surviving implementation"
	}
	first, second := ranked[0], ranked[1]
	if first.Health.State != second.Health.State || first.Health.Score != second.Health.Score {
		return "healthiest surviving implementation"
	}
	if measuring && owesMore(first, second) {
		// Said plainly and without the word "cheapest" anywhere near it: this
		// dispatch is buying a measurement, not spending the cheapest option.
		// A reader who mistook it for a cost decision would go looking for a
		// cost bug that is not there.
		return fmt.Sprintf("break-in turn: %s has %d of %d measurements, not a cost decision",
			first.ID, first.Cost.Samples, BreakInSamples)
	}
	if cheaper(first, second) {
		if first.Cost.HasMeasurements(BreakInSamples) && second.Cost.HasMeasurements(BreakInSamples) {
			return "cheapest of the healthy ones (measured)"
		}
		return "cheapest of the healthy ones (estimated)"
	}
	// Neither is cheaper on both axes, so Atenea has no basis to prefer one.
	// Saying so is the point: this is where a user rule belongs.
	return "no cheaper option among equals, settled by id"
}

// owesMore reports whether a should go first because it owes the base more
// measurements than b does. Only meaningful while somebody is still in
// break-in: two implementations that have both earned their numbers owe
// nothing, however far apart their sample counts have since drifted.
func owesMore(a, b contract.Implementation) bool {
	if !inBreakIn(a) && !inBreakIn(b) {
		return false
	}
	return a.Cost.Samples < b.Cost.Samples
}

// rankWith orders the survivors: health, then the break-in turn, then cost,
// then id. It is a ranking and never a filter -- nobody leaves the funnel for
// being expensive.
//
// That distinction is deliberate and load-bearing, and the break-in turn is
// what finally makes it pay. A cost FILTER would starve the loser of the very
// measurements that could correct its estimate: the expensive one is never
// run, so it is never measured, so the hybrid cost stays frozen on day-one
// guesswork forever. Ranking alone does not fix that on its own -- the
// estimated-cheapest still wins every time and the other never earns a number
// either. So while anybody still owes the base its samples, fewest samples
// goes first, and the rotation ends by itself the moment everyone has enough.
//
// It converges because each turn adds a sample to whoever had the fewest: two
// providers at zero alternate until both hold BreakInSamples, and from then on
// cost decides with real numbers on both sides and never rotates again.
func rankWith(measuring bool) func(a, b contract.Implementation) int {
	return func(a, b contract.Implementation) int {
		if d := a.Health.State.Rank() - b.Health.State.Rank(); d != 0 {
			return d
		}
		switch {
		case a.Health.Score > b.Health.Score:
			return -1
		case a.Health.Score < b.Health.Score:
			return 1
		}
		if measuring {
			switch {
			case owesMore(a, b):
				return -1
			case owesMore(b, a):
				return 1
			}
		}
		switch {
		case cheaper(a, b):
			return -1
		case cheaper(b, a):
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	}
}

// cheaper reports whether a costs less than b on BOTH axes, using each one's
// best available figure: its measurements once it has enough, its estimate
// until then.
//
// Requiring both axes is what keeps this free of invented weights. Nobody has
// decided what a second of wall clock is worth in tokens, so a genuine
// trade-off -- faster but chattier -- is reported as a tie rather than settled
// by a number this package made up.
func cheaper(a, b contract.Implementation) bool {
	x := a.Cost.Effective(BreakInSamples)
	y := b.Cost.Effective(BreakInSamples)
	if x.Tokens > y.Tokens || x.Duration > y.Duration {
		return false
	}
	return x.Tokens < y.Tokens || x.Duration < y.Duration
}

// ruleFor returns the most specific rule matching the request.
func (s *Selector) ruleFor(capabilityID, repositoryID string) (Rule, bool) {
	var best Rule
	found := false
	for _, rule := range s.rules {
		if rule.Capability != capabilityID {
			continue
		}
		if rule.Repository != "" && rule.Repository != repositoryID {
			continue
		}
		if !found || rule.specificity() > best.specificity() {
			best, found = rule, true
		}
	}
	return best, found
}

func ids(impls []contract.Implementation) []string {
	out := make([]string, len(impls))
	for i, impl := range impls {
		out[i] = impl.ID
	}
	return out
}

func list(values []string) string {
	if len(values) == 0 {
		return "nothing"
	}
	return strings.Join(values, ", ")
}
