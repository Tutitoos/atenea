// Package workflow runs a graph of agent steps.
//
// A workflow is a DAG handed to Atenea: every node is one agent assignment,
// every edge is "after". Steps with nothing left to wait for run at the same
// time, up to one ceiling per lane; a step that fails takes its dependents
// with it and nothing else. What the run leaves behind is a record two tables
// wide -- see [Store] -- so a workflow that was cut, or whose Atenea died,
// continues instead of starting over.
//
// Nothing here decides anything. There is no orchestrator, no model and no
// growth: the graph arrives whole, from a file or a test, and this package
// executes exactly it.
package workflow

import (
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Step is one node: an agent assignment, plus its place in the graph.
//
// It names an agent TYPE, not a capability. A capability step goes through the
// funnel, which picks an implementation on cost and health; an agent step is
// asked for by name and runs the one thing that name resolves to. The two are
// different dispatches, and a step that could be either would have to be told
// which every time.
// Effects reports the union of what this graph's steps may cause, sorted.
//
// A surface that has to authorize a graph needs one answer to "what does this
// let happen", and the effects are spread across the steps -- inside a file the
// request only names. Sorted and deduplicated so the refusal reads the same
// whichever step declared what.
func (g Graph) Effects() []contract.Effect {
	seen := make(map[contract.Effect]struct{}, 4)
	for _, step := range g.Steps {
		for _, effect := range step.Permission.Effects {
			seen[effect] = struct{}{}
		}
	}
	out := make([]contract.Effect, 0, len(seen))
	for effect := range seen {
		out = append(out, effect)
	}
	slices.Sort(out)
	return out
}

type Step struct {
	ID string
	// TypeName is the declared [[agent]] this step runs.
	TypeName string
	// Task is the assignment: what to do, over which files, and what done
	// looks like.
	Task contract.Task
	// Needs lists the step ids that must finish OK before this one starts.
	// Empty means it can start immediately.
	Needs []string
	// Subject is the step whose answer this one is handed, empty when this
	// step is handed nothing. It is an edge as well as a pipe: a step reading
	// another's answer runs after it, and saying so twice would let the two
	// halves disagree.
	//
	// One upstream, not a list. The card a subject fills is a review's, and a
	// review is of one run.
	Subject string
	// On is how much of the subject's outcome this step demands. It is only
	// meaningful with Subject, and the zero value is [OnAnswered].
	On Requirement
	// Permission is the slice of the commission this step acts under: the
	// effects it may cause and its share of the money. It is stamped by
	// whoever drew the graph, never decided by the step.
	Permission contract.Permission
	// BudgetEstimateUSD is the forecast used before dispatch. It is separate
	// from Permission.BudgetUSD, which is the approved share.
	BudgetEstimateUSD float64
	BudgetMinimumUSD  float64
	BudgetSource      string
	// Route is the resolved model/tool/provider surface for this step. It is
	// persisted with the workflow so resume cannot silently return to the
	// machine-wide defaults.
	Route *contract.Route
}

// Requirement is how much of an upstream outcome a subject edge demands.
//
// It exists as a declared word rather than as the presence of a second
// `needs` line naming the same step. Those two lines look redundant, and a
// reader tidying one away would leave a graph that still runs and quietly
// reviews things it was meant to skip -- silent, and clean-looking, which is
// the pair of properties worth spending a keyword to avoid.
type Requirement uint8

const (
	// OnAnswered runs the dependent whenever the subject produced a report
	// somebody validated: ok, failed or incomplete. It is the default because
	// "it says it failed" is the claim most worth auditing, and a reviewer
	// that only ever sees successes audits the half that needs it least.
	OnAnswered Requirement = iota
	// OnOK runs the dependent only if the subject succeeded.
	OnOK
)

var requirementNames = map[Requirement]string{OnAnswered: "answered", OnOK: "ok"}

func (r Requirement) String() string {
	if name, ok := requirementNames[r]; ok {
		return name
	}
	return "unspecified"
}

// ParseRequirement reads the word a graph file spells.
func ParseRequirement(s string) (Requirement, error) {
	switch strings.TrimSpace(s) {
	case "answered":
		return OnAnswered, nil
	case "ok":
		return OnOK, nil
	}
	return OnAnswered, contract.Fail(contract.FailureInvalidInput,
		"unknown requirement %q: answered or ok", s)
}

// satisfiedBy reports whether an upstream status clears this requirement.
//
// The line is judged versus unjudged, not good versus bad. `interrupted` is
// the one that never clears anything: nobody read a report, so there is no
// verdict to hand over, and a subject built from it would be a card claiming
// an answer that was never given.
func (r Requirement) satisfiedBy(s Status) bool {
	if r == OnOK {
		return s == StatusOK
	}
	return s == StatusOK || s == StatusFailed || s == StatusIncomplete
}

// Edge is one thing a step waits on, and how much of it the step demands.
type Edge struct {
	ID string
	On Requirement
	// Subject reports whether the answer travels along this edge, or only the
	// order.
	Subject bool
}

// Edges is everything this step waits on: its ordering edges, which always
// demand OK, and its subject edge, which demands what it declared.
//
// Every reader of the graph goes through here -- readiness, the cycle check,
// the concurrency check and the blocked reason -- so a new kind of edge is
// added in one place instead of four that can drift.
func (s Step) Edges() []Edge {
	out := make([]Edge, 0, len(s.Needs)+1)
	for _, need := range s.Needs {
		out = append(out, Edge{ID: need, On: OnOK})
	}
	if s.Subject != "" {
		out = append(out, Edge{ID: s.Subject, On: s.On, Subject: true})
	}
	return out
}

// Clone returns a deep copy.
func (s Step) Clone() Step {
	s.Task.Files = slices.Clone(s.Task.Files)
	s.Needs = slices.Clone(s.Needs)
	s.Permission = s.Permission.Clone()
	if s.Route != nil {
		route := s.Route.Clone()
		s.Route = &route
	}
	return s
}

// Graph is the whole commission: what was asked, what may be spent on it, and
// the steps that answer it.
type Graph struct {
	// Task is the commission in the user's own words. It is what a step's
	// permission is a slice OF, so it is stamped onto any step that did not
	// carry it.
	Task string
	// GrantUSD is the ceiling for the whole graph. The shares handed to the
	// steps are divided out of it and may not add up to more, because money
	// is split rather than copied -- see [contract.Permission].
	GrantUSD float64
	Steps    []Step
}

// Clone returns a deep copy.
func (g Graph) Clone() Graph {
	g.Steps = slices.Clone(g.Steps)
	for i, step := range g.Steps {
		g.Steps[i] = step.Clone()
	}
	return g
}

var stepID = regexp.MustCompile(`^[a-z0-9]+(?:[-.][a-z0-9]+)*$`)

// Plan is a graph that has been checked against the declared agent types: the
// shape holds, every type resolves, and every step's lane is known.
//
// It exists as a separate type because compiling is where every refusal
// happens. A caller holding a Plan is holding a graph that will not be turned
// away halfway through for something that was visible before anything ran.
type Plan struct {
	Graph Graph
	// Pools is each step's lane, resolved from its agent type.
	Pools map[string]config.Pool
	// order is the position of each step id in Graph.Steps, which is the
	// order the queue serves. Declaration order, so the same graph makes the
	// same choices on a slower machine.
	order map[string]int
}

// Step looks one up by id.
func (p Plan) Step(id string) (Step, bool) {
	for _, step := range p.Graph.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return Step{}, false
}

// Pool is the lane a step runs in.
func (p Plan) Pool(id string) config.Pool { return p.Pools[id] }

// Compile checks a graph against the declared types and resolves the lanes.
//
// Everything it can refuse, it refuses here: an unknown agent type, an edge
// pointing at nothing, a cycle, a step granted an effect its type never
// declared, shares adding up past the grant, and two steps that could run at
// once over the same file. A graph that compiles is one whose failures from
// then on are the agents' own.
func Compile(graph Graph, types []config.AgentType) (Plan, error) {
	if strings.TrimSpace(graph.Task) == "" {
		return Plan{}, contract.Fail(contract.FailureInvalidInput,
			"workflow: task is required: a graph nobody can say the purpose of is not reviewable")
	}
	if len(graph.Steps) == 0 {
		return Plan{}, contract.Fail(contract.FailureInvalidInput,
			"workflow: a graph with no steps has nothing to run")
	}
	if graph.GrantUSD < 0 {
		return Plan{}, contract.Fail(contract.FailureInvalidInput,
			"workflow: grant must not be negative, got %v", graph.GrantUSD)
	}

	declared := make(map[string]config.AgentType, len(types))
	for _, t := range types {
		declared[t.Spec.Name] = t
	}

	out := Plan{
		Graph: graph.Clone(),
		Pools: make(map[string]config.Pool, len(graph.Steps)),
		order: make(map[string]int, len(graph.Steps)),
	}
	var shares float64
	for i := range out.Graph.Steps {
		step := &out.Graph.Steps[i]
		if !stepID.MatchString(step.ID) {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step id %q must be lowercase", step.ID)
		}
		if _, seen := out.order[step.ID]; seen {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s is declared twice", step.ID)
		}
		out.order[step.ID] = i

		// A step's permission is a slice of the commission's, so the
		// commission is what it says it came from. Filling it here rather
		// than asking every caller to repeat the task on every step is the
		// difference between a stamp and a field nobody maintains.
		if strings.TrimSpace(step.Permission.Task) == "" {
			step.Permission.Task = out.Graph.Task
		}
		if err := step.Permission.Validate(); err != nil {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s: %v", step.ID, err)
		}
		if err := step.Task.Validate(); err != nil {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s: %v", step.ID, err)
		}
		shares += step.Permission.BudgetUSD

		agentType, ok := declared[step.TypeName]
		if !ok {
			return Plan{}, contract.Fail(contract.FailureNotFound,
				"workflow: step %s: no agent type %q: declared are %s",
				step.ID, step.TypeName, strings.Join(names(declared), ", "))
		}
		// The type's effects are the ceiling on what an agent of that type
		// may cause. Granting a step more than its type declares is a
		// promise the spawn will refuse to keep, and finding that out at
		// dispatch means the earlier steps already ran.
		for _, effect := range step.Permission.Effects {
			if !slices.Contains(agentType.Effects, effect) {
				return Plan{}, contract.Fail(contract.FailureInvalidInput,
					"workflow: step %s: granted %s, which agent type %s does not declare",
					step.ID, effect, step.TypeName)
			}
		}
		// A reviewer with nothing to review, and a step handed an answer
		// nothing in it will read: both are visible here, and both used to
		// be found at run time -- the first as an `incomplete` from an
		// agent doing the only honest thing it could with an empty card.
		if agentType.Pool == config.PoolReview && step.Subject == "" {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s: agent type %s reviews, and a review needs a subject: "+
					"name the step it audits with subject = \"<step>\"",
				step.ID, step.TypeName)
		}
		if !agentType.ReadsASubject() && step.Subject != "" {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s: subject %q is handed to agent type %s, which never "+
					"reads one: declare `reads_subject = true` on that type, or drop the edge",
				step.ID, step.Subject, step.TypeName)
		}
		if step.Subject == "" && step.On != OnAnswered {
			return Plan{}, contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s: on = %q with no subject to apply it to",
				step.ID, step.On)
		}
		out.Pools[step.ID] = agentType.Pool
	}

	// Money is split, not copied: four steps each handed the whole grant
	// would spend it four times over and every one of them would be inside
	// its own ceiling while doing it.
	//
	// The zero grant is not an exemption, and treating it as one is how a run
	// with no gate got written. `graph.GrantUSD > 0` skipped the check exactly
	// where the arithmetic is most obviously wrong -- shares that add up to
	// anything at all, out of nothing -- so the graph compiled, the run was
	// written, and Store.Ask (which does check) refused afterwards. What was
	// left behind was a workflow row with every step pending and no gate at
	// all: `list` showed it, and `resume` ran it.
	if shares > graph.GrantUSD+moneyEpsilon {
		return Plan{}, contract.Fail(contract.FailureInvalidInput,
			"workflow: step shares add up to $%.2f, past the $%.2f grant",
			shares, graph.GrantUSD)
	}

	reach, err := out.reachable()
	if err != nil {
		return Plan{}, err
	}
	if err := out.checkWriters(reach); err != nil {
		return Plan{}, err
	}
	return out, nil
}

// moneyEpsilon absorbs the last bit of binary floating point, so four shares
// of a third of a dollar are not refused for adding up to a hundredth of a
// cent more than the dollar they came from.
const moneyEpsilon = 1e-9

func names(declared map[string]config.AgentType) []string {
	out := slices.Collect(maps.Keys(declared))
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

// reachable returns, for each step, every step it transitively waits on. It
// is also the cycle check: a graph with a loop has a step that waits on
// itself, and there is no order in which to run it.
//
// Subject edges count. A step reading another's answer is ordered after it as
// surely as one that only declared `needs`, and leaving the pipe out of the
// graph would let a reviewer be scheduled beside the run it audits.
func (p Plan) reachable() (map[string]map[string]bool, error) {
	needs := make(map[string][]string, len(p.Graph.Steps))
	for _, step := range p.Graph.Steps {
		ids := make([]string, 0, len(step.Needs)+1)
		for _, edge := range step.Edges() {
			if _, ok := p.order[edge.ID]; !ok {
				if edge.Subject {
					return nil, contract.Fail(contract.FailureInvalidInput,
						"workflow: step %s reads the answer of %q, which no step declares",
						step.ID, edge.ID)
				}
				return nil, contract.Fail(contract.FailureInvalidInput,
					"workflow: step %s waits on %q, which no step declares",
					step.ID, edge.ID)
			}
			if edge.ID == step.ID {
				if edge.Subject {
					return nil, contract.Fail(contract.FailureInvalidInput,
						"workflow: step %s reviews itself", step.ID)
				}
				return nil, contract.Fail(contract.FailureInvalidInput,
					"workflow: step %s waits on itself", step.ID)
			}
			ids = append(ids, edge.ID)
		}
		needs[step.ID] = ids
	}

	out := make(map[string]map[string]bool, len(needs))
	var walk func(id string, seen map[string]bool) error
	walk = func(id string, onPath map[string]bool) error {
		if _, done := out[id]; done {
			return nil
		}
		if onPath[id] {
			return contract.Fail(contract.FailureInvalidInput,
				"workflow: step %s is part of a cycle: a graph that returns to a "+
					"finished step never drains", id)
		}
		onPath[id] = true
		acc := make(map[string]bool)
		for _, need := range needs[id] {
			if err := walk(need, onPath); err != nil {
				return err
			}
			acc[need] = true
			for further := range out[need] {
				acc[further] = true
			}
		}
		delete(onPath, id)
		out[id] = acc
		return nil
	}
	for _, step := range p.Graph.Steps {
		if err := walk(step.ID, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkWriters refuses a graph where two steps that COULD run at the same time
// both touch a file and one of them writes it.
//
// Refused when the graph is built, not serialized when it runs. A runtime
// claim would quietly make one of them wait, so the same graph would run in a
// different order on a faster machine and the ordering nobody declared would
// be load-bearing. This way the answer is the same every time and arrives
// before anything spawns.
//
// It is deliberately conservative: two steps are "concurrent" when neither
// waits on the other, whatever the lane ceilings happen to be that day. A
// ceiling is a resource decision and can change between runs; an edge is the
// author saying these two are ordered.
func (p Plan) checkWriters(reach map[string]map[string]bool) error {
	type claim struct {
		step  string
		write bool
	}
	byPath := make(map[string][]claim)
	for _, step := range p.Graph.Steps {
		writes := slices.Contains(step.Permission.Effects, contract.EffectWrite)
		for _, file := range step.Task.Files {
			clean := normalize(file)
			if clean == "" {
				continue
			}
			byPath[clean] = append(byPath[clean], claim{step: step.ID, write: writes})
		}
	}

	paths := slices.Collect(maps.Keys(byPath))
	sort.Strings(paths)
	for _, file := range paths {
		claims := byPath[file]
		for i := range claims {
			for j := i + 1; j < len(claims); j++ {
				a, b := claims[i], claims[j]
				if !a.write && !b.write {
					continue
				}
				if reach[a.step][b.step] || reach[b.step][a.step] {
					continue
				}
				writer := a
				if !writer.write {
					writer = b
				}
				return contract.Fail(contract.FailureInvalidInput,
					"workflow: steps %s and %s can run at the same time and both "+
						"touch %s, which %s writes: order them with needs, or "+
						"give them different files",
					a.step, b.step, file, writer.step)
			}
		}
	}
	return nil
}

// normalize makes two spellings of one file compare equal. Slash paths,
// because the files on a step are repository-relative and the graph is
// written by hand.
func normalize(file string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	return path.Clean(strings.ReplaceAll(file, "\\", "/"))
}
