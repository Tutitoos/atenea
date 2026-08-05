package codebasememory

// repository.index: hands a repository to codebase-memory-mcp and asks it to
// build or refresh the graph everything else in this package only ever
// reads. Building is the one thing detection itself must never do on a
// caller's behalf -- this is where "write" and "process" actually apply for
// this adapter, and the only capability here that carries them.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// defaultIndexMode is the thoroughness asked for when the caller does not
// name one -- the middle of the three the tool itself offers, the same
// "small by default" reasoning every optional field in this package
// follows: cheaper than a full reindex, and unlike "fast" it still resolves
// the similarity edges a caller reading the graph later may want, even
// though detection's own freshness check never does.
const defaultIndexMode = "moderate"

// indexRepositoryAsk is repository.index's payload, read once.
type indexRepositoryAsk struct {
	mode string
}

func readIndexRepositoryAsk(payload map[string]any) (indexRepositoryAsk, error) {
	out := indexRepositoryAsk{mode: defaultIndexMode}
	if mode := strings.ToLower(strings.TrimSpace(stringAt(payload, "mode"))); mode != "" {
		switch mode {
		case "fast", "moderate", "full":
			out.mode = mode
		default:
			return indexRepositoryAsk{}, contract.Fail(contract.FailureInvalidInput,
				`repository.index: mode must be "fast", "moderate" or "full", got %q`, mode)
		}
	}
	return out, nil
}

// indexRepositoryResponse is index_repository's answer, narrowed to what the
// capability promises back. The tool reports more -- excluded directories,
// an ADR hint, whether a shared artifact was written -- none of which
// repository.index's own contract claims to answer, the same restraint
// symbol.calls already shows trace_path's own richer answer.
type indexRepositoryResponse struct {
	Status string `json:"status"`
	Nodes  int    `json:"nodes"`
	Edges  int    `json:"edges"`
}

func (r *Runner) runRepositoryIndex(ctx context.Context, req contract.RunRequest, root string) (contract.Outcome, error) {
	started := time.Now()
	ask, err := readIndexRepositoryAsk(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}
	weight := &meter{}

	// repo_path, not project: index_repository is the one tool in this
	// family that names its path argument differently, measured rather
	// than assumed.
	raw, err := r.invoke(ctx, "index_repository", map[string]any{
		"repo_path": root,
		"mode":      ask.mode,
	}, weight)
	if err != nil {
		return contract.Outcome{}, err
	}
	var resp indexRepositoryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"codebase-memory: could not read index_repository's answer: %v", err).WithRaw(string(raw))
	}

	result := map[string]any{
		"status": resp.Status,
		"nodes":  resp.Nodes,
		"edges":  resp.Edges,
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}
	return contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		Spent:   contract.Sample{Duration: time.Since(started), PeakRSS: weight.peak},
	}, nil
}
