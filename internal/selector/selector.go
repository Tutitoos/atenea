// Package selector picks which implementation answers a capability for a given
// repository.
//
// It is a funnel. Constraints say who CAN work here, health says who is
// AVAILABLE right now, and the last stage picks among whoever is left. The
// third filter -- cost -- is not wired yet: on a cold start there are no fresh
// measurements to compare, so ranking by cost would mean ranking by guesswork.
// It joins the funnel once the metrics base is feeding real numbers.
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

	fitting, constraintStage := s.filterConstraints(req)
	decision.Stages = append(decision.Stages, constraintStage)
	if len(fitting) == 0 {
		return decision, contract.Fail(contract.FailureNotFound,
			"no implementation of %s fits repository %s", req.Capability, req.Repository.ID)
	}

	usable, healthStage := filterHealth(fitting)
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

func (s *Selector) filterConstraints(req Request) ([]contract.Implementation, Stage) {
	stage := Stage{Name: StageConstraints, In: ids(req.Candidates)}
	kept := make([]contract.Implementation, 0, len(req.Candidates))
	for _, impl := range req.Candidates {
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

// choose settles the survivors. A user rule wins outright; otherwise the
// healthiest one does, with the implementation id as the final tie-break so the
// same catalog always produces the same answer.
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
	slices.SortFunc(ranked, byHealthThenID)
	return ranked[0], "healthiest surviving implementation", notices
}

func byHealthThenID(a, b contract.Implementation) int {
	if d := a.Health.State.Rank() - b.Health.State.Rank(); d != 0 {
		return d
	}
	switch {
	case a.Health.Score > b.Health.Score:
		return -1
	case a.Health.Score < b.Health.Score:
		return 1
	}
	return strings.Compare(a.ID, b.ID)
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
