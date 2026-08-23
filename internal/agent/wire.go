package agent

import (
	"bytes"
	"encoding/json"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The wire is written out by hand rather than by marshaling the contract
// structs directly, and that is deliberate.
//
// An agent is a separate program, often not written in Go and often not
// written by us. What crosses the pipe is the thing it has to parse, so it has
// to be readable by a person implementing one: names, not enum ordinals. Half
// the contract enums are bare uint8 with no JSON form, so marshaling them
// directly would put `"kind":2` on the wire -- a number whose meaning lives in
// the order of a Go const block, which is not a contract anybody outside this
// repository could keep.
//
// It also puts a seam here on purpose. Renaming a Go field is then a change
// Atenea absorbs, instead of one that silently breaks every agent on the
// machine.

// assignmentWire is what an agent reads on stdin: one JSON object, then EOF.
type assignmentWire struct {
	Contract string     `json:"contract"`
	ID       string     `json:"id"`
	ParentID string     `json:"parent_id,omitempty"`
	Kind     string     `json:"kind"`
	Type     string     `json:"type"`
	Depth    int        `json:"depth"`
	Task     taskWire   `json:"task"`
	Limits   limitsWire `json:"limits"`
	Effects  []string   `json:"effects"`
	// BudgetUSD is what this run may spend. Absent when nobody granted
	// money, which a model-backed agent has to be able to tell from a grant
	// of zero: the first means run without a ceiling of your own, and the
	// second means do not spend.
	BudgetUSD *float64 `json:"budget_usd,omitempty"`
	// CommissionUSD is the grant of the run this step belongs to, present
	// only inside a workflow. An agent writing a graph divides this figure;
	// BudgetUSD above is its own share and dividing that was the bug this
	// field exists to end.
	CommissionUSD *float64 `json:"commission_usd,omitempty"`
	// Context carries only the levels the type declared, keyed by level name.
	// A level that was not declared is absent, not empty: absent says nobody
	// offered it, and empty would say it was offered and had nothing in it.
	Context map[string]any `json:"context"`
	// ResultSchema is the shape this answer will be judged against, sent so
	// the agent can be told rather than having to know. A model agent needs
	// it in the prompt; a scripted one can ignore it.
	ResultSchema map[string]any `json:"result_schema"`
	Route        *routeWire     `json:"route,omitempty"`
	// Subject is the run this agent was asked to judge, absent on ordinary
	// work. A reviewer reads its whole case from here: what was asked, what
	// came back, and which attempt it was.
	Subject *subjectWire `json:"subject,omitempty"`
	// Rejected is this agent's own refused attempt, present only on a
	// relaunch. Same shape as subject: a whole report somebody else read.
	Rejected *subjectWire `json:"rejected,omitempty"`
}

type routeWire struct {
	Model        string            `json:"model,omitempty"`
	Fallbacks    []string          `json:"fallbacks,omitempty"`
	Backend      string            `json:"backend,omitempty"`
	Binary       string            `json:"binary,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Providers    map[string]string `json:"providers,omitempty"`
	Tools        []string          `json:"tools,omitempty"`
}

type taskWire struct {
	Objective string   `json:"objective"`
	Files     []string `json:"files,omitempty"`
	Criterion string   `json:"criterion"`
}

type subjectWire struct {
	RunID   string         `json:"run_id"`
	Type    string         `json:"type"`
	Attempt int            `json:"attempt"`
	Task    taskWire       `json:"task"`
	Result  map[string]any `json:"result,omitempty"`
	Verdict string         `json:"verdict"`
	Reason  *reasonWire    `json:"reason,omitempty"`
	// Set only on a relaunch: the review that refused this answer, and the
	// sentence it refused it with.
	ReviewID  string      `json:"review_id,omitempty"`
	Rejection *reasonWire `json:"rejection,omitempty"`
}

type limitsWire struct {
	MaxSeconds float64 `json:"max_seconds"`
	MaxTokens  int     `json:"max_tokens"`
}

// reportWire is what an agent writes on stdout: one JSON object.
type reportWire struct {
	Result     map[string]any  `json:"result"`
	Verdict    string          `json:"verdict"`
	Reason     *reasonWire     `json:"reason,omitempty"`
	Discovered []discoveryWire `json:"discovered,omitempty"`
	// Notices are caveats about the report, distinct from Reason: a
	// truncated discovery or a partial answer's own account of itself, not
	// why the run ended the way it did.
	Notices []string `json:"notices,omitempty"`
	// Completeness and StoppedAt travel together: absent on an agent that
	// never measured itself, which is every agent before this pair existed.
	// See contract.Report for why the claim is a pointer and why it needs a
	// stop point.
	Completeness *float64 `json:"completeness,omitempty"`
	StoppedAt    string   `json:"stopped_at,omitempty"`
	// Spent is absent on the agents that spend nothing, which is what
	// unmeasured looks like on the wire. An agent writing `"spent": {}` says
	// the same thing: a charge nobody filled in.
	Spent *chargeWire `json:"spent,omitempty"`
}

// chargeWire is what a far side reports about its own cost.
//
// The dollar field is a pointer for the reason the column is: absent and zero
// have to stay different, because a turn that really was free and a turn
// nobody priced are not the same fact.
type chargeWire struct {
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	CacheReadTokens  int      `json:"cache_read_tokens"`
	CacheWriteTokens int      `json:"cache_write_tokens"`
	USD              *float64 `json:"usd,omitempty"`
	PricedBy         string   `json:"priced_by,omitempty"`
}

type reasonWire struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type discoveryWire struct {
	Level string `json:"level"`
	Note  string `json:"note"`
}

func encodeAssignment(a contract.Assignment, ctxPayload map[string]any,
	schema map[string]any) ([]byte, error) {
	out := assignmentWire{
		Contract: a.Version.String(),
		ID:       a.ID,
		ParentID: a.ParentID,
		Kind:     a.Kind.String(),
		Type:     a.TypeName,
		Depth:    a.Depth,
		Task: taskWire{
			Objective: a.Task.Objective,
			Files:     a.Task.Files,
			Criterion: a.Task.Criterion,
		},
		Limits: limitsWire{
			MaxSeconds: a.Limits.MaxDuration.Seconds(),
			MaxTokens:  a.Limits.MaxTokens,
		},
		Context:      ctxPayload,
		ResultSchema: schema,
	}
	if a.Route != nil {
		route := a.Route.Clone()
		out.Route = &routeWire{Model: route.Model, Fallbacks: route.Fallbacks, Backend: route.Backend, Binary: route.Binary,
			Capabilities: route.Capabilities, Providers: route.Providers, Tools: route.Tools}
	}
	if a.BudgetUSD != nil {
		budget := *a.BudgetUSD
		out.BudgetUSD = &budget
	}
	if a.CommissionUSD != nil {
		commission := *a.CommissionUSD
		out.CommissionUSD = &commission
	}
	out.Subject = encodeSubject(a.Subject)
	out.Rejected = encodeSubject(a.Rejected)
	for _, effect := range a.Effects {
		out.Effects = append(out.Effects, effect.String())
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"agent %s: encoding the assignment: %v", a.ID, err)
	}
	return append(payload, '\n'), nil
}

// decodeReport reads what the agent wrote.
//
// Anything unreadable here is a DEATH, not a failed run: the agent did not
// answer, and a caller cannot tell an agent that answered badly from one that
// never answered by looking at a parse error. The distinction is made by the
// caller, which is why this returns a plain error and takes no position.
// encodeSubject writes one card. Both `subject` and `rejected` are the same
// shape on the wire because they are the same thing seen from two sides: a
// whole validated report somebody else already read. What differs is whose
// it is, and that is said by which key it arrives under, not by a second
// shape an agent would have to learn twice.
func encodeSubject(s *contract.Subject) *subjectWire {
	if s == nil {
		return nil
	}
	out := &subjectWire{
		RunID:   s.RunID,
		Type:    s.TypeName,
		Attempt: s.Attempt,
		Task: taskWire{
			Objective: s.Task.Objective,
			Files:     s.Task.Files,
			Criterion: s.Task.Criterion,
		},
		Result:  s.Result,
		Verdict: s.Verdict.String(),
	}
	// The reason travels whenever there is one. A reviewer told only that
	// the run came back `incomplete` has to guess at what stopped it, and
	// guessing is the one thing a review may not do.
	if s.Reason.Kind != contract.FailureUnspecified || s.Reason.Text != "" {
		out.Reason = &reasonWire{Kind: s.Reason.Kind.String(), Text: s.Reason.Text}
	}
	if s.ReviewID != "" {
		out.ReviewID = s.ReviewID
		out.Rejection = &reasonWire{Kind: s.Rejection.Kind.String(), Text: s.Rejection.Text}
	}
	return out
}

func decodeReport(raw []byte) (contract.Report, error) {
	var wire reportWire
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	// An agent that sent a field nobody declared has misread the contract,
	// and accepting it quietly is how the two drift apart.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return contract.Report{}, contract.Fail(contract.FailureInvalidInput,
			"unreadable report: %v", err)
	}
	verdict, err := contract.ParseVerdict(wire.Verdict)
	if err != nil {
		return contract.Report{}, contract.Fail(contract.FailureInvalidInput,
			"unreadable report: %v", err)
	}
	out := contract.Report{
		Result: wire.Result, Verdict: verdict,
		Notices: wire.Notices, StoppedAt: wire.StoppedAt,
	}
	if wire.Completeness != nil {
		amount := *wire.Completeness
		out.Completeness = &amount
	}
	if wire.Reason != nil {
		kind, err := contract.ParseFailureKind(wire.Reason.Kind)
		if err != nil {
			return contract.Report{}, contract.Fail(contract.FailureInvalidInput,
				"unreadable report: %v", err)
		}
		out.Reason = contract.Reason{Kind: kind, Text: wire.Reason.Text}
	}
	for _, d := range wire.Discovered {
		level, err := contract.ParseContextLevel(d.Level)
		if err != nil {
			return contract.Report{}, contract.Fail(contract.FailureInvalidInput,
				"unreadable report: %v", err)
		}
		out.Discovered = append(out.Discovered, contract.Discovery{Level: level, Note: d.Note})
	}
	if c := wire.Spent; c != nil {
		out.Spent = contract.Charge{
			InputTokens:      c.InputTokens,
			OutputTokens:     c.OutputTokens,
			CacheReadTokens:  c.CacheReadTokens,
			CacheWriteTokens: c.CacheWriteTokens,
			PricedBy:         c.PricedBy,
		}
		if c.USD != nil {
			amount := *c.USD
			out.Spent.USD = &amount
		}
	}
	return out, nil
}
