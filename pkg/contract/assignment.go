package contract

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxAgentDepth is how deep a chain of agents may go, counting the root as
// level 1.
//
// Three is a cap, not a measurement, and it is here rather than in settings on
// purpose: depth is the one dimension where a mistake is not visible from
// outside. A run that is too slow or too expensive announces itself; a
// delegation chain that keeps delegating looks, from every level, exactly like
// work being done. Three levels is enough for the shape the design actually
// has -- a commission that explores, splits per repository, and dispatches --
// and a fourth would mean an agent that only forwards, which is the shape
// worth refusing.
const MaxAgentDepth = 3

// MaxDiscoveryLength is the character ceiling on one discovered fact.
//
// A discovery is a fact, and a fact that needs more than a couple of lines is
// a summary of several. Two hundred characters is what makes the history
// cheap to carry into the next task, which is the only reason it is kept at
// all. Runes, not bytes: a limit that cut a multi-byte character in half
// would produce invalid text out of valid input.
const MaxDiscoveryLength = 200

// Task is the work itself: what to do, where, and how anyone will know it is
// done.
//
// Criterion is required and is the field that keeps the other two honest. An
// objective without one is a direction, and a direction cannot be reviewed:
// the parent judging this agent would have nothing to judge against except
// its own reading of the same sentence the child already read.
type Task struct {
	// Objective is what the agent is being asked to accomplish, in prose.
	Objective string
	// Files scopes the task. Empty means the agent was given no positions and
	// must find its own, which is ordinary for an exploring step and a
	// mistake for a narrow one -- so it is a fact recorded here, never a
	// default filled in on the agent's behalf.
	Files []string
	// Criterion states what "done" looks like, in terms someone other than
	// the author can check.
	Criterion string
}

// Validate checks the task.
func (t Task) Validate() error {
	if strings.TrimSpace(t.Objective) == "" {
		return Fail(FailureInvalidInput, "task: objective is required")
	}
	if strings.TrimSpace(t.Criterion) == "" {
		return Fail(FailureInvalidInput,
			"task %q: criterion is required, or nobody can review the answer", t.Objective)
	}
	for _, f := range t.Files {
		if strings.TrimSpace(f) == "" {
			return Fail(FailureInvalidInput, "task %q: empty file entry", t.Objective)
		}
	}
	return nil
}

// Clone returns a deep copy.
func (t Task) Clone() Task {
	t.Files = slices.Clone(t.Files)
	return t
}

// Limits is the ceiling on one agent's run: wall clock and tokens.
//
// Both are required and both must be positive. A missing limit is not "no
// limit" here -- it is a limit nobody decided, and the two are told apart by
// refusing the second. What money may be spent is not in this struct: that is
// Permission.BudgetUSD, because a spending ceiling is an authorization the
// user granted, while these two are resource guards the caller sets.
type Limits struct {
	// MaxDuration is the wall clock the agent may take.
	MaxDuration time.Duration
	// MaxTokens is the token ceiling for the whole run, input and output
	// together.
	MaxTokens int
}

// Validate checks the ceilings.
func (l Limits) Validate() error {
	if l.MaxDuration <= 0 {
		return Fail(FailureInvalidInput,
			"limits: max duration must be positive, got %v", l.MaxDuration)
	}
	if l.MaxTokens <= 0 {
		return Fail(FailureInvalidInput,
			"limits: max tokens must be positive, got %d", l.MaxTokens)
	}
	return nil
}

// Fits reports whether these limits are inside the parent's. A child may ask
// for less than its parent, never more.
func (l Limits) Fits(parent Limits) bool {
	return l.MaxDuration <= parent.MaxDuration && l.MaxTokens <= parent.MaxTokens
}

// AgentTypeSpec is a declared agent type: its name, its kind, and the shape
// its result must take.
//
// The shape is declared, not inferred. An agent that answers in whatever form
// it likes cannot be reviewed by a machine, only read by a person, and the
// review at every level is what the design rests on. Result reuses the same
// Field vocabulary a capability declares its outputs in, so one payload
// checker serves both and a schema that advertises a shape the validator then
// refuses cannot happen.
type AgentTypeSpec struct {
	// Name is the type name as declared in atenea.toml. An Assignment carries
	// this string; resolution against the declared set happens where the
	// settings are loaded, not here.
	Name string
	// Kind is which of the two kinds of agent this type is.
	Kind AgentType
	// Result is the shape this type's agents must answer in.
	Result []Field
}

// Validate checks the declaration.
func (s AgentTypeSpec) Validate() error {
	if !slugID.MatchString(s.Name) {
		return Fail(FailureInvalidInput, "agent type name %q must be lowercase", s.Name)
	}
	if s.Kind == AgentUnspecified {
		return Fail(FailureInvalidInput, "agent type %s: kind is required", s.Name)
	}
	if _, ok := agentTypeNames[s.Kind]; !ok {
		return Fail(FailureInvalidInput, "agent type %s: unknown kind", s.Name)
	}
	if len(s.Result) == 0 {
		return Fail(FailureInvalidInput,
			"agent type %s: declares no result shape, so no answer could be checked", s.Name)
	}
	return validateFields("result", "agent "+s.Name, s.Result)
}

// ResultSchema states the declared result shape as JSON Schema.
//
// Strict at every depth: additionalProperties is false, which is what
// ValidateResult already enforces. Advertising a door the validator has
// nailed shut is worse than advertising nothing, because the caller does as
// it is told.
func (s AgentTypeSpec) ResultSchema() (map[string]any, error) {
	return objectSchema(s.Result)
}

// ValidateResult judges a result payload against the declared shape.
func (s AgentTypeSpec) ValidateResult(payload map[string]any) error {
	return checkPayload("agent "+s.Name, "result", s.Result, payload)
}

// Clone returns a deep copy.
func (s AgentTypeSpec) Clone() AgentTypeSpec {
	s.Result = cloneFields(s.Result)
	return s
}

// Assignment is the card handed to one agent for one execution: who it is,
// what it was asked, what it may see, what it may spend and what it may
// touch.
//
// One struct for both kinds of agent. The kind is a field, so an orchestrator
// and a specialist are handed the same shape and hand back the same shape,
// and there is no second contract to keep in step with this one.
//
// Everything on it is a grant traveling downwards. Nothing an agent holds is
// something it gave itself: the parent stamps the card, and Child is the only
// way to make one from another, which is where the rules that matter are
// enforced rather than described.
type Assignment struct {
	// Version is the contract version this card was written against.
	Version Version
	// ID identifies this execution, not this agent's role -- two runs of the
	// same type are two ids. It is what a receipt, a measurement and a
	// discovery are all filed under.
	ID string
	// ParentID is the execution that handed this one out. Empty on a root
	// assignment, and empty means root: an id that names nothing is a broken
	// chain, which Validate refuses.
	ParentID string
	// Kind is orchestrator or specialized.
	Kind AgentType
	// TypeName is the declared agent type, by name, as written in
	// atenea.toml. The name travels; resolving it against the declared set is
	// the settings layer's job.
	TypeName string
	// Depth is this agent's level, counting the root as 1.
	Depth int
	// Task is the work.
	Task Task
	// Context lists the levels this agent may read. A level here is a
	// permission, never a delivery: the agent asks and is served, and one it
	// never asks for costs nothing.
	Context []ContextLevel
	// Limits is the ceiling on time and tokens.
	Limits Limits
	// Effects are the consequences this agent is allowed to cause. A child
	// never holds one its parent did not, which is enforced in Child.
	Effects []Effect
}

// RootAssignment builds a depth-1 card, the only kind with no parent.
func RootAssignment(id, typeName string, kind AgentType, task Task, limits Limits) Assignment {
	return Assignment{
		Version:  Current,
		ID:       id,
		Kind:     kind,
		TypeName: typeName,
		Depth:    1,
		Task:     task,
		Limits:   limits,
		Effects:  []Effect{EffectRead},
	}
}

// Validate checks the card on its own terms. It cannot see the parent, so the
// rules that compare the two live in Child.
func (a Assignment) Validate() error {
	if a.Version == (Version{}) {
		return Fail(FailureInvalidInput, "assignment %s: version is required", a.ID)
	}
	if !slugID.MatchString(a.ID) {
		return Fail(FailureInvalidInput, "assignment id %q must be lowercase", a.ID)
	}
	if a.ParentID != "" && !slugID.MatchString(a.ParentID) {
		return Fail(FailureInvalidInput,
			"assignment %s: parent id %q must be lowercase", a.ID, a.ParentID)
	}
	if a.ParentID == a.ID {
		return Fail(FailureInvalidInput, "assignment %s: is its own parent", a.ID)
	}
	if a.Kind == AgentUnspecified {
		return Fail(FailureInvalidInput, "assignment %s: kind is required", a.ID)
	}
	if _, ok := agentTypeNames[a.Kind]; !ok {
		return Fail(FailureInvalidInput, "assignment %s: unknown kind", a.ID)
	}
	if !slugID.MatchString(a.TypeName) {
		return Fail(FailureInvalidInput,
			"assignment %s: type name %q must be lowercase", a.ID, a.TypeName)
	}
	if err := a.validateDepth(); err != nil {
		return err
	}
	if err := a.Task.Validate(); err != nil {
		return err
	}
	if err := a.Limits.Validate(); err != nil {
		return err
	}
	return a.validateGrants()
}

func (a Assignment) validateDepth() error {
	if a.Depth < 1 {
		return Fail(FailureInvalidInput,
			"assignment %s: depth starts at 1, got %d", a.ID, a.Depth)
	}
	if a.Depth > MaxAgentDepth {
		return Fail(FailurePermissionDenied,
			"assignment %s: depth %d exceeds the cap of %d", a.ID, a.Depth, MaxAgentDepth)
	}
	if a.Depth == 1 && a.ParentID != "" {
		return Fail(FailureInvalidInput,
			"assignment %s: depth 1 is the root and has no parent, got %q", a.ID, a.ParentID)
	}
	if a.Depth > 1 && a.ParentID == "" {
		return Fail(FailureInvalidInput,
			"assignment %s: depth %d without a parent id", a.ID, a.Depth)
	}
	return nil
}

func (a Assignment) validateGrants() error {
	for _, level := range a.Context {
		if _, ok := contextLevelNames[level]; !ok || level == ContextUnspecified {
			return Fail(FailureInvalidInput, "assignment %s: unknown context level", a.ID)
		}
	}
	if len(a.Effects) == 0 {
		return Fail(FailureInvalidInput,
			"assignment %s: declares no effect, so it could not even read", a.ID)
	}
	for _, effect := range a.Effects {
		if _, ok := effectNames[effect]; !ok {
			return Fail(FailureInvalidInput, "assignment %s: unknown effect", a.ID)
		}
	}
	return nil
}

// Causes reports whether this agent is allowed to cause an effect.
func (a Assignment) Causes(effect Effect) bool { return slices.Contains(a.Effects, effect) }

// Sees reports whether this agent may read a context level.
func (a Assignment) Sees(level ContextLevel) bool { return slices.Contains(a.Context, level) }

// Child stamps a card for work this agent is handing down.
//
// This is the only way to build a non-root Assignment, and that is the point:
// the two rules that cannot be left to good behavior are enforced here, at the
// moment of creation, so a card that breaks them never exists to be passed
// around.
//
//   - A child never declares an effect its parent does not hold. Authority
//     runs downwards. An agent that can widen its own reach by describing
//     itself generously is not an authority chain, it is a suggestion.
//   - Depth is capped. The parent's level plus one, refused past MaxAgentDepth.
//
// Context and limits narrow the same way, for the same reason. Effects, levels
// and ceilings are all grants, and a grant that a holder can enlarge on the
// way down is not a grant.
func (a Assignment) Child(id, typeName string, kind AgentType, task Task, want []Effect, limits Limits) (Assignment, error) {
	if err := a.Validate(); err != nil {
		return Assignment{}, Fail(FailureInvalidInput,
			"assignment %s cannot hand out work: %s", a.ID, err.Error())
	}
	if a.Depth >= MaxAgentDepth {
		return Assignment{}, Fail(FailurePermissionDenied,
			"assignment %s is at depth %d: a child would be %d, past the cap of %d",
			a.ID, a.Depth, a.Depth+1, MaxAgentDepth)
	}
	for _, effect := range want {
		if !a.Causes(effect) {
			return Assignment{}, Fail(FailurePermissionDenied,
				"assignment %s cannot grant %s to child %s: it does not hold it itself",
				a.ID, effect, id)
		}
	}
	if !limits.Fits(a.Limits) {
		return Assignment{}, Fail(FailurePermissionDenied,
			"assignment %s cannot grant child %s more than it holds: %v/%d tokens against %v/%d",
			a.ID, id, limits.MaxDuration, limits.MaxTokens,
			a.Limits.MaxDuration, a.Limits.MaxTokens)
	}
	child := Assignment{
		Version:  a.Version,
		ID:       id,
		ParentID: a.ID,
		Kind:     kind,
		TypeName: typeName,
		Depth:    a.Depth + 1,
		Task:     task.Clone(),
		Context:  slices.Clone(a.Context),
		Limits:   limits,
		Effects:  slices.Clone(want),
	}
	if err := child.Validate(); err != nil {
		return Assignment{}, err
	}
	return child, nil
}

// Clone returns a deep copy.
func (a Assignment) Clone() Assignment {
	a.Task = a.Task.Clone()
	a.Context = slices.Clone(a.Context)
	a.Effects = slices.Clone(a.Effects)
	return a
}

// Reason is why a verdict came out the way it did: one of the common bins,
// plus the sentence a person actually needs.
//
// Both halves, always. The bin is what the core reacts to and it has to be a
// closed set or every consumer ends up matching on prose; the text is what
// makes the bin actionable, because "not_found" on its own has never told
// anybody which thing was not found.
type Reason struct {
	Kind FailureKind
	Text string
}

// Validate checks the reason.
func (r Reason) Validate() error {
	if r.Kind == FailureUnspecified {
		return Fail(FailureInvalidInput,
			"reason: kind is required, and unspecified is a bug rather than a bin")
	}
	if _, ok := failureNames[r.Kind]; !ok {
		return Fail(FailureInvalidInput, "reason: unknown kind")
	}
	if strings.TrimSpace(r.Text) == "" {
		return Fail(FailureInvalidInput, "reason %s: text is required", r.Kind)
	}
	return nil
}

func (r Reason) String() string { return r.Kind.String() + ": " + r.Text }

// Empty reports whether nobody filled this reason in.
func (r Reason) Empty() bool {
	return r.Kind == FailureUnspecified && strings.TrimSpace(r.Text) == ""
}

// Report is what an agent hands back: three things with three destinations.
//
// The result goes to whoever asked, in the shape the agent type declared. The
// verdict is consumed by the reviewing parent. The discovered facts feed the
// history, so the next task does not pay to learn them again.
type Report struct {
	// Result is the answer, in the shape the agent type declares.
	Result map[string]any
	// Verdict is ok, failed, incomplete or canceled.
	Verdict Verdict
	// Reason is why. Required for everything except a plain ok with a result
	// in it -- see Validate, which is where that one exception is spelled
	// out.
	Reason Reason
	// Discovered are the short facts worth outliving this task.
	Discovered []Discovery
	// Notices are caveats about this report that are not failures. Normalize
	// writes here when it has to shorten a discovery, so a truncated fact is
	// never silently truncated.
	Notices []string
}

// Validate judges the report against the type that produced it.
//
// The spec is required rather than optional. A report whose shape nobody
// checked is a report a parent has to read as prose, and the review at every
// level is the design's load-bearing beam.
func (r Report) Validate(spec AgentTypeSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if r.Verdict == VerdictUnspecified {
		return Fail(FailureInvalidInput, "report: verdict is required")
	}
	if _, ok := verdictNames[r.Verdict]; !ok {
		return Fail(FailureInvalidInput, "report: unknown verdict")
	}
	if err := r.validateReason(); err != nil {
		return err
	}
	if err := r.validateDiscovered(); err != nil {
		return err
	}
	if len(r.Result) == 0 {
		// Nothing to check against the schema, and the missing answer has
		// already been accounted for by the rule above.
		return nil
	}
	return spec.ValidateResult(r.Result)
}

// validateReason enforces the two rules about why.
//
// An empty result always needs one, whatever the verdict says. An agent that
// answers nothing and calls it ok is the most expensive shape there is: it
// looks like success to every counter, and the only way to find out otherwise
// is to open the result and notice it is not there. Making the reason
// mandatory turns that into a refusal at the boundary.
//
// Everything that is not plain ok needs one too, incomplete very much
// included: incomplete without a reason cannot be acted on, because the whole
// content of the verdict is which part is missing.
func (r Report) validateReason() error {
	if r.Verdict == VerdictOK && len(r.Result) > 0 {
		if r.Reason.Empty() {
			return nil
		}
		return r.Reason.Validate()
	}
	if r.Reason.Empty() {
		if len(r.Result) == 0 {
			return Fail(FailureInvalidInput,
				"report: verdict %s with an empty result requires a reason", r.Verdict)
		}
		return Fail(FailureInvalidInput,
			"report: verdict %s requires a reason", r.Verdict)
	}
	return r.Reason.Validate()
}

func (r Report) validateDiscovered() error {
	for _, d := range r.Discovered {
		if _, ok := contextLevelNames[d.Level]; !ok || d.Level == ContextUnspecified {
			return Fail(FailureInvalidInput,
				"report: discovery %q has no context level to be filed under", d.Note)
		}
		if strings.TrimSpace(d.Note) == "" {
			return Fail(FailureInvalidInput, "report: empty discovery")
		}
		if utf8.RuneCountInString(d.Note) > MaxDiscoveryLength {
			return Fail(FailureInvalidInput,
				"report: discovery is %d characters, over the %d cap: call Normalize first",
				utf8.RuneCountInString(d.Note), MaxDiscoveryLength)
		}
	}
	return nil
}

// Normalize returns a copy with over-long discoveries shortened to the cap,
// each shortening announced in Notices.
//
// Truncating is the right call -- a fact worth keeping should not be dropped
// because its author was wordy -- but doing it quietly is not. The reader of a
// clipped fact would have no way to tell it from a complete one, and would
// act on half a sentence believing it whole. So the cut leaves a mark, in the
// one field a caller already reads for caveats.
func (r Report) Normalize() Report {
	out := r.Clone()
	for i, d := range out.Discovered {
		length := utf8.RuneCountInString(d.Note)
		if length <= MaxDiscoveryLength {
			continue
		}
		out.Discovered[i].Note = string([]rune(d.Note)[:MaxDiscoveryLength])
		out.Notices = append(out.Notices,
			"discovery truncated from "+strconv.Itoa(length)+" characters to "+
				strconv.Itoa(MaxDiscoveryLength)+": "+out.Discovered[i].Note)
	}
	return out
}

// Failed reports whether the work cannot be trusted. Incomplete is not failed:
// see VerdictIncomplete for why the two must never be read as one.
func (r Report) Failed() bool { return r.Verdict == VerdictFailed }

// Incomplete reports whether the work stopped short of the objective while
// what it did produce still stands.
func (r Report) Incomplete() bool { return r.Verdict == VerdictIncomplete }

// Clone returns a deep copy.
func (r Report) Clone() Report {
	out := r
	if r.Result != nil {
		out.Result = make(map[string]any, len(r.Result))
		for k, v := range r.Result {
			out.Result[k] = v
		}
	}
	out.Discovered = slices.Clone(r.Discovered)
	out.Notices = slices.Clone(r.Notices)
	return out
}
