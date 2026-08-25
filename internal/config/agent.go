package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// AgentType is one declared kind of agent: the contract half (name, kind,
// result shape) and the machine half (what to spawn, what it may see, what it
// may spend and what it may touch).
//
// The two halves are one block in the settings file on purpose. A result
// shape declared in one place and a command declared in another is two
// declarations that must agree about which agent they describe, and nothing
// would notice when they stopped.
type AgentType struct {
	// Spec is what pkg/contract needs: the name a caller resolves, the kind,
	// and the shape every answer is checked against.
	Spec contract.AgentTypeSpec
	// Summary is one line for `atenea catalog`.
	Summary string
	// Command is the binary Atenea spawns, and Args are the arguments before
	// the assignment. The assignment itself never rides on argv -- it goes in
	// on stdin, because a task naming a real file list overruns ARG_MAX.
	Command string
	Args    []string
	// Env is added to the child's environment, as KEY=VALUE.
	Env []string
	// Context lists the levels Atenea serves this type. Anything not listed
	// is not sent, which is the whole point of declaring them.
	Context []contract.ContextLevel
	// Effects are what an agent of this type may cause. It is a ceiling, not
	// a request: a child of this agent can hold these or fewer, never more.
	Effects []contract.Effect
	// Limits is the ceiling on one run.
	Limits contract.Limits
	// Pool is which parallel lane this type is scheduled in.
	//
	// Reviews do not compete with the work they judge. A machine that let one
	// pool hold both would starve its own auditing exactly when it is busiest
	// -- every slot full of agents, the reviewer queued behind them, and the
	// answers piling up unjudged. Declaring the lane here is what will let a
	// scheduler size the two separately.
	//
	// Nothing schedules anything today: `atenea agent` runs one agent at a
	// time and there is no cap to compete for. The field is here because the
	// distinction belongs to the TYPE rather than to whoever dispatches it,
	// and a lane inferred at dispatch time is a lane two callers will infer
	// differently.
	Pool Pool
	// ReadsSubject says this type is handed another step's whole answer.
	//
	// Separate from Pool, because reviewing and reading a subject are two
	// different facts that happened to coincide while reviewers were the
	// only consumers. A reviewer reads a subject AND is scheduled in the
	// audit lane; the planner reads one and is ordinary work. Inferring the
	// input from the lane forced the choice of putting a planner in the
	// review pool, where it would compete with auditing for slots sized for
	// auditing.
	//
	// Read it through [AgentType.ReadsASubject], which is where the review
	// implication lives.
	ReadsSubject bool
	// Local is true when this type came from a repository's own overlay
	// rather than from this machine's settings.
	//
	// Written by the merge and never by a file: fileAgent has no field for
	// it and localAgent has none either, so a repository cannot dress its
	// own type as shipped. It exists because the planner is told which types
	// are the repository's own, and a flag Atenea sets beside a name Atenea
	// validated is the only part of that sentence a cloned file does not
	// write.
	Local bool
}

// ReadsASubject reports whether this type is handed another step's answer.
//
// A review-pool type does whether or not it declared so: a review with
// nothing to review is refused anyway, so requiring the flag as well would
// only be a second way to write the same settings wrong. The implication is
// here rather than at parse time so that it holds for a type built in code
// too -- a test's fixture and a shipped declaration answer the same.
func (a AgentType) ReadsASubject() bool { return a.ReadsSubject || a.Pool == PoolReview }

// Pool is a parallel lane. A closed set: an unknown lane in a settings file
// is a typo that would otherwise create a third pool nothing sizes.
type Pool uint8

// The lanes that exist.
const (
	// PoolAgent is the default: work that produces answers.
	PoolAgent Pool = iota
	// PoolReview is auditing, sized separately so it cannot be crowded out
	// by the work it audits.
	PoolReview
)

var poolNames = map[Pool]string{PoolAgent: "agent", PoolReview: "review"}

func (p Pool) String() string {
	if name, ok := poolNames[p]; ok {
		return name
	}
	return "pool(" + strconv.Itoa(int(p)) + ")"
}

// ParsePool reads a lane name. Empty means the default lane, because most
// types are ordinary work and should not have to say so.
func ParsePool(s string) (Pool, error) {
	switch strings.TrimSpace(s) {
	case "", "agent":
		return PoolAgent, nil
	case "review":
		return PoolReview, nil
	}
	return PoolAgent, contract.Fail(contract.FailureInvalidInput,
		"unknown pool %q: agent or review", s)
}

// Validate checks the declaration.
func (a AgentType) Validate(source string) error {
	fail := func(format string, args ...any) error {
		return contract.Fail(contract.FailureInvalidInput,
			"settings %s: agent %s: %s", source, a.Spec.Name, fmt.Sprintf(format, args...))
	}
	if err := a.Spec.Validate(); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "settings %s: %v", source, err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return fail("command is required: an agent type nothing can spawn is a promise with no far side")
	}
	if len(a.Context) == 0 {
		return fail("declares no context level, so it would run blind")
	}
	for _, level := range a.Context {
		if level == contract.ContextUnspecified {
			return fail("empty context level")
		}
	}
	if len(a.Effects) == 0 {
		return fail("declares no effect, so it could not even read")
	}
	if err := a.Limits.Validate(); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "settings %s: agent %s: %v",
			source, a.Spec.Name, err)
	}
	return nil
}

// AgentTypeByName resolves a declared type by name.
//
// The failure names what is declared. A caller that mistyped a name is one
// keystroke from the answer, and the alternative -- "unknown agent type" with
// nothing after it -- sends them to the settings file to find out what this
// machine even has.
func (c Config) AgentTypeByName(name string) (AgentType, error) {
	for _, agent := range c.Agents {
		if agent.Spec.Name == name {
			return agent, nil
		}
	}
	declared := make([]string, 0, len(c.Agents))
	for _, agent := range c.Agents {
		declared = append(declared, agent.Spec.Name)
	}
	if len(declared) == 0 {
		return AgentType{}, contract.Fail(contract.FailureNotFound,
			"no agent type %q: this settings file declares none", name)
	}
	return AgentType{}, contract.Fail(contract.FailureNotFound,
		"no agent type %q: declared are %s", name, strings.Join(declared, ", "))
}

// fileAgent is the TOML shape of AgentType.
type fileAgent struct {
	Name        string   `toml:"name"`
	Kind        string   `toml:"kind"`
	Summary     string   `toml:"summary"`
	Command     string   `toml:"command"`
	Args        []string `toml:"args"`
	Env         []string `toml:"env"`
	Context     []string `toml:"context"`
	Effects     []string `toml:"effects"`
	MaxDuration string   `toml:"max_duration"`
	// MaxTokens is a pointer so an absent key is distinguishable from a
	// written zero. contract.Limits reads zero as "the caller declared no
	// boundary", so a plain int would hand every [[agent]] that forgot the key
	// an agent with its token ceiling switched off -- the exact opposite of
	// what this file's own header and docs/content/settings.md promise.
	MaxTokens    *int        `toml:"max_tokens"`
	Pool         string      `toml:"pool"`
	ReadsSubject bool        `toml:"reads_subject"`
	Result       []fileField `toml:"result"`
}

func (a fileAgent) build(source string) (AgentType, error) {
	fail := func(format string, args ...any) (AgentType, error) {
		return AgentType{}, contract.Fail(contract.FailureInvalidInput,
			"settings %s: agent %s: %s", source, a.Name, fmt.Sprintf(format, args...))
	}
	kind, err := contract.ParseAgentType(a.Kind)
	if err != nil {
		return fail("%v", err)
	}
	out := AgentType{
		Spec:    contract.AgentTypeSpec{Name: a.Name, Kind: kind},
		Summary: strings.TrimSpace(a.Summary),
		Command: strings.TrimSpace(a.Command),
		Args:    a.Args,
		Env:     a.Env,
		// Declared, not derived: ReadsASubject adds the review implication
		// where it is read, so a round-trip through this file preserves what
		// the settings actually said.
		ReadsSubject: a.ReadsSubject,
	}
	if out.Pool, err = ParsePool(a.Pool); err != nil {
		return fail("%v", err)
	}
	if out.Spec.Result, err = buildFields(a.Result); err != nil {
		return fail("%v", err)
	}
	for _, name := range a.Context {
		level, err := contract.ParseContextLevel(name)
		if err != nil {
			return fail("%v", err)
		}
		out.Context = append(out.Context, level)
	}
	for _, name := range a.Effects {
		effect, err := contract.ParseEffect(name)
		if err != nil {
			return fail("%v", err)
		}
		out.Effects = append(out.Effects, effect)
	}
	if strings.TrimSpace(a.MaxDuration) == "" {
		return fail("max_duration is required")
	}
	if out.Limits.MaxDuration, err = time.ParseDuration(a.MaxDuration); err != nil {
		return fail("max_duration %q: %v", a.MaxDuration, err)
	}
	// Both ceilings have to be written down. An omitted max_tokens would reach
	// contract.Limits as zero, which Limits.Fits reads as a parent that
	// constrains nothing, so the agent would run with no token ceiling at all
	// and nobody would have chosen that. Writing zero on purpose is still
	// allowed -- that is an operator deciding there is no boundary, which is a
	// different act from forgetting the key.
	if a.MaxTokens == nil {
		return fail("max_tokens is required; write 0 to declare no token ceiling")
	}
	out.Limits.MaxTokens = *a.MaxTokens
	if err := out.Validate(source); err != nil {
		return AgentType{}, err
	}
	return out, nil
}
