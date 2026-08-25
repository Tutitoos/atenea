// Package plancheck is the reviewer that stands between a model and the
// engine.
//
// The planner hands back a graph as TOML text. Between that answer and a run
// there is exactly one question worth asking: does the engine accept it? This
// agent answers it the only way that cannot be wrong -- by running the same
// compiler the engine runs, against the same declared agent types, and
// handing back the compiler's own sentence when it refuses.
//
// It is a reviewer and not a validator inside the runner on purpose. A refusal
// here is a `failed` verdict on a review, which is what earns the relaunch:
// the machinery in internal/agent/review.go hands the rejected attempt and the
// sentence that rejected it to the second try. A planner told only "invalid"
// writes the same graph again.
//
// Nothing here spawns, reads the repository, or costs anything. It parses and
// compiles, which is why it can be shipped as a scripted agent and audited by
// reading it.
package plancheck

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// PlanField is the result key the planner writes its graph into, and the one
// key this reviewer knows how to read.
const PlanField = "plan"

// source is what a compile refusal calls the text it was handed. The compiler
// prints it where a path normally goes, so it has to read as a thing rather
// than a file: "workflow the plan: step a waits on itself".
const source = "the plan"

type assignment struct {
	Task struct {
		Objective string   `json:"objective"`
		Files     []string `json:"files"`
		Criterion string   `json:"criterion"`
	} `json:"task"`
	Context map[string]json.RawMessage `json:"context"`
	Subject *subject                   `json:"subject"`
}

type subject struct {
	RunID   string         `json:"run_id"`
	Type    string         `json:"type"`
	Attempt int            `json:"attempt"`
	Result  map[string]any `json:"result"`
	Verdict string         `json:"verdict"`
	Reason  *reason        `json:"reason"`
}

type report struct {
	Result  map[string]any `json:"result"`
	Verdict string         `json:"verdict"`
	Reason  *reason        `json:"reason,omitempty"`
}

type reason struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Main runs the reviewer: read the subject on stdin, compile what it planned,
// answer on stdout, exit zero whatever the verdict. Refusing a graph is this
// agent working.
func Main(stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the assignment: %w", err)
	}
	var in assignment
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("the assignment is not readable: %w", err)
	}
	return json.NewEncoder(stdout).Encode(judge(in, config.LoadEffectiveIn))
}

// loader is config.LoadEffectiveIn, named so a test can hand this agent a
// settings file without putting one on disk at the location the process would
// find.
type loader func(explicit, dir string) (config.Config, error)

func judge(in assignment, load loader) report {
	if in.Subject == nil {
		return incomplete("nothing to review: this assignment carries no subject")
	}
	s := in.Subject

	// A planner that already reported its own shortfall is not re-litigated.
	// Compiling the empty answer underneath it would replace the planner's
	// sentence with a parse error about nothing.
	switch s.Verdict {
	case "failed":
		return refuse("the planner reported failed itself: " + reasonText(s))
	case "incomplete":
		return incomplete("the planner reported incomplete itself: " + reasonText(s))
	case "canceled":
		return incomplete("the planner was canceled: " + reasonText(s))
	}

	text, ok := s.Result[PlanField].(string)
	if !ok {
		return refuse(fmt.Sprintf(
			"the answer has no %s: a planner's whole job is the graph, and there is nothing here to compile",
			PlanField))
	}
	if strings.TrimSpace(text) == "" {
		return refuse("the plan is empty")
	}

	// The settings are read here rather than served as context because what
	// this reviewer needs is the declared agent types, and those are not one
	// of the four context levels. The process inherits the environment Atenea
	// was started with, so it resolves the same settings file Atenea did.
	//
	// It has to be the EFFECTIVE settings, read at the repository the plan is
	// for, because that is what both of the other two sides read. The planner
	// writes its menu of types from config.LoadEffectiveIn on the same root,
	// and `atenea workflow create` compiles the accepted graph with
	// config.LoadEffective. Reading only the global file here put this
	// reviewer alone in the dark about `.atenea/config.toml`: a plan naming a
	// type the repository declares -- which the planner had just offered it
	// -- was refused as unknown, the run relaunched, and the second attempt
	// was refused the same way, while the engine would have compiled it.
	// The root comes from the assignment for the reason planner's does: an
	// agent's working directory was chosen by whoever spawned it and need not
	// be the tree the work is about.
	where := repositoryRoot(in)
	if where == "" {
		where = "."
	}
	cfg, err := load("", where)
	if err != nil {
		// Could not check. Not the planner's fault and not an approval: an
		// unchecked graph passed off as compiled is the exact failure this
		// reviewer exists to prevent.
		return incomplete("the declared agent types could not be read, so nothing was compiled: " +
			contract.MessageOf(err))
	}

	graph, err := workflow.ParseGraph(text, source)
	if err != nil {
		return refuse(contract.MessageOf(err))
	}
	plan, err := workflow.Compile(graph, cfg.Agents)
	if err != nil {
		return refuse(contract.MessageOf(err))
	}

	// The declared result types are string, int and bool -- there is no
	// float -- so the plan's allocation is not reported here. Inventing a
	// unit to squeeze money through an int, or printing a dollar figure as
	// prose, would both be worse than the silence: Compile has already
	// refused any plan that allocates past its grant, so an accepted plan's
	// arithmetic is a fact the verdict already carries.
	return report{
		Verdict: "ok",
		Result: map[string]any{
			"steps":   len(plan.Graph.Steps),
			"subject": s.RunID,
		},
	}
}

// refuse is the verdict that earns a relaunch: the reviewer looked, and the
// answer is wrong. The text is the compiler's own sentence, unwrapped, because
// that sentence is what the next attempt is handed and it already names both
// the step and the cure.
func refuse(text string) report {
	return report{
		Result:  map[string]any{},
		Verdict: "failed",
		Reason:  &reason{Kind: "invalid_input", Text: text},
	}
}

// incomplete is the reviewer's own shortfall, never the planner's.
func incomplete(text string) report {
	return report{
		Result:  map[string]any{},
		Verdict: "incomplete",
		Reason:  &reason{Kind: "unavailable", Text: text},
	}
}

// repositoryRoot reads the tree this plan is for out of the assignment.
//
// It is the planner's own helper, spelled again here rather than shared: the
// two agents are separate programs that happen to be compiled into one
// binary, and a package that reached into the other's internals for a
// nineteen-line struct read would tie the pair together for nothing. A root
// that does not exist on this machine is treated as absent, because a stale
// path from another host would send the overlay lookup somewhere arbitrary.
func repositoryRoot(in assignment) string {
	raw, ok := in.Context["repository"]
	if !ok {
		return ""
	}
	var level struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(raw, &level); err != nil {
		return ""
	}
	if level.Root == "" {
		return ""
	}
	if _, err := os.Stat(level.Root); err != nil {
		return ""
	}
	return level.Root
}

func reasonText(s *subject) string {
	if s.Reason == nil || strings.TrimSpace(s.Reason.Text) == "" {
		return "no reason given"
	}
	return s.Reason.Text
}
