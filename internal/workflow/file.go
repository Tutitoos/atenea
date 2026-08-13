package workflow

import (
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// fileGraph is the on-disk shape of a graph.
//
// TOML, like the settings file, because a graph is written by a person until
// something writes it for them. The wire between Atenea and an agent is JSON
// and stays JSON: that one is generated on both ends.
type fileGraph struct {
	Task     string     `toml:"task"`
	GrantUSD *float64   `toml:"budget_usd"`
	Steps    []fileStep `toml:"step"`
}

type fileStep struct {
	ID        string   `toml:"id"`
	Agent     string   `toml:"agent"`
	Objective string   `toml:"objective"`
	Files     []string `toml:"files"`
	Criterion string   `toml:"criterion"`
	Needs     []string `toml:"needs"`
	Subject   string   `toml:"subject"`
	// On is a pointer so that writing it at all can be told from leaving it
	// out. The default is the same either way; what differs is that `on`
	// declared beside no subject is a line the author believes is doing
	// something, and it is refused rather than ignored.
	On       *string  `toml:"on"`
	Effects  []string `toml:"effects"`
	GrantUSD *float64 `toml:"budget_usd"`
}

// ReadFile reads a graph from a TOML file.
//
// Unknown keys are refused rather than ignored, the same as the settings file:
// a misspelled `need` silently dropping an edge would turn an ordered graph
// into a parallel one, which is the one mistake here that still produces a
// clean run and a wrong answer.
func ReadFile(path string) (Graph, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Graph{}, contract.Fail(contract.FailureNotFound,
			"workflow: reading %s: %v", path, err)
	}
	var decoded fileGraph
	meta, err := toml.Decode(string(raw), &decoded)
	if err != nil {
		return Graph{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: %v", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Graph{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: unknown key(s): %s", path, strings.Join(keys, ", "))
	}

	out := Graph{Task: strings.TrimSpace(decoded.Task)}
	if decoded.GrantUSD != nil {
		out.GrantUSD = *decoded.GrantUSD
	}
	for _, step := range decoded.Steps {
		effects := make([]contract.Effect, 0, len(step.Effects))
		for _, name := range step.Effects {
			effect, err := contract.ParseEffect(name)
			if err != nil {
				return Graph{}, contract.Fail(contract.FailureInvalidInput,
					"workflow %s: step %s: %v", path, step.ID, err)
			}
			effects = append(effects, effect)
		}
		converted := Step{
			ID:       strings.TrimSpace(step.ID),
			TypeName: strings.TrimSpace(step.Agent),
			Task: contract.Task{
				Objective: strings.TrimSpace(step.Objective),
				Files:     step.Files,
				Criterion: strings.TrimSpace(step.Criterion),
			},
			Needs:      step.Needs,
			Subject:    strings.TrimSpace(step.Subject),
			Permission: contract.Permission{Effects: effects},
		}
		if step.On != nil {
			if converted.Subject == "" {
				return Graph{}, contract.Fail(contract.FailureInvalidInput,
					"workflow %s: step %s: on = %q with no subject to apply it to",
					path, step.ID, *step.On)
			}
			on, err := ParseRequirement(*step.On)
			if err != nil {
				return Graph{}, contract.Fail(contract.FailureInvalidInput,
					"workflow %s: step %s: %v", path, step.ID, err)
			}
			converted.On = on
		}
		if step.GrantUSD != nil {
			converted.Permission.BudgetUSD = *step.GrantUSD
		}
		out.Steps = append(out.Steps, converted)
	}
	return out, nil
}
