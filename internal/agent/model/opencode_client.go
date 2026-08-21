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
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	modelName, err := c.modelFor(req.Role)
	if err != nil {
		return Answer{}, err
	}
	prompt := req.Prompt
	if req.reservesAnswer() {
		prompt = req.sentPrompt()
	}
	got, err := c.opencode.Run(turnCtx, agentopencode.Request{
		Model:      modelName,
		Prompt:     prompt,
		Dir:        dir,
		BudgetUSD:  req.BudgetUSD,
		ReadTokens: req.ReadTokens,
		Tools:      req.Tools,
		Schema:     req.Schema,
	})
	answer := Answer{
		Text:       got.Text,
		Structured: got.Structured,
		Spent:      got.Spent,
		Passes:     got.Passes,
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
