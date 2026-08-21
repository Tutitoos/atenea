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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	DefaultBinary  = "opencode"
	DefaultTimeout = 5 * time.Minute
)

// Options configures the OpenCode executable.
type Options struct {
	Binary  string
	Timeout time.Duration
}

// Request is one OpenCode model turn.
type Request struct {
	Model      string
	Prompt     string
	Dir        string
	BudgetUSD  float64
	ReadTokens int
	Tools      string
	Schema     map[string]any
}

// Answer is the provider-neutral result needed by the model client.
type Answer struct {
	Text       string
	Structured json.RawMessage
	Spent      contract.Charge
	Passes     int
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

// Version returns the CLI's first version token, or empty when it cannot be
// queried.
func (r *Runner) Version(ctx context.Context) string { return firstToken(r.version.Version(ctx)) }

// Run executes one non-interactive JSON event stream.
func (r *Runner) Run(ctx context.Context, req Request) (Answer, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return Answer{}, contract.Fail(contract.FailureInvalidInput, "opencode request: prompt is required")
	}
	if req.BudgetUSD < 0 {
		return Answer{}, contract.Fail(contract.FailureInvalidInput,
			"opencode request: budget_usd must not be negative, got %v", req.BudgetUSD)
	}
	if req.ReadTokens < 0 {
		return Answer{}, contract.Fail(contract.FailureInvalidInput,
			"opencode request: read_tokens must not be negative, got %d", req.ReadTokens)
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
	argv := []string{"run", "--format", "json", "--pure"}
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
	turnCtx, cancel := context.WithTimeout(ctx, r.timeout)
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
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := stream.accept(scanner.Bytes()); err != nil {
			_ = procgroup.Kill(cmd)
			_ = cmd.Wait()
			return Answer{}, err
		}
		if stream.reached(req.ReadTokens) {
			// OpenCode has no stdin finalize protocol. Stop only after a
			// completed step, retaining the text already emitted. This is an
			// observed boundary, never an exact provider cap.
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
		return Answer{}, contract.Stopped(ctxErr, "opencode", r.timeout).WithRaw(strings.TrimSpace(stderr.String()))
	}
	if scanErr != nil {
		return Answer{}, contract.Fail(contract.FailureUnavailable,
			"opencode event stream could not be read: %v", scanErr).WithRaw(strings.TrimSpace(stderr.String()))
	}
	if stream.errText != "" {
		return Answer{Spent: stream.charge()}, failureFor(stream.errText, waitErr)
	}
	if waitErr != nil && !stream.stopped {
		return Answer{Spent: stream.charge()}, failureFor(strings.TrimSpace(stderr.String()), waitErr)
	}
	if !stream.finished {
		return Answer{Spent: stream.charge()}, contract.Fail(contract.FailureUnavailable,
			"opencode ended without a step_finish event").WithRaw(strings.TrimSpace(stderr.String()))
	}
	text := strings.TrimSpace(stream.text.String())
	if text == "" {
		return Answer{Spent: stream.charge()}, contract.Fail(contract.FailureUnavailable,
			"opencode completed a step without text output").WithRaw(strings.TrimSpace(stderr.String()))
	}
	answer := Answer{Text: text, Spent: stream.charge(), Passes: 1}
	if len(req.Schema) > 0 {
		structured, err := structuredObject(text)
		if err != nil {
			return Answer{Spent: answer.Spent, Passes: answer.Passes}, err
		}
		answer.Structured = structured
	}
	return answer, nil
}

type eventStream struct {
	text     strings.Builder
	textIDs  map[string]struct{}
	finished bool
	stopped  bool
	errText  string
	usage    contract.Charge
	cost     float64
	costSeen bool
}

type event struct {
	Type  string          `json:"type"`
	Part  json.RawMessage `json:"part"`
	Error json.RawMessage `json:"error"`
}

type part struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
	Time struct {
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
	switch ev.Type {
	case "text":
		var p part
		if err := json.Unmarshal(ev.Part, &p); err != nil {
			return contract.Fail(contract.FailureUnavailable, "opencode text event is malformed: %v", err).WithRaw(string(raw))
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
		var p part
		if err := json.Unmarshal(ev.Part, &p); err != nil {
			return contract.Fail(contract.FailureUnavailable, "opencode step_finish event is malformed: %v", err).WithRaw(string(raw))
		}
		s.finished = true
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
	case "error", "session.error":
		s.errText = rawError(ev.Error, raw)
	}
	return nil
}

func (s eventStream) reached(limit int) bool {
	return limit > 0 && s.finished && allowance.Weigh(
		s.usage.InputTokens, s.usage.OutputTokens,
		s.usage.CacheReadTokens, s.usage.CacheWriteTokens,
	) >= limit
}

func (s eventStream) charge() contract.Charge {
	out := s.usage
	if s.costSeen {
		out.USD = &s.cost
		out.PricedBy = "opencode step_finish cost"
	}
	return out
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

func structuredObject(text string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[len(lines)-1], "```") {
			return nil, contract.Fail(contract.FailureInvalidInput, "opencode structured answer has an incomplete Markdown fence")
		}
		trimmed = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"opencode answered with invalid structured JSON: %v", err).WithRaw(text)
	}
	if object == nil {
		return nil, contract.Fail(contract.FailureInvalidInput, "opencode structured answer is not a JSON object").WithRaw(text)
	}
	return json.RawMessage(trimmed), nil
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

func rawError(raw json.RawMessage, fallback []byte) string {
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
