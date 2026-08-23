package contract

import (
	"maps"
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

// Limits is what one agent's run was told to hold itself to: wall clock and
// tokens.
//
// Both are required and both must be positive. A missing limit is not "no
// limit" here -- it is a limit nobody decided, and the two are told apart by
// refusing the second. What money may be spent is not in this struct: that is
// Permission.BudgetUSD, because money is an authorization a person gave while
// these two are guards the caller sets.
//
// MaxDuration is a ceiling something enforces: it reaches a model turn as
// model.Request.Timeout. MaxTokens reaches the model client as both the
// observed read boundary and a local answer-level acceptance check. Neither
// field promises to stop an external process at an exact provider event
// boundary.
type Limits struct {
	// MaxDuration is the wall clock the agent may take.
	MaxDuration time.Duration
	// MaxTokens is the token count the caller declared this run should be
	// held to, input and output together. When a grant exists, the planner also
	// uses it as the upper bound for the client's observed incremental read
	// allowance; zero means the caller declared none.
	//
	// This is not an exact provider hard cap. The client can observe an event
	// after the boundary and must then request a final answer, so in-flight
	// usage may overshoot. The distinction is intentional: Atenea now applies
	// the declared boundary without claiming control over provider internals.
	//
	// Validate required it positive until 2026-08-16, so that "nobody
	// decided" could not pass for "no limit". That refusal bought invented
	// numbers instead of decisions, and the live settings file said so in a
	// comment: three agent types declared `max_tokens = 1` with "it spends no
	// tokens; the ceiling still has to be a real number". The two that back a
	// model declared 200,000, which steps that finished `ok` already exceed --
	// 224,148 cache-read tokens on one of them. Honoring any of those numbers
	// would have cut work that completes.
	//
	// Kept as an assignment-level declaration so adapters and reports retain
	// the caller's intent even when a provider cannot offer an exact cap. The
	// historical investigation remains in docs/content/not-built-yet.md.
	MaxTokens int
}

// Validate checks the duration and rejects negative token declarations. Zero
// means the caller declared no token boundary.
func (l Limits) Validate() error {
	if l.MaxDuration <= 0 {
		return Fail(FailureInvalidInput,
			"limits: max duration must be positive, got %v", l.MaxDuration)
	}
	if l.MaxTokens < 0 {
		return Fail(FailureInvalidInput,
			"limits: max tokens cannot be negative, got %d", l.MaxTokens)
	}
	return nil
}

// Fits reports whether these limits are inside the parent's. A child may ask
// for less than its parent, never more.
//
// A parent that declared no token count constrains none, which is what zero
// now means. Read the other way -- zero as a ceiling of nothing -- a parent
// that simply did not declare would refuse every child that did.
func (l Limits) Fits(parent Limits) bool {
	if l.MaxDuration > parent.MaxDuration {
		return false
	}
	return parent.MaxTokens == 0 || l.MaxTokens <= parent.MaxTokens
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
	// Route is the model/tool/provider decision stamped onto this run. It is
	// optional for legacy direct agent calls, but present on decision-router
	// workflows.
	Route *Route
	// Context lists the levels this agent may read. A level here is a
	// permission, never a delivery: the agent asks and is served, and one it
	// never asks for costs nothing.
	Context []ContextLevel
	// Limits is the ceiling on time and tokens.
	Limits Limits
	// Effects are the consequences this agent is allowed to cause. A child
	// never holds one its parent did not, which is enforced in Child.
	Effects []Effect
	// BudgetUSD is the share of a grant this run is FORECAST to draw against.
	// Nil is not zero, and the difference is load-bearing: nil is "nobody
	// granted money here", which is every dispatch outside a workflow, and
	// zero is "you were considered and given none", which an agent that
	// spends must refuse rather than read as freedom.
	//
	// IT IS NOT A CEILING, and calling it one here was wrong until
	// 2026-08-16. Nothing in this system can stop a turn mid-flight. The one
	// control that reaches a provider is the CLI's --max-budget-usd, checked
	// BETWEEN messages, so what it bounds is the decision to start another
	// one; a turn already running spends what it spends. Measured on a
	// 23-step run: a $0.09 share spent $0.41 (4.6x), a $0.10 share spent
	// $0.41, and a $5.22 grant was charged $5.88 -- 12.6% over, with the
	// person who authorized $5.22 never asked about the rest. The only real
	// bound is one message's worth of output, roughly $3.86 at the rates
	// internal/allowance measures, which is a figure nobody chose and 43x
	// the smallest share that run used.
	//
	// So this is a forecast that a person approved, and the approval is real
	// -- Compile checks the shares against the grant before anything spawns,
	// and a plan whose steps are underfunded is refused. What the approval
	// cannot do is hold. A caller that needs a true ceiling does not have one
	// today; see docs/content/not-built-yet.md.
	//
	// It is here rather than in Limits because money is an authorization a
	// person gave, where time is a guard a caller sets. That distinction
	// survives this correction: both are ceilings only as far as something
	// enforces them, and neither is enforced by this struct.
	//
	// Nothing subdivides it in the agent tree today, because every dispatch
	// that carries money is a workflow step and the engine dispatches those
	// at the root. The day an agent hands money to a child, the narrowing
	// belongs in Child beside the other three.
	BudgetUSD *float64
	// CommissionUSD is the grant of the run this step belongs to -- what the
	// whole graph was authorized to spend, not what this step may draw.
	//
	// Two different figures, and conflating them was measured. An agent that
	// writes a graph has to divide the commission's grant; before this field
	// existed the shipped planner was handed its own BudgetUSD under the
	// name "the grant for the whole graph", so it divided its own allowance
	// and its plans came back the same size no matter what the commission
	// granted -- $0.87 of $10.00 on 2026-08-14, and the same $0.90 of $3.50
	// through eleven runs before it.
	//
	// Nil outside a workflow, where there is no run above this one and the
	// question has no answer. An agent that needs it and finds nil is being
	// told there is no commission, not that it was granted nothing.
	CommissionUSD *float64
	// Subject is another run's whole answer, handed to this one. Nil on
	// ordinary work. Two kinds of agent read it: a reviewer, whose job is to
	// judge it, and an agent whose work takes another's answer as its input
	// -- the shipped planner reads the exploration it plans from this way.
	Subject *Subject
	// Rejected is THIS agent's own previous attempt, refused by a review,
	// with ReviewID and Rejection saying which review and why. Nil on a
	// first attempt.
	//
	// Separate from Subject because they are two different things that were
	// one field until an agent had both: a planner relaunched after its
	// graph was refused needs the exploration it planned from AND the graph
	// that was refused. Folding the second into the first cost it the first,
	// and a planner handed only the complaint writes a new plan with a new
	// set of mistakes.
	Rejected *Subject
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
	if a.Subject != nil {
		if a.Subject.RunID == a.ID {
			return Fail(FailureInvalidInput,
				"assignment %s: reviews itself", a.ID)
		}
		if err := a.Subject.Validate(); err != nil {
			return Fail(FailureInvalidInput, "assignment %s: %s", a.ID, err.Error())
		}
	}
	if a.Rejected != nil {
		if err := a.Rejected.Validate(); err != nil {
			return Fail(FailureInvalidInput, "assignment %s: rejected: %s", a.ID, err.Error())
		}
		if strings.TrimSpace(a.Rejected.Rejection.Text) == "" {
			// The card exists to say what was wrong. Handing an agent its
			// own answer back with no sentence attached tells it only that
			// it failed, which is the retry that reruns the same mistake.
			return Fail(FailureInvalidInput,
				"assignment %s: a rejected attempt with no rejection to answer", a.ID)
		}
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
	if a.BudgetUSD != nil && *a.BudgetUSD < 0 {
		return Fail(FailureInvalidInput,
			"assignment %s: budget is negative ($%.2f)", a.ID, *a.BudgetUSD)
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
	if a.Route != nil {
		route := a.Route.Clone()
		a.Route = &route
	}
	if a.Subject != nil {
		subject := a.Subject.Clone()
		a.Subject = &subject
	}
	return a
}

// Subject is the run an agent has been asked to judge: what was asked, what
// came back, and how it ended.
//
// It exists because a reviewer needs the answer in front of it and cannot be
// told to go and find it. Handing the report over is also what keeps a review
// checkable: two reviewers given the same subject are looking at the same
// thing, and a disagreement between them is about judgement rather than about
// what they happened to read.
//
// Nil on an ordinary assignment. Present means "this run is about that run",
// which is a different relationship from parenthood -- a reviewer is
// dispatched by whoever dispatched the work, not by the work.
type Subject struct {
	// RunID is the execution being judged. It is what the trace row's
	// `reviews` column points at.
	RunID string
	// TypeName is the agent type that produced the answer.
	TypeName string
	// Task is what that run was asked, criterion included: a review with no
	// criterion is an opinion.
	Task Task
	// Attempt is which try this was, counting from 1, so a reviewer can see
	// it is looking at a relaunch.
	Attempt int
	// Result, Verdict and Reason are the report as it was validated. A
	// reviewer judges what the parent would have consumed, not a summary of
	// it.
	Result  map[string]any
	Verdict Verdict
	Reason  Reason
	// ReviewID and Rejection are set on the card in [Assignment.Rejected]:
	// which review refused the answer, and why. They are empty on a subject
	// handed to a reviewer -- it is judging, not reacting to a judgement.
	ReviewID  string
	Rejection Reason
}

// Validate checks the subject can be judged at all.
func (s Subject) Validate() error {
	if !slugID.MatchString(s.RunID) {
		return Fail(FailureInvalidInput, "subject: run id %q must be lowercase", s.RunID)
	}
	if !slugID.MatchString(s.TypeName) {
		return Fail(FailureInvalidInput,
			"subject %s: type name %q must be lowercase", s.RunID, s.TypeName)
	}
	if s.Verdict == VerdictUnspecified {
		return Fail(FailureInvalidInput,
			"subject %s: verdict is required; a review of a run with no verdict is a review of nothing",
			s.RunID)
	}
	if s.Attempt < 1 {
		return Fail(FailureInvalidInput,
			"subject %s: attempt starts at 1, got %d", s.RunID, s.Attempt)
	}
	if s.ReviewID != "" && !slugID.MatchString(s.ReviewID) {
		return Fail(FailureInvalidInput,
			"subject %s: review id %q must be lowercase", s.RunID, s.ReviewID)
	}
	if s.ReviewID != "" && strings.TrimSpace(s.Rejection.Text) == "" {
		// A rejection with no sentence is the shape this whole design is
		// against: the agent is told it failed and not told what failed.
		return Fail(FailureInvalidInput,
			"subject %s: review %s rejected it without saying why", s.RunID, s.ReviewID)
	}
	return s.Task.Validate()
}

// Clone returns a deep copy.
func (s Subject) Clone() Subject {
	s.Task = s.Task.Clone()
	if s.Result != nil {
		s.Result = maps.Clone(s.Result)
	}
	return s
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

// Charge is what one run of an agent cost, in the units the far side actually
// reported.
//
// Two fields and not one, because the two are not equally trustworthy. Tokens
// are counted by whoever served the request and are the same number whatever
// contract the account is on. A dollar figure is arithmetic somebody did with
// a price list, and on subscription traffic the price list is not what was
// billed -- measured on this machine, a proxy's own ledger and the billed
// figure disagreed by a wide margin for the same request. So USD is optional
// and never travels without PricedBy naming whose price produced it, and a
// reader that cannot say whose price it is looking at should show the tokens.
type Charge struct {
	// InputTokens and OutputTokens are what the turn consumed and produced.
	InputTokens  int
	OutputTokens int
	// CacheReadTokens and CacheWriteTokens are counted separately because
	// they are priced differently everywhere, and folding them into the
	// input count would make a warm repository look cheap rather than
	// cached.
	CacheReadTokens  int
	CacheWriteTokens int
	// USD is nil unless the far side priced the turn itself. Nil is not
	// zero: it means nobody said, and a run that cost real money must never
	// read as one that cost nothing.
	USD *float64
	// PricedBy names where USD came from -- which provider, which list, at
	// what moment. Required whenever USD is set, refused when it is not,
	// because a dollar figure with no provenance is the exact claim this
	// project keeps finding to be false.
	PricedBy string
}

// Measured reports whether this charge says anything at all. A zero Charge is
// what an agent that spends no tokens hands back, and it must read as
// unmeasured rather than as a free run.
func (c Charge) Measured() bool {
	return c.Tokens() > 0 || c.USD != nil
}

// Tokens is everything the turn moved. Cache reads are in it because they
// were paid for, even if cheaply.
func (c Charge) Tokens() int {
	return c.InputTokens + c.OutputTokens + c.CacheReadTokens + c.CacheWriteTokens
}

// Plus adds a charge to this one: what two runs of the same work cost
// together, which is what a person paying for a relaunch actually owes.
//
// The dollar figure survives only when both halves carry one. A priced turn
// added to an unpriced one is not the priced figure -- that would print the
// smaller number as the total, which is the same partial-measurement lie a
// half-measured workflow refuses to tell. Tokens always add: they are counted
// by whoever served the request and never guessed.
func (c Charge) Plus(o Charge) Charge {
	if !o.Measured() {
		return c
	}
	if !c.Measured() {
		return o
	}
	out := Charge{
		InputTokens:      c.InputTokens + o.InputTokens,
		OutputTokens:     c.OutputTokens + o.OutputTokens,
		CacheReadTokens:  c.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens + o.CacheWriteTokens,
	}
	if c.USD == nil || o.USD == nil {
		return out
	}
	total := *c.USD + *o.USD
	out.USD = &total
	out.PricedBy = c.PricedBy
	if o.PricedBy != c.PricedBy {
		// Two sources priced the two halves, and the sum belongs to both.
		// Naming one would attribute a figure to a source that never said
		// it.
		out.PricedBy = c.PricedBy + " and " + o.PricedBy
	}
	return out
}

// Validate refuses the two shapes that would put an unsupported number on a
// receipt: a negative count, and a price nobody will stand behind.
func (c Charge) Validate() error {
	for _, pair := range []struct {
		name  string
		value int
	}{
		{"input_tokens", c.InputTokens},
		{"output_tokens", c.OutputTokens},
		{"cache_read_tokens", c.CacheReadTokens},
		{"cache_write_tokens", c.CacheWriteTokens},
	} {
		if pair.value < 0 {
			return Fail(FailureInvalidInput, "charge: %s is negative (%d)", pair.name, pair.value)
		}
	}
	if c.USD == nil {
		if strings.TrimSpace(c.PricedBy) != "" {
			return Fail(FailureInvalidInput,
				"charge: priced_by is %q with no amount beside it", c.PricedBy)
		}
		return nil
	}
	if *c.USD < 0 {
		return Fail(FailureInvalidInput, "charge: amount is negative ($%.4f)", *c.USD)
	}
	if strings.TrimSpace(c.PricedBy) == "" {
		return Fail(FailureInvalidInput,
			"charge: $%.4f with no priced_by: a dollar figure has to say whose price it used", *c.USD)
	}
	return nil
}

// Report is what an agent hands back: four things with four destinations.
//
// The result goes to whoever asked, in the shape the agent type declared. The
// verdict is consumed by the reviewing parent. The discovered facts feed the
// history, so the next task does not pay to learn them again. The charge goes
// on the receipt.
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
	// Spent is what this run cost. The zero value means unmeasured, which is
	// the ordinary case for an agent that spends no tokens.
	Spent Charge
	// Completeness is how much of the objective this answer covers, from 0
	// (exclusive) to 1. Nil means unclaimed -- the ordinary case, and every
	// report accepted before this field existed.
	//
	// It lives on the report, not on whatever reads the report afterwards,
	// because completeness is a fact about the answer: how much of the
	// objective it actually covers. The agent that stopped three files into
	// five knows that number; a reviewer or a caller working only from the
	// result would have to guess it, which makes it a measurement, not a
	// judgement, and measurements belong with what they measure.
	//
	// Partial is deliberately still VerdictOK rather than a fifth verdict.
	// The verdict slot answers one question -- did anybody look at this --
	// and every consumer branches on that alone: the reviewer gate `on =
	// "answered"`, retry logic, receipts. A verdict that meant "answered,
	// but not all of it" would put a second question in a slot built for
	// one, and every place that already treats VerdictOK as "there is
	// something here to judge" would start treating answered work as if
	// nobody had answered.
	//
	// Nil rather than a bare float so "never measured" and "measured at
	// zero" stay distinguishable -- the same reason Charge.USD is a
	// pointer. A zero value read as absent would silently pass off an
	// unmeasured report as one that covered nothing at all.
	Completeness *float64
	// StoppedAt is what this answer did not reach. Required whenever
	// Completeness is below 1 -- see validateCompleteness -- because a
	// completeness number with nothing named beside it is not actionable: a
	// caller reading "0.55" cannot resume, retry, or even describe the run
	// to a person without knowing which part of the objective is missing.
	StoppedAt string
}

// Partial reports whether this answer covers less than the whole objective.
// A report with no Completeness claim at all is not partial -- it never
// measured itself, which is a different fact from measuring itself short.
func (r Report) Partial() bool {
	return r.Completeness != nil && *r.Completeness < 1
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
	if err := r.Spent.Validate(); err != nil {
		return err
	}
	if err := r.validateReason(); err != nil {
		return err
	}
	if err := r.validateCompleteness(); err != nil {
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

// validateCompleteness enforces the rules tying a completeness claim to the
// rest of the report.
//
// The range is (0, 1]: zero is not "no progress", it is nothing to claim --
// an agent with nothing to show reports that through Verdict and Reason,
// the same as it always has, and a Completeness of zero would just be a
// second, contradicting way to say the same thing. A claim below 1 has to
// name where it stopped, for the same reason validateReason makes a reason
// mandatory: a number with nothing beside it cannot be acted on.
//
// A claim only stands on VerdictOK. VerdictIncomplete already exists for
// "nobody obtained an answer to judge"; Completeness is the opposite fact --
// somebody did, just not all of it -- and letting it ride on any other
// verdict would blur the one distinction VerdictIncomplete exists to draw.
func (r Report) validateCompleteness() error {
	if r.Completeness == nil {
		return nil
	}
	if r.Verdict != VerdictOK {
		return Fail(FailureInvalidInput,
			"report: completeness is set on a %s report; only an ok verdict can be partial", r.Verdict)
	}
	c := *r.Completeness
	if c <= 0 || c > 1 {
		return Fail(FailureInvalidInput,
			"report: completeness %v is out of range, want (0, 1]", c)
	}
	if c < 1 && strings.TrimSpace(r.StoppedAt) == "" {
		return Fail(FailureInvalidInput,
			"report: completeness %v below 1 requires stopped_at to say what was not reached", c)
	}
	return nil
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
	// StoppedAt is prose a model wrote, and prose comes with the whitespace
	// a model leaves around it. Trimmed here rather than at the boundary
	// that checks it, so a caller reading the field after Normalize sees
	// the same value the emptiness check saw.
	out.StoppedAt = strings.TrimSpace(out.StoppedAt)
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

// Subject turns this report into the case handed to whoever reads it next:
// a reviewer auditing it, or the agent itself on a relaunch.
//
// One constructor, because there are two callers -- `atenea agent --review`
// and a workflow's subject edge -- and a subject assembled twice is two
// different reviews of one answer, decided by which door the caller came
// through.
//
// The whole validated report travels, never a projection of it. A subject
// narrowed to the interesting fields is a summary, and a review of a summary
// is a review of something the parent never consumed.
func (r Report) Subject(runID, typeName string, attempt int, work Task) Subject {
	return Subject{
		RunID:    runID,
		TypeName: typeName,
		Task:     work,
		Attempt:  attempt,
		Result:   r.Result,
		Verdict:  r.Verdict,
		Reason:   r.Reason,
	}
}

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
	if r.Spent.USD != nil {
		// The pointer is what distinguishes "unmeasured" from "$0.00", so a
		// shallow copy would let two reports share the one number that must
		// never be edited in place.
		amount := *r.Spent.USD
		out.Spent.USD = &amount
	}
	if r.Completeness != nil {
		// Same reasoning as Spent.USD above: the pointer is what makes
		// "unclaimed" and "claimed at some value" different facts, so the
		// copy needs its own float rather than the original's address.
		value := *r.Completeness
		out.Completeness = &value
	}
	return out
}
