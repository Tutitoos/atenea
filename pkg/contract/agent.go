package contract

import (
	"slices"
	"strings"
)

// AgentType names the kind of agent a card describes. Two kinds exist: the
// orchestrator, which explores, splits a commission and hands out the pieces,
// deciding everything a run needs decided; and the specialized agent, which
// is handed one objective already decided and answers in the shape its type
// declares.
//
// A field, not a fork. Both kinds carry the same Assignment and hand back the
// same Report, so there is one agent contract and no pair of them to drift
// apart. What differs between the kinds is authority -- who may split work --
// and that is a value here, checked where it matters, rather than a second
// struct with its own rules.
type AgentType uint8

// The kinds of agent.
const (
	// AgentUnspecified is the zero value and is never valid on a real agent.
	AgentUnspecified AgentType = iota
	// AgentOrchestrator explores, splits the commission and hands out the
	// pieces. It decides; it does not do the work itself.
	AgentOrchestrator
	// AgentSpecialized executes one objective it was handed and answers in
	// the shape its declared type promises. It never splits work.
	AgentSpecialized
)

var agentTypeNames = map[AgentType]string{
	AgentUnspecified:  "",
	AgentOrchestrator: "orchestrator",
	AgentSpecialized:  "specialized",
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

	// VerdictCanceled is what a step that nobody let finish gets, and it is
	// not a judgement at all -- it is the absence of one.
	//
	// Failed is a claim about the work. Reviewing something that never came
	// back and calling it failed makes that claim on no evidence, and blames
	// the work for a decision the person at the keyboard made. The two have
	// to be different words on the screen, because a reader acts on them
	// differently: one is worth investigating and the other is not.
	VerdictCanceled

	// VerdictIncomplete is work that got somewhere and stopped short: part of
	// the objective is done, the rest is not, and nothing about what came
	// back is wrong.
	//
	// It is not a softer Failed and must never be folded into one. Failed
	// says the answer cannot be trusted, so the right move is to discard it
	// and investigate. Incomplete says the answer is sound as far as it goes,
	// so the right move is to keep it and continue -- and a caller that reads
	// the two as one bin throws away work that was fine, or trusts work that
	// was not. The distinction is what the reason field is for: an incomplete
	// report says which part is missing and why it stopped.
	VerdictIncomplete
)

var verdictNames = map[Verdict]string{
	VerdictUnspecified: "unjudged",
	VerdictOK:          "ok",
	VerdictFailed:      "failed",
	VerdictCanceled:    "canceled",
	VerdictIncomplete:  "incomplete",
}

func (v Verdict) String() string {
	if name, ok := verdictNames[v]; ok {
		return name
	}
	return "unknown"
}

// ParseVerdict reads a verdict name, the same way a receipt's Review field
// was written by String().
func ParseVerdict(s string) (Verdict, error) {
	for value, name := range verdictNames {
		if name == strings.ToLower(strings.TrimSpace(s)) {
			return value, nil
		}
	}
	return VerdictUnspecified, Fail(FailureInvalidInput, "unknown verdict %q", s)
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
	// Evidence contains validated provider observations, never instructions.
	Evidence    []QueryEvidence
	Result      map[string]any
	Verdict     Verdict
	Discoveries []Discovery
	Spent       Sample
	// ToolVersion is what the far side answered when asked who it is. The
	// settings file also carries a version, but that one is a declaration and
	// this one is a fact: the case worth catching is a tool upgraded on disk
	// by someone who never opened the TOML. Measurements are filed under this
	// string, so an upgrade starts a fresh baseline instead of dragging the
	// old numbers along. Empty means the far side would not say.
	ToolVersion string
	// SpentUSD is what the far side actually charged for this call, or zero
	// when nothing was charged or nobody said.
	//
	// It is deliberately not in Sample. Sample is the measurement base: three
	// physical facts about a run -- time, tokens, memory -- that come out the
	// same if you run the same work again. A dollar figure is a fact about a
	// price list and an account, and it changes without anything in the
	// repository changing. Filed as a measurement it would silently rewrite
	// the meaning of every historical row the next time a price moved, and it
	// would average across whoever's key happened to pay.
	//
	// So money never ranks. It is reported: summed onto the receipt, so a
	// human can see what a commission cost. What money does govern is
	// permission -- a ceiling on what one call may spend -- and that lives
	// with the grant, not here.
	//
	// Read it beside SpentUSDKnown, always. On its own a zero here is two
	// different facts wearing the same face.
	SpentUSD float64
	// SpentUSDKnown says whether SpentUSD is a measurement.
	//
	// Without it, zero means both "this call cost nothing" and "nobody said",
	// and those are not the same news: the first is a fact about a free
	// provider, the second is the absence of one. Charge.USD keeps them apart
	// with a pointer and CostUpdate keeps them apart with exactly this field;
	// Outcome did neither, so a receipt for a run through ripgrep and a
	// receipt for a run whose provider reported nothing read identically.
	//
	// A bool rather than a *float64, matching CostUpdate.Known rather than
	// Charge.USD, and the reason is the seam. Outcome travels on the Runner
	// interface, which this package cannot extend without breaking every
	// implementer, so the shape that costs least is the one that leaves the
	// existing field alone: an adapter built before this goes on compiling
	// and leaves it false, which reads as "nobody said" -- the honest answer
	// for an adapter that never had a way to say otherwise.
	SpentUSDKnown bool
	// Notices are caveats about this Outcome that do not rise to a failure --
	// reasons the Result may still be right but should not be taken as the
	// last word (an adapter saying "the index may be stale", say). They
	// differ from a Discovery in destination and shelf life: a Discovery is a
	// fact about the repository worth paying to remember, a Notice is a doubt
	// about this one answer, read once by whoever asked and not carried into
	// history. Empty means this adapter had nothing to add, not that
	// freshness (or anything else a Notice might cover) was checked and
	// confirmed clean -- most capabilities carry no such check at all.
	Notices []string
	// OutOfScope is how many results this call returned that fell outside the
	// scope it was asked for, and which the adapter dropped before answering.
	// Zero means either a clean answer or a provider that cannot stray --
	// one confined by construction has nothing to count.
	//
	// It is a fact about the quality of this answer, and it is deliberately
	// three things at once that it is not. It is not a Notice: a sentence is
	// read once by whoever asked and then gone, which is exactly why a
	// provider that wandered on every call paid nothing for it. It is not in
	// Sample, for the same reason SpentUSD is not -- Sample is time, tokens
	// and memory, the physical cost of running the work, and this is a
	// judgement about what came back. And it is not a failure: the answer is
	// right, the core already filtered it, and marking the provider down
	// would remove a provider that works to punish a defect that was
	// neutralized.
	//
	// What it is for is ranking. Wandering is waste -- tokens and time spent
	// on results nobody could use -- and waste belongs to cost, which is the
	// funnel stage that decides between providers that all work.
	OutOfScope int
}

// QueryEvidence keeps bounded graph answers distinct from exhaustive results.
// It lives outside Result so existing capability output contracts stay intact.
type QueryEvidence struct {
	Freshness         string `json:"freshness,omitempty"`
	Tool              string `json:"tool"`
	SnapshotID        int    `json:"snapshot_id,omitempty"`
	Completeness      string `json:"completeness,omitempty"`
	Truncated         bool   `json:"truncated"`
	NextCursor        string `json:"next_cursor,omitempty"`
	Exact             int    `json:"exact"`
	Candidate         int    `json:"candidate"`
	UnresolvedRelated int    `json:"unresolved_related"`
	PackageLevel      int    `json:"package_level"`
	BlindSpots        int    `json:"blind_spots"`
	InvisibleScopes   int    `json:"invisible_scopes"`
}
