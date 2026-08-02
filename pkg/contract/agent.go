package contract

import (
	"slices"
	"strings"
)

// AgentType tells the orchestrator apart from the specialists.
//
// There is ONE agent contract, not two. A field says which family an agent
// belongs to and the boxes that do not apply to it stay empty. Two separate
// contracts would drift apart the first time one of them grew a field, and
// sharing the shape costs nothing: the contract is the mold, not the content.
type AgentType uint8

// The two families of agent.
const (
	// AgentUnspecified is the zero value and is never valid on a real agent.
	AgentUnspecified AgentType = iota
	// AgentOrchestrator explores, splits the commission and hands out the
	// pieces. It decides; it does not do the work itself.
	AgentOrchestrator
	// AgentSpecialist runs one concrete thing: search, write code, audit.
	AgentSpecialist
)

var agentTypeNames = map[AgentType]string{
	AgentUnspecified:  "",
	AgentOrchestrator: "orchestrator",
	AgentSpecialist:   "specialist",
}

func (t AgentType) String() string {
	if name, ok := agentTypeNames[t]; ok {
		return name
	}
	return "unknown"
}

// ParseAgentType reads an agent type name.
func ParseAgentType(s string) (AgentType, error) {
	for value, name := range agentTypeNames {
		if name != "" && name == strings.ToLower(strings.TrimSpace(s)) {
			return value, nil
		}
	}
	return AgentUnspecified, Fail(FailureInvalidInput, "unknown agent type %q", s)
}

// ContextLevel is one of the four heights context is stored at.
//
// An agent never gets everything. It works with the levels it needs and no
// more, which is what keeps the token bill down and the blast radius small.
type ContextLevel uint8

// The four context levels.
const (
	// ContextUnspecified is the zero value and is never a valid declaration.
	ContextUnspecified ContextLevel = iota
	// ContextRepository is what happens inside one repository: its detail, how
	// it works from the inside. What happens in a repository stays there.
	ContextRepository
	// ContextWorkspace is the map of relations between repositories: who calls
	// whom. Deliberately separate from the repository level.
	ContextWorkspace
	// ContextGlobal is what holds everywhere regardless of repository, such as
	// the language rule. Atenea carries it into every task.
	ContextGlobal
	// ContextHistory is what happened in earlier sessions: user decisions and
	// facts Atenea discovered. Little and good, loaded lazily.
	ContextHistory
)

var contextLevelNames = map[ContextLevel]string{
	ContextUnspecified: "",
	ContextRepository:  "repository",
	ContextWorkspace:   "workspace",
	ContextGlobal:      "global",
	ContextHistory:     "history",
}

func (l ContextLevel) String() string {
	if name, ok := contextLevelNames[l]; ok {
		return name
	}
	return "unknown"
}

// ParseContextLevel reads a context level name.
func ParseContextLevel(s string) (ContextLevel, error) {
	for value, name := range contextLevelNames {
		if name != "" && name == strings.ToLower(strings.TrimSpace(s)) {
			return value, nil
		}
	}
	return ContextUnspecified, Fail(FailureInvalidInput, "unknown context level %q", s)
}

// Agent is the card every agent carries: what it is, what it may ask for and
// which context levels it is entitled to read.
//
// Declaring a level is a PERMISSION, not a delivery. The agent states what it
// has a right to; Atenea does not hand it over until the agent actually asks.
// That is what lets the contract double as the "who touches what" map without
// inflating every agent with context it will never open.
type Agent struct {
	ID      string
	Type    AgentType
	Summary string
	// Capabilities the agent may ask for, by capability id. An agent asking for
	// anything else is refused: authority runs downwards and an agent that can
	// widen its own reach is not an authority chain.
	Capabilities []string
	// Context lists the levels this agent is entitled to read.
	Context []ContextLevel
}

// Validate checks the card itself.
func (a Agent) Validate() error {
	if !slugID.MatchString(a.ID) {
		return Fail(FailureInvalidInput, "agent id %q must be lowercase", a.ID)
	}
	if a.Type == AgentUnspecified {
		return Fail(FailureInvalidInput, "agent %s: type is required", a.ID)
	}
	if _, ok := agentTypeNames[a.Type]; !ok {
		return Fail(FailureInvalidInput, "agent %s: unknown type", a.ID)
	}
	if strings.TrimSpace(a.Summary) == "" {
		return Fail(FailureInvalidInput, "agent %s: summary is required", a.ID)
	}
	if len(a.Capabilities) == 0 {
		return Fail(FailureInvalidInput,
			"agent %s: declares no capability, so it can never be given work", a.ID)
	}
	for _, id := range a.Capabilities {
		if !capabilityID.MatchString(id) {
			return Fail(FailureInvalidInput,
				"agent %s: capability %q must be dotted lowercase", a.ID, id)
		}
	}
	for _, level := range a.Context {
		if level == ContextUnspecified {
			return Fail(FailureInvalidInput, "agent %s: empty context level", a.ID)
		}
		if _, ok := contextLevelNames[level]; !ok {
			return Fail(FailureInvalidInput, "agent %s: unknown context level", a.ID)
		}
	}
	return nil
}

// Sees reports whether the agent declared a right to this context level. It
// answers the permission question, never the delivery one.
func (a Agent) Sees(level ContextLevel) bool { return slices.Contains(a.Context, level) }

// CanAsk reports whether the agent is allowed to request this capability.
func (a Agent) CanAsk(capability string) bool { return slices.Contains(a.Capabilities, capability) }

// Clone returns a deep copy.
func (a Agent) Clone() Agent {
	a.Capabilities = slices.Clone(a.Capabilities)
	a.Context = slices.Clone(a.Context)
	return a
}

// Verdict is the "did it go well" half of what an agent hands back.
type Verdict uint8

// The verdicts an agent or its reviewer can reach.
const (
	// VerdictUnspecified is the zero value: nobody has judged this yet.
	VerdictUnspecified Verdict = iota
	VerdictOK
	VerdictFailed
)

var verdictNames = map[Verdict]string{
	VerdictUnspecified: "unjudged",
	VerdictOK:          "ok",
	VerdictFailed:      "failed",
}

func (v Verdict) String() string {
	if name, ok := verdictNames[v]; ok {
		return name
	}
	return "unknown"
}

// Discovery is something an agent learned on the way that outlives the task.
//
// It is filed under the context level it belongs to, so the same fact does not
// have to be paid for twice: next time it is read instead of re-explored.
type Discovery struct {
	Level ContextLevel
	Note  string
}

// Outcome is what an agent hands back when it finishes: three things, each
// with a different destination.
//
// The result goes to whoever asked. The verdict is consumed by the reviewing
// parent. The discoveries feed the history so the next task does not pay to
// learn the same thing again. Spent rides along because the core is the only
// writer of metrics: agents report their numbers upwards, they never write.
type Outcome struct {
	Result      map[string]any
	Verdict     Verdict
	Discoveries []Discovery
	Spent       Sample
}
