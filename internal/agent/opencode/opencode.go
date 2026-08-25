// Package opencode is the isolated model runner for OpenCode's headless CLI.
//
// OpenCode's JSON mode is an event stream, not Claude Code's final envelope.
// This package keeps that protocol separate and accepts a turn only after a
// completed step_finish event and a non-empty text event have been observed.
// Missing terminal events are failures, never successful partial answers.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	// DefaultBinary is the command used when no OpenCode binary is configured.
	DefaultBinary = "opencode"
	// DefaultTimeout is the fallback ceiling for one OpenCode turn.
	DefaultTimeout = 5 * time.Minute
	// maxOpenCodeToolSteps prevents a provider from spending the whole turn
	// retrying unavailable capabilities. Once this many intermediate tool
	// steps have finished, the runner resumes the same session with a
	// tool-free finalization request and accepts the partial answer honestly.
	maxOpenCodeToolSteps = 4
)

// Options configures the OpenCode executable.
type Options struct {
	Binary  string
	Timeout time.Duration
}

// Request is one OpenCode model turn.
type Request struct {
	Model     string
	Prompt    string
	Dir       string
	BudgetUSD float64
	// MaxTokens is the assignment's declared total token ceiling. OpenCode
	// has no native flag for it, so the adapter refuses a completed answer
	// whose reported usage crosses it. This cannot undo provider work already
	// in flight, but it prevents an over-limit answer from being accepted.
	MaxTokens  int
	ReadTokens int
	Tools      string
	Schema     map[string]any
	// Timeout is the ceiling on this one turn, finalization pass included.
	// Zero takes the runner's own, which is the one the client was built
	// with.
	//
	// It is a field on the request because the caller's deadline is not
	// always the runner's. model.Request.Timeout may be longer than the
	// client's -- a plan turn given room a search turn does not need -- and
	// before this existed the runner re-wrapped the caller's already-bounded
	// context with its own smaller ceiling, so the larger deadline was
	// silently cut and the timeout message named a limit that had not
	// expired.
	Timeout time.Duration
}

// Answer is the provider-neutral result needed by the model client.
type Answer struct {
	Text       string
	Structured json.RawMessage
	Spent      contract.Charge
	Passes     int
	// ToolCalls is diagnostic evidence from the JSON event stream. It is kept
	// separate from Text because a model can claim to have used a tool without
	// OpenCode ever emitting a tool_use event.
	ToolCalls []string
}

// Runner invokes OpenCode without inheriting an interactive TUI or answering
// permission prompts on the operator's behalf.
type Runner struct {
	binary  string
	timeout time.Duration
	version *toolversion.Probe
}

// New constructs a runner. Missing binaries are discovered lazily on Run.
func New(opts Options) (*Runner, error) {
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"opencode adapter: timeout must not be negative, got %s", opts.Timeout)
	}
	binary := strings.TrimSpace(opts.Binary)
	if binary == "" {
		binary = DefaultBinary
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &Runner{
		binary:  binary,
		timeout: timeout,
		version: toolversion.New(binary, "--version"),
	}, nil
}

// limitFor is the ceiling this turn actually runs under: the request's own
// when it names one, and the runner's otherwise.
//
// A caller with a longer deadline than the runner's default used to lose it
// here, and the failure it got back named the runner's figure rather than the
// one that expired -- which sends whoever reads the report looking for a
// timeout that never fired.
func (r *Runner) limitFor(req Request) time.Duration {
	if req.Timeout > 0 {
		return req.Timeout
	}
	return r.timeout
}

// Version returns the CLI's first version token, or empty when it cannot be
// queried.
func (r *Runner) Version(ctx context.Context) string { return firstToken(r.version.Version(ctx)) }

// Run executes one non-interactive JSON event stream.
func (r *Runner) Run(ctx context.Context, req Request) (Answer, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return Answer{}, contract.Fail(contract.FailureInvalidInput, "opencode request: prompt is required")
	}
	if req.BudgetUSD < 0 || math.IsNaN(req.BudgetUSD) || math.IsInf(req.BudgetUSD, 0) {
		return Answer{}, contract.Fail(contract.FailureInvalidInput,
			"opencode request: budget_usd must be finite and non-negative, got %v", req.BudgetUSD)
	}
	if req.ReadTokens < 0 {
		return Answer{}, contract.Fail(contract.FailureInvalidInput,
			"opencode request: read_tokens must not be negative, got %d", req.ReadTokens)
	}
	if req.MaxTokens < 0 {
		return Answer{}, contract.Fail(contract.FailureInvalidInput,
			"opencode request: max_tokens must not be negative, got %d", req.MaxTokens)
	}
	if req.Timeout < 0 {
		return Answer{}, contract.Fail(contract.FailureInvalidInput,
			"opencode request: timeout must not be negative, got %s", req.Timeout)
	}
	if req.Dir != "" {
		abs, err := filepath.Abs(req.Dir)
		if err != nil {
			return Answer{}, contract.Fail(contract.FailureInvalidInput, "opencode request: dir %q: %v", req.Dir, err)
		}
		req.Dir = abs
	}

	prompt, err := promptWithSchema(req.Prompt, req.Schema)
	if err != nil {
		return Answer{}, err
	}
	// Do not pass --pure. The current OpenCode server rejects pure mode for
	// authenticated provider sessions, even when no MCP config is injected.
	// Tool access remains isolated by the explicit MCP config below, and plan
	// turns receive no config at all.
	argv := []string{"run", "--format", "json"}
	if req.Dir != "" {
		argv = append(argv, "--dir", req.Dir)
	}
	if strings.TrimSpace(req.Model) != "" {
		argv = append(argv, "--model", req.Model)
	}
	argv = append(argv, prompt)

	binary, err := exec.LookPath(r.binary)
	if err != nil {
		return Answer{}, contract.Fail(contract.FailureUnavailable,
			"opencode is not installed: %q is not on PATH", r.binary)
	}
	limit := r.limitFor(req)
	turnCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(turnCtx, binary, argv...)
	cmd.Dir = req.Dir
	procgroup.Contain(cmd)
	if strings.TrimSpace(req.Tools) != "" {
		content, err := openCodeConfig(req.Tools)
		if err != nil {
			return Answer{}, err
		}
		cmd.Env = appendWithout(os.Environ(), "OPENCODE_CONFIG_CONTENT", content)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Answer{}, contract.Fail(contract.FailureUnavailable, "opencode stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Answer{}, failureFor(strings.TrimSpace(stderr.String()), err)
	}

	var stream eventStream
	var limitErr error
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := stream.accept(scanner.Bytes()); err != nil {
			// The charge, on this path too. An event this adapter cannot read
			// says nothing about the steps that arrived before it, and those
			// steps carried real tokens and a real cost: every other failing
			// return below reports them, and dropping them here is how a
			// baseline learns that a turn killed by one malformed line was
			// free.
			_ = procgroup.Kill(cmd)
			_ = cmd.Wait()
			return Answer{Spent: stream.charge()}, err
		}
		if limitErr = stream.limitFailure(req); limitErr != nil {
			// The provider has no native budget or token flag. Once an event
			// gives us an observed excess, stop the process immediately so no
			// later event can add work after the local boundary. This still
			// cannot undo provider work already in flight, but it is stronger
			// than waiting for the terminal event to reject the answer.
			stream.stopped = true
			_ = procgroup.Kill(cmd)
			break
		}
		if stream.reached(req.ReadTokens) || stream.needsFinalization() {
			// OpenCode has no stdin finalize protocol. Stop only after a
			// completed step, retaining the session id so a second, tool-free
			// turn can ask for the structured answer. This is an observed
			// boundary, never an exact provider cap.
			stream.stopped = true
			_ = procgroup.Kill(cmd)
			break
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_ = procgroup.Kill(cmd)
	}
	waitErr := cmd.Wait()
	if ctxErr := turnCtx.Err(); ctxErr != nil {
		return Answer{}, contract.Stopped(ctxErr, "opencode", limit).WithRaw(strings.TrimSpace(stderr.String()))
	}
	if limitErr != nil {
		return Answer{Spent: stream.charge()}, limitErr
	}
	if scanErr != nil {
		// Same reason as the malformed-event path above: a stream that broke
		// mid-turn still billed for the steps it did deliver.
		return Answer{Spent: stream.charge()}, contract.Fail(contract.FailureUnavailable,
			"opencode event stream could not be read: %v", scanErr).WithRaw(strings.TrimSpace(stderr.String()))
	}
	if stream.errText != "" {
		return Answer{Spent: stream.charge()}, failureFor(stream.errText, waitErr)
	}
	if waitErr != nil && !stream.stopped {
		return Answer{Spent: stream.charge()}, failureFor(strings.TrimSpace(stderr.String()), waitErr)
	}
	charge := stream.charge()
	text := strings.TrimSpace(stream.text.String())
	passes := 1
	toolCalls := append([]string(nil), stream.toolCalls...)
	if !stream.finished && stream.stopped && stream.sessionID != "" {
		finalStream, finalErr := r.finalize(turnCtx, req, stream.sessionID)
		charge = charge.Plus(finalStream.charge())
		if finalErr != nil {
			return Answer{Spent: charge}, finalErr
		}
		stream = finalStream
		text = strings.TrimSpace(stream.text.String())
		toolCalls = append(toolCalls, stream.toolCalls...)
		passes++
	}
	if !stream.finished {
		return Answer{Spent: stream.charge()}, contract.Fail(contract.FailureUnavailable,
			"opencode ended without a step_finish event").WithRaw(strings.TrimSpace(stderr.String()))
	}
	if text == "" {
		return Answer{Spent: charge}, contract.Fail(contract.FailureUnavailable,
			"opencode completed a step without text output").WithRaw(strings.TrimSpace(stderr.String()))
	}
	answer := Answer{Text: text, Spent: charge, Passes: passes, ToolCalls: toolCalls}
	if len(req.Schema) > 0 {
		structured, err := structuredObject(text, req.Schema)
		if err != nil {
			return answer, err
		}
		answer.Structured = structured
	}
	return answer, nil
}

// finalize resumes a session that spent its read allowance on intermediate
// tool turns. OpenCode persists the session, so the final answer can be
// requested without repeating the repository context or the work already
// paid for. The prompt deliberately has no permission to call another tool.
func (r *Runner) finalize(ctx context.Context, req Request, sessionID string) (eventStream, error) {
	var stream eventStream
	prompt, err := finalizationPrompt(req.Schema)
	if err != nil {
		return stream, err
	}
	argv := []string{"run", "--format", "json", "--session", sessionID}
	// Finalization is deliberately tool-free by prompt. Do not re-inject MCP
	// config: the persisted session already has its context and the model must
	// close from the evidence it collected.
	if req.Dir != "" {
		argv = append(argv, "--dir", req.Dir)
	}
	argv = append(argv, prompt)

	binary, err := exec.LookPath(r.binary)
	if err != nil {
		return stream, contract.Fail(contract.FailureUnavailable,
			"opencode is not installed: %q is not on PATH", r.binary)
	}
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = req.Dir
	procgroup.Contain(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return stream, contract.Fail(contract.FailureUnavailable, "opencode stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return stream, failureFor(strings.TrimSpace(stderr.String()), err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := stream.accept(scanner.Bytes()); err != nil {
			_ = procgroup.Kill(cmd)
			_ = cmd.Wait()
			return stream, err
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_ = procgroup.Kill(cmd)
	}
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stream, contract.Stopped(ctxErr, "opencode", r.limitFor(req)).WithRaw(strings.TrimSpace(stderr.String()))
	}
	if scanErr != nil {
		return stream, contract.Fail(contract.FailureUnavailable,
			"opencode event stream could not be read: %v", scanErr).WithRaw(strings.TrimSpace(stderr.String()))
	}
	if stream.errText != "" {
		return stream, failureFor(stream.errText, waitErr)
	}
	if waitErr != nil {
		return stream, failureFor(strings.TrimSpace(stderr.String()), waitErr)
	}
	if !stream.finished {
		return stream, contract.Fail(contract.FailureUnavailable,
			"opencode finalization ended without a step_finish event").WithRaw(strings.TrimSpace(stderr.String()))
	}
	return stream, nil
}

type eventStream struct {
	text         strings.Builder
	textIDs      map[string]struct{}
	finished     bool
	stopped      bool
	sessionID    string
	stepFinishes int
	errText      string
	usage        contract.Charge
	cost         float64
	costSeen     bool
	toolCalls    []string
}

type event struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"sessionID"`
	Part       json.RawMessage `json:"part"`
	Error      json.RawMessage `json:"error"`
	Properties json.RawMessage `json:"properties"`
}

type part struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
	Time   struct {
		End *int64 `json:"end"`
	} `json:"time"`
	Tokens *struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Cost *float64 `json:"cost"`
}

func (s *eventStream) accept(raw []byte) error {
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"opencode emitted invalid JSON event: %v", err).WithRaw(string(raw))
	}
	if ev.SessionID != "" {
		s.sessionID = ev.SessionID
	}
	switch ev.Type {
	case "text":
		p, err := decodePart(ev.Part, "text", raw)
		if err != nil {
			return err
		}
		if p.Text == "" || (p.Time.End == nil && p.ID != "") {
			return nil
		}
		if s.textIDs == nil {
			s.textIDs = make(map[string]struct{})
		}
		if p.ID != "" {
			if _, seen := s.textIDs[p.ID]; seen {
				return nil
			}
			s.textIDs[p.ID] = struct{}{}
		}
		if s.text.Len() > 0 {
			s.text.WriteByte('\n')
		}
		s.text.WriteString(p.Text)
	case "step_finish":
		p, err := decodePart(ev.Part, "step_finish", raw)
		if err != nil {
			return err
		}
		// OpenCode emits step_finish for tool-call turns as well as for the
		// final answer. Only `stop` (or an older event without reason) closes
		// the turn; treating an intermediate tool step as terminal can cut
		// the model off before it emits the structured answer.
		if p.Reason == "" || p.Reason == "stop" {
			s.finished = true
		}
		s.stepFinishes++
		if p.Tokens != nil {
			s.usage.InputTokens += p.Tokens.Input
			s.usage.OutputTokens += p.Tokens.Output
			s.usage.CacheReadTokens += p.Tokens.Cache.Read
			s.usage.CacheWriteTokens += p.Tokens.Cache.Write
		}
		if p.Cost != nil {
			s.cost += *p.Cost
			s.costSeen = true
		}
	case "tool_use":
		var tool struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(ev.Part, &tool); err != nil || strings.TrimSpace(tool.Tool) == "" {
			return contract.Fail(contract.FailureUnavailable,
				"opencode tool_use event is malformed").WithRaw(string(raw))
		}
		s.toolCalls = append(s.toolCalls, tool.Tool)
	case "error", "session.error":
		s.errText = rawError(ev.Error, ev.Properties, raw)
	}
	return nil
}

// reached reports that the turn has weighed more than the read allowance it
// was given.
//
// It deliberately does not require s.finished. Finished means a step_finish
// arrived with reason "" or "stop", which is this CLI saying the model has
// already delivered its final answer -- at that point there is no reading
// left to cut short, and the allowance can only ever confirm a turn that
// finished on its own. Requiring it made Request.ReadTokens inoperative on
// this backend for the exact turn it exists to protect: the one calling tools,
// where each intermediate step_finish adds weight and nothing sets finished
// until the money is gone. Crossing it now stops the process at a step
// boundary and hands the session to finalize, which is what the allowance was
// always for.
//
// A step boundary, because that is where this CLI reports usage at all: every
// step_finish carries the tokens of the step it closes, and no other event
// carries any. So one step_finish has to have arrived before the weight means
// anything -- zero steps weigh zero, and an allowance that fired on that would
// kill every turn before its first tool call.
func (s eventStream) reached(limit int) bool {
	return limit > 0 && s.stepFinishes > 0 && allowance.Weigh(
		s.usage.InputTokens, s.usage.OutputTokens,
		s.usage.CacheReadTokens, s.usage.CacheWriteTokens,
	) >= limit
}

func (s eventStream) needsFinalization() bool {
	if s.finished || s.sessionID == "" || s.stepFinishes == 0 {
		return false
	}
	return s.stepFinishes >= maxOpenCodeToolSteps
}

func (s eventStream) charge() contract.Charge {
	out := s.usage
	if s.costSeen {
		out.USD = &s.cost
		out.PricedBy = "opencode step_finish cost"
	}
	return out
}

// limitFailure is the local boundary this adapter enforces in place of the
// native flags OpenCode does not have.
//
// The two ceilings land in different bins, and deliberately. A declared
// max_tokens is the agent type's contract with this machine, so crossing it
// is a refusal -- the same bin model.enforceMaxTokens uses for the same
// number. Request.BudgetUSD is this one call's own ceiling with no larger
// pool behind it, so a turn that hits it was not refused by policy; it simply
// could not answer inside what it was given, which reads the same as a
// provider that did not deliver. That is FailureUnavailable, and it is what
// the Claude backend has always returned for the identical fact -- see
// failureFor in internal/agent/model. Until 2026-08-25 this returned
// permission_denied instead, so which backend was configured decided which
// bin the same budget exhaustion landed in, and callers that sort by bin gave
// the same run two different verdicts.
func (s eventStream) limitFailure(req Request) error {
	charge := s.charge()
	if req.MaxTokens > 0 && charge.Tokens() > req.MaxTokens {
		return contract.Fail(contract.FailurePermissionDenied,
			"opencode reported %d tokens above the requested limit of %d", charge.Tokens(), req.MaxTokens).
			WithRaw(fmt.Sprintf("observed_tokens=%d max_tokens=%d", charge.Tokens(), req.MaxTokens))
	}
	if req.BudgetUSD > 0 && charge.USD != nil && *charge.USD > req.BudgetUSD {
		return contract.Fail(contract.FailureUnavailable,
			"opencode reported a cost above the requested budget ($%.4f > $%.4f)", *charge.USD, req.BudgetUSD).
			WithRaw(fmt.Sprintf("observed_cost_usd=%.8f budget_usd=%.8f", *charge.USD, req.BudgetUSD))
	}
	return nil
}

func promptWithSchema(prompt string, schema map[string]any) (string, error) {
	if len(schema) == 0 {
		return prompt, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"opencode schema cannot be expressed as JSON: %v", err)
	}
	return prompt + "\n\nReturn only one JSON object matching this JSON Schema. Do not use Markdown fences or explanatory text.\nJSON Schema:\n" + string(raw), nil
}

func finalizationPrompt(schema map[string]any) (string, error) {
	return promptWithSchema(
		"Stop using tools now. Use only the evidence already collected. Return the best answer you can now; set completeness below 1 and explain stopped_at when the evidence is partial.",
		schema,
	)
}

func structuredObject(text string, schema map[string]any) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[len(lines)-1], "```") {
			return nil, contract.Fail(contract.FailureInvalidInput, "opencode structured answer has an incomplete Markdown fence")
		}
		trimmed = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		if recovered, ok := recoverStructuredObject(trimmed, schema); ok {
			return recovered, nil
		}
		return nil, contract.Fail(contract.FailureInvalidInput,
			"opencode answered with invalid structured JSON: %v", err).WithRaw(text)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"opencode structured answer contains more than one JSON value").WithRaw(text)
		}
		return nil, contract.Fail(contract.FailureInvalidInput,
			"opencode structured answer has trailing data: %v", err).WithRaw(text)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, contract.Fail(contract.FailureInvalidInput, "opencode structured answer is not a JSON object").WithRaw(text)
	}
	if err := validateSchemaValue(value, schema, "$"); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"opencode structured answer violates its schema: %v", err).WithRaw(text)
	}
	return json.RawMessage(trimmed), nil
}

func recoverStructuredObject(text string, schema map[string]any) (json.RawMessage, bool) {
	for offset := strings.IndexByte(text, '{'); offset >= 0; {
		candidate := text[offset:]
		decoder := json.NewDecoder(strings.NewReader(candidate))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err == nil {
			end := int(decoder.InputOffset())
			if object, ok := value.(map[string]any); ok && strings.TrimSpace(candidate[end:]) == "" {
				if validateSchemaValue(object, schema, "$") == nil {
					return json.RawMessage(candidate[:end]), true
				}
			}
		}
		next := strings.IndexByte(candidate[1:], '{')
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return nil, false
}

// validateSchemaValue covers the JSON Schema subset Atenea's planner emits:
// object closure, required fields, primitive types, nested properties, enum
// and numeric bounds. OpenCode has no native schema flag, so this local check
// is the enforcement boundary after the prompt-level request.
func validateSchemaValue(value any, schema map[string]any, path string) error {
	if schema == nil {
		return nil
	}
	if expected, ok := schema["type"].(string); ok && !schemaTypeMatches(value, expected) {
		return fmt.Errorf("%s must be %s", path, expected)
	}
	switch expected := schema["type"].(string); expected {
	case "object":
		object := value.(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		for _, required := range schemaStrings(schema["required"]) {
			if _, present := object[required]; !present {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		if additionalProperties, ok := schema["additionalProperties"].(bool); ok && !additionalProperties {
			for name := range object {
				if _, declared := properties[name]; !declared {
					return fmt.Errorf("%s.%s is not declared", path, name)
				}
			}
		}
		for name, child := range properties {
			childSchema, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("schema for %s.%s is malformed", path, name)
			}
			if childValue, present := object[name]; present {
				if err := validateSchemaValue(childValue, childSchema, path+"."+name); err != nil {
					return err
				}
			}
		}
	case "array":
		items, _ := schema["items"].(map[string]any)
		for i, item := range value.([]any) {
			if err := validateSchemaValue(item, items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "number", "integer":
		number, err := schemaNumber(value)
		if err != nil {
			return fmt.Errorf("%s is not numeric", path)
		}
		if expected == "integer" && number != float64(int64(number)) {
			return fmt.Errorf("%s must be an integer", path)
		}
		if minimum, ok := schemaNumberField(schema["minimum"]); ok && number < minimum {
			return fmt.Errorf("%s must be at least %v", path, minimum)
		}
		if maximum, ok := schemaNumberField(schema["maximum"]); ok && number > maximum {
			return fmt.Errorf("%s must be at most %v", path, maximum)
		}
	}
	if enum, ok := schema["enum"].([]any); ok && !schemaContains(enum, value) {
		return fmt.Errorf("%s has a value outside enum", path)
	}
	return nil
}

func schemaTypeMatches(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, err := schemaNumber(value)
		return err == nil
	case "integer":
		number, err := schemaNumber(value)
		return err == nil && number == float64(int64(number))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func schemaStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if name, ok := item.(string); ok {
				out = append(out, name)
			}
		}
		return out
	default:
		return nil
	}
}

func schemaNumber(value any) (float64, error) {
	switch number := value.(type) {
	case json.Number:
		return number.Float64()
	case float64:
		return number, nil
	case float32:
		return float64(number), nil
	case int:
		return float64(number), nil
	case int64:
		return float64(number), nil
	default:
		return 0, fmt.Errorf("not a number")
	}
}

func schemaNumberField(value any) (float64, bool) {
	number, err := schemaNumber(value)
	return number, err == nil
}

func schemaContains(values []any, want any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, want) {
			return true
		}
	}
	return false
}

func decodePart(raw json.RawMessage, name string, eventRaw []byte) (part, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return part{}, contract.Fail(contract.FailureUnavailable,
			"opencode %s event is missing its part", name).WithRaw(string(eventRaw))
	}
	var decoded part
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return part{}, contract.Fail(contract.FailureUnavailable,
			"opencode %s event is malformed: %v", name, err).WithRaw(string(eventRaw))
	}
	wantType := map[string]string{"text": "text", "step_finish": "step-finish"}[name]
	if wantType != "" && decoded.Type != wantType {
		return part{}, contract.Fail(contract.FailureUnavailable,
			"opencode %s event has part type %q, want %q", name, decoded.Type, wantType).WithRaw(string(eventRaw))
	}
	return decoded, nil
}

type claudeConfig struct {
	MCPServers map[string]claudeServer `json:"mcpServers"`
}

type claudeServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
}

type openCodeServer struct {
	Type        string            `json:"type"`
	URL         string            `json:"url,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

func openCodeConfig(source string) (string, error) {
	raw := []byte(source)
	if !strings.HasPrefix(strings.TrimSpace(source), "{") {
		var err error
		raw, err = os.ReadFile(source)
		if err != nil {
			return "", contract.Fail(contract.FailureInvalidInput, "opencode MCP config %q: %v", source, err)
		}
	}
	var in claudeConfig
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", contract.Fail(contract.FailureInvalidInput, "opencode MCP config is not valid Claude MCP JSON: %v", err)
	}
	out := make(map[string]openCodeServer, len(in.MCPServers))
	for id, server := range in.MCPServers {
		switch {
		case strings.TrimSpace(server.URL) != "":
			out[id] = openCodeServer{Type: "remote", URL: server.URL}
		case strings.TrimSpace(server.Command) != "":
			command := append([]string{server.Command}, server.Args...)
			out[id] = openCodeServer{Type: "local", Command: command, Environment: server.Env}
		default:
			return "", contract.Fail(contract.FailureInvalidInput, "opencode MCP server %q has neither command nor url", id)
		}
	}
	encoded, err := json.Marshal(struct {
		MCP map[string]openCodeServer `json:"mcp"`
	}{MCP: out})
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput, "opencode MCP config cannot be encoded: %v", err)
	}
	return string(encoded), nil
}

func appendWithout(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
}

func rawError(raw, properties json.RawMessage, fallback []byte) string {
	if len(raw) == 0 && len(properties) > 0 {
		var nested struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(properties, &nested) == nil {
			raw = nested.Error
		}
	}
	if len(raw) == 0 {
		return strings.TrimSpace(string(fallback))
	}
	var object struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if object.Data.Message != "" {
			return object.Data.Message
		}
		if object.Name != "" {
			return object.Name
		}
	}
	return strings.TrimSpace(string(raw))
}

func failureFor(message string, runErr error) *contract.Failure {
	text := strings.TrimSpace(message)
	if text == "" && runErr != nil {
		text = runErr.Error()
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "rate_limit"),
		strings.Contains(lower, "too many requests"), strings.Contains(lower, "quota"):
		return contract.Fail(contract.FailureUnavailable,
			"opencode provider is rate limited or out of quota").WithRaw(text)
	case strings.Contains(lower, "authenticate"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "not logged in"), strings.Contains(lower, "api key"),
		strings.Contains(lower, "401"):
		return contract.Fail(contract.FailureUnavailable,
			"opencode provider is not authenticated on this machine").WithRaw(text)
	case strings.Contains(lower, "context length"), strings.Contains(lower, "context window"),
		strings.Contains(lower, "too many tokens"), strings.Contains(lower, "prompt is too long"):
		return contract.Fail(contract.FailureInvalidInput,
			"opencode rejected the request because its context is too large").WithRaw(text)
	case strings.Contains(lower, "budget"):
		return contract.Fail(contract.FailurePermissionDenied,
			"opencode stopped at a spending ceiling").WithRaw(text)
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"), strings.Contains(lower, "auto-reject"):
		return contract.Fail(contract.FailurePermissionDenied, "opencode refused the work").WithRaw(text)
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such file"):
		return contract.Fail(contract.FailureNotFound, "opencode could not find the requested resource").WithRaw(text)
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return contract.Fail(contract.FailureTimeout, "opencode timed out").WithRaw(text)
	default:
		return contract.Fail(contract.FailureUnavailable, "opencode did not answer").WithRaw(text)
	}
}

func firstToken(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
