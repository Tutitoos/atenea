package config

import (
	"fmt"
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
	Name        string      `toml:"name"`
	Kind        string      `toml:"kind"`
	Summary     string      `toml:"summary"`
	Command     string      `toml:"command"`
	Args        []string    `toml:"args"`
	Env         []string    `toml:"env"`
	Context     []string    `toml:"context"`
	Effects     []string    `toml:"effects"`
	MaxDuration string      `toml:"max_duration"`
	MaxTokens   int         `toml:"max_tokens"`
	Result      []fileField `toml:"result"`
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
		Limits:  contract.Limits{MaxTokens: a.MaxTokens},
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
	if err := out.Validate(source); err != nil {
		return AgentType{}, err
	}
	return out, nil
}
