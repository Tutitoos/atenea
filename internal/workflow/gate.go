package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// MaxExpansions is how many times one run may grow.
//
// A cap rather than a budget line, because the two run out for different
// reasons and a reader deserves to know which. Three is small on purpose: a
// graph that needed a fourth round of replanning is a graph whose first plan
// was a guess, and the honest move there is a new commission rather than a
// deeper one.
const MaxExpansions = 3

// Kind is what a gate asks for.
//
// Launching and approving are the same mechanism -- a person reading a plan
// and letting it run -- and different acts. Launching commits a grant that
// nothing had claimed; approving an expansion extends one already in flight.
// Reading a gate log should not require counting ordinals to tell them apart.
type Kind uint8

// The three things a gate can ask.
const (
	// KindLaunch is the first gate on every run: the plan as created, before
	// anything spawns.
	KindLaunch Kind = iota
	// KindApprove is an expansion of a run already going.
	KindApprove
	// KindRedo is a step being dispatched again at a share somebody raised.
	//
	// It is its own kind because it is the only one that authorizes money
	// against work whose cost is already known. A launch and an expansion
	// both bless an estimate; a redo blesses a second attempt at a step that
	// has already been measured failing, and the figure it raises is the
	// answer to what that step actually needed. A reader totalling what a run
	// was allowed to spend has to be able to tell the third from the first
	// two without diffing shares by hand.
	KindRedo
)

var kindNames = map[Kind]string{
	KindLaunch: "launch", KindApprove: "approve", KindRedo: "redo",
}

func (k Kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// ParseKind reads a kind name back off the record.
func ParseKind(s string) (Kind, error) {
	for kind, name := range kindNames {
		if name == s {
			return kind, nil
		}
	}
	return KindLaunch, contract.Fail(contract.FailureInvalidInput,
		"unknown gate kind %q", s)
}

// Decision is how a gate was answered, or that it has not been.
type Decision uint8

// The three states of a gate.
const (
	// DecisionWaiting is a gate nobody has answered. It stays this way
	// indefinitely: nothing here times out, because a question that expires
	// into a default is not a question.
	DecisionWaiting Decision = iota
	DecisionApproved
	DecisionRejected
)

var decisionNames = map[Decision]string{
	DecisionWaiting:  "waiting",
	DecisionApproved: "approved",
	DecisionRejected: "rejected",
}

func (d Decision) String() string {
	if name, ok := decisionNames[d]; ok {
		return name
	}
	return "decision(" + strconv.Itoa(int(d)) + ")"
}

// ParseDecision reads a decision name back off the record.
func ParseDecision(s string) (Decision, error) {
	for decision, name := range decisionNames {
		if name == s {
			return decision, nil
		}
	}
	return DecisionWaiting, contract.Fail(contract.FailureInvalidInput,
		"unknown gate decision %q", s)
}

// Proposal is what a gate asks about: steps to add, and the started-nothing
// steps they replace.
//
// On a launch it is the whole graph and replaces nothing. On an expansion it
// is the new work, plus whichever pending steps the new work supersedes.
type Proposal struct {
	// Steps are the steps this proposal puts into the graph.
	Steps []Step
	// Replaces names steps already in the graph that this one removes.
	//
	// Only steps that have not STARTED may appear here -- see [Store.Ask],
	// which refuses the rest. Not "not executed": a step that is running has
	// begun to touch the world, and replanning it would be a decision about
	// work already underway.
	Replaces []string
}

// Clone returns a deep copy.
func (p Proposal) Clone() Proposal {
	p.Steps = slices.Clone(p.Steps)
	for i, step := range p.Steps {
		p.Steps[i] = step.Clone()
	}
	p.Replaces = slices.Clone(p.Replaces)
	return p
}

// Digest is what an approval is an approval OF.
//
// An approval names an artifact rather than a moment. The engine recomputes
// this over what it is about to apply, immediately before applying it, and
// refuses on any difference -- so a plan that changed between the reading and
// the running cannot run on the strength of having once been read.
//
// The freeze is what stops that arising at all: while a gate is open nothing
// new is dispatched, and a proposal may only touch steps that have not
// started, so no step named here can change state while somebody reads it.
// This digest is the check on the freeze, not a substitute for it.
func (p Proposal) Digest() string {
	var b strings.Builder
	// A fixed field order, written out by hand. Marshaling a struct would
	// tie the digest to Go's field order, and reordering a declaration is
	// exactly the kind of edit nobody expects to invalidate an approval.
	for _, step := range p.Steps {
		fmt.Fprintf(&b, "step\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%.6f\x00%.6f\x00%.6f\x00%s\x00",
			step.ID, step.TypeName, step.Task.Objective, step.Task.Criterion,
			step.Subject, step.On, step.Permission.BudgetUSD,
			step.BudgetEstimateUSD, step.BudgetMinimumUSD, step.BudgetSource)
		for _, file := range step.Task.Files {
			fmt.Fprintf(&b, "file\x00%s\x00", file)
		}
		for _, need := range step.Needs {
			fmt.Fprintf(&b, "need\x00%s\x00", need)
		}
		for _, effect := range step.Permission.Effects {
			fmt.Fprintf(&b, "effect\x00%s\x00", effect)
		}
		if step.Route != nil {
			fmt.Fprintf(&b, "route\x00%s\x00", jsonRoute(step.Route))
		}
	}
	// Sorted, because which order a caller listed the replaced steps in is
	// not something a reader agreed to.
	replaces := slices.Clone(p.Replaces)
	sort.Strings(replaces)
	for _, id := range replaces {
		fmt.Fprintf(&b, "replaces\x00%s\x00", id)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Short is the digest as a person reads it back: enough to compare two
// printed plans by eye, never enough to be mistaken for the whole.
func Short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

// AllocatedUSD is what this proposal's steps claim of the grant.
//
// Allocated, never spent. Nothing on this machine can report a charge, so a
// number called spend here would be a measurement nobody took -- see
// [StepRow.Spent], which reads unmeasured for the same reason.
func (p Proposal) AllocatedUSD() float64 {
	var out float64
	for _, step := range p.Steps {
		out += step.Permission.BudgetUSD
	}
	return out
}

// Gate is one question put to a person, and its answer.
type Gate struct {
	// applied is nil only for gates written before application was recorded.
	applied *bool
	RunID   string
	// Ordinal counts gates within a run from 0, which is always the launch.
	Ordinal  int
	Kind     Kind
	Proposal Proposal
	// Digest is of the proposal as it was ASKED. The approval binds to it.
	Digest   string
	Decision Decision
	Asked    time.Time
	Answered time.Time
	// Hand is who answered, as far as this machine can tell: the operating
	// system user, and the surface the answer arrived through.
	//
	// Not an identity. Nothing here authenticates anybody, and a field named
	// for a person that holds only $USER would be a claim backed by nothing
	// -- the same shape as a receipt reporting a spend nobody measured. A
	// real identity needs something that verifies one, and there is no such
	// thing in Atenea today.
	Hand string
	// Reason is why a rejection was a rejection. Required on one, because a
	// refusal with no sentence tells the next reader that something was
	// turned down and nothing about what to do instead.
	Reason string
}

// Waiting reports whether this gate is still a question.
func (g Gate) Waiting() bool { return g.Decision == DecisionWaiting }

// Hand describes the answering party as well as this machine is able to.
//
// Read the doc comment on [Gate.Hand] before using this for anything that
// matters: it is a description, not a credential.
func Hand(surface string) string {
	name := "unknown"
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		name = u.Username
	} else if env := strings.TrimSpace(os.Getenv("USER")); env != "" {
		name = env
	}
	return name + " via " + surface
}
