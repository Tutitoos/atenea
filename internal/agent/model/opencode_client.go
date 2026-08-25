package model

import (
	"context"
	"encoding/json"
	"time"

	agentopencode "github.com/Tutitoos/atenea/internal/agent/opencode"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func (c *Client) turnOpenCode(ctx context.Context, dir string, timeout time.Duration, req Request) (Answer, error) {
	if c.opencode == nil {
		return Answer{}, contract.Fail(contract.FailureUnavailable, "opencode backend is not initialized")
	}
	modelName, err := c.modelFor(req.Role)
	if err != nil {
		return Answer{}, err
	}
	prompt := req.Prompt
	if req.reservesAnswer() {
		prompt = req.sentPrompt()
	}
	// The deadline travels as a field rather than as a context this side
	// wraps: the runner bounds the turn AND its finalization pass with it,
	// and it is the runner that names the limit in the timeout failure. A
	// context wrapped here as well would be the same instant twice, and the
	// runner would still have reported its own default.
	got, err := c.opencode.Run(ctx, agentopencode.Request{
		Model:      modelName,
		Prompt:     prompt,
		Dir:        dir,
		BudgetUSD:  req.BudgetUSD,
		MaxTokens:  req.MaxTokens,
		ReadTokens: req.ReadTokens,
		Tools:      req.Tools,
		Schema:     req.Schema,
		Timeout:    timeout,
	})
	answer := Answer{
		Text:       got.Text,
		Structured: got.Structured,
		Spent:      got.Spent,
		Passes:     got.Passes,
		ToolCalls:  append([]string(nil), got.ToolCalls...),
	}
	if err != nil {
		return answer, err
	}
	if len(answer.Structured) > 0 {
		var claim struct {
			Completeness *float64 `json:"completeness"`
			StoppedAt    string   `json:"stopped_at"`
		}
		if err := json.Unmarshal(answer.Structured, &claim); err == nil {
			answer.Completeness = claim.Completeness
			answer.StoppedAt = claim.StoppedAt
		}
	}
	return answer, nil
}
