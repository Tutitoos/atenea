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
	// Context carries only the levels the type declared, keyed by level name.
	// A level that was not declared is absent, not empty: absent says nobody
	// offered it, and empty would say it was offered and had nothing in it.
	Context map[string]any `json:"context"`
	// ResultSchema is the shape this answer will be judged against, sent so
	// the agent can be told rather than having to know. A model agent needs
	// it in the prompt; a scripted one can ignore it.
	ResultSchema map[string]any `json:"result_schema"`
}

type taskWire struct {
	Objective string   `json:"objective"`
	Files     []string `json:"files,omitempty"`
	Criterion string   `json:"criterion"`
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
	out := contract.Report{Result: wire.Result, Verdict: verdict}
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
	return out, nil
}
