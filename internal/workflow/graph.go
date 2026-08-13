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
	// Permission is the slice of the commission this step acts under: the
	// effects it may cause and its share of the money. It is stamped by
	// whoever drew the graph, never decided by the step.
	Permission contract.Permission
}

// Clone returns a deep copy.
func (s Step) Clone() Step {
	s.Task.Files = slices.Clone(s.Task.Files)
	s.Needs = slices.Clone(s.Needs)
	s.Permission = s.Permission.Clone()
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
		out.Pools[step.ID] = agentType.Pool
	}

	// Money is split, not copied: four steps each handed the whole grant
	// would spend it four times over and every one of them would be inside
	// its own ceiling while doing it.
	if graph.GrantUSD > 0 && shares > graph.GrantUSD+moneyEpsilon {
		return Plan{}, contract.Fail(contract.FailureInvalidInput,
			"workflow: step shares add up to %.2f, past the %.2f grant",
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
func (p Plan) reachable() (map[string]map[string]bool, error) {
	needs := make(map[string][]string, len(p.Graph.Steps))
	for _, step := range p.Graph.Steps {
		for _, need := range step.Needs {
			if _, ok := p.order[need]; !ok {
				return nil, contract.Fail(contract.FailureInvalidInput,
					"workflow: step %s waits on %q, which no step declares",
					step.ID, need)
			}
			if need == step.ID {
				return nil, contract.Fail(contract.FailureInvalidInput,
					"workflow: step %s waits on itself", step.ID)
			}
		}
		needs[step.ID] = step.Needs
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
