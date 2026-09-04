package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// MethodStats is the internal socket method for activity snapshots.
const MethodStats = "atenea/stats"

func statsBasePath(cfg config.Config) string {
	if cfg.Metrics.Path != "" {
		return cfg.Metrics.Path
	}
	return metrics.DefaultPath()
}
func statsCatalog(cfg config.Config) []toolstats.Tool {
	var out []toolstats.Tool
	for _, c := range cfg.Capabilities {
		out = append(out, toolstats.Tool{Level: "request", Name: c.ID, Provider: "atenea", State: "declared"})
	}
	for _, name := range []string{toolListRepositories, commandTool, toolWorkflowCreate, toolWorkflowLaunch, "orchestrator.task"} {
		out = append(out, toolstats.Tool{Level: "request", Name: name, Provider: "atenea", State: "declared"})
	}
	for _, i := range cfg.Implementations {
		out = append(out, toolstats.Tool{Level: "attempt", Name: i.ID, Provider: i.Provider, State: i.Health.State.String()})
	}
	for _, s := range cfg.MCPServers {
		if s.Expose == config.ExposeRaw {
			out = append(out, toolstats.Tool{Level: "catalog", Name: "raw." + s.ID + ".*", Provider: s.ID, State: "catalog_unknown"})
			for _, name := range s.Tools {
				for _, level := range []string{"request", "attempt"} {
					out = append(out, toolstats.Tool{Level: level, Name: "raw." + s.ID + "." + name, Provider: s.ID, State: "declared"})
				}
			}
		}
	}
	return out
}

// StatsFromDisk is deliberately independent of New: reading stats cannot run upkeep or probes.
func StatsFromDisk(ctx context.Context, cfg config.Config, q toolstats.Query) (toolstats.Snapshot, error) {
	store := toolstats.New(toolstats.Path(statsBasePath(cfg)))
	return readStats(ctx, cfg, store, q)
}
func readStats(ctx context.Context, cfg config.Config, store *toolstats.Store, q toolstats.Query) (toolstats.Snapshot, error) {
	out, err := store.Read(ctx, q, statsCatalog(cfg))
	if err != nil {
		return out, err
	}
	if err = toolstats.Legacy(ctx, statsBasePath(cfg), &out); err != nil {
		out.Legacy = nil
		out.LegacyError = toolstats.Clean(err.Error(), 240)
		out.Coverage.Partial = true
		out.Coverage.Notes = append(out.Coverage.Notes, "Legacy metrics could not be read: "+out.LegacyError)
		return out, nil
	}
	return out, nil
}

// Stats returns activity and catalog data without starting providers.
func (c *Core) Stats(ctx context.Context, q toolstats.Query) (toolstats.Snapshot, error) {
	out, err := readStats(ctx, c.settings, c.stats, q)
	out.Source = "service"
	out.Service = "running"
	return out, err
}
func (v *conversation) statsQuery(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var q toolstats.Query
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
	}
	out, err := v.core.Stats(ctx, q)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	return out, nil
}

// AskedStats queries the running service without an MCP handshake.
func AskedStats(q toolstats.Query) (toolstats.Snapshot, error) {
	conn, err := ipc.DialTimeout(SocketPath(), askTimeout)
	if err != nil {
		return toolstats.Snapshot{}, err
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return toolstats.Snapshot{}, err
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return toolstats.Snapshot{}, err
	}
	if err = json.NewEncoder(conn).Encode(rpcRequest{JSONRPC: rpcVersion, ID: 1, Method: MethodStats, Params: raw}); err != nil {
		return toolstats.Snapshot{}, err
	}
	var reply struct {
		Result toolstats.Snapshot `json:"result"`
		Error  *rpcError          `json:"error"`
	}
	if err = json.NewDecoder(conn).Decode(&reply); err != nil {
		return reply.Result, err
	}
	if reply.Error != nil {
		return reply.Result, fmt.Errorf("service stats: %s", reply.Error.Message)
	}
	if reply.Result.Version != 1 {
		return reply.Result, fmt.Errorf("service does not support stats version 1")
	}
	return reply.Result, nil
}

type statsRunner struct {
	contract.Runner
	store *toolstats.Store
}

func (r statsRunner) Unwrap() contract.Runner { return r.Runner }

func (r statsRunner) Run(ctx context.Context, req contract.RunRequest) (out contract.Outcome, err error) {
	_, call := r.store.Begin(ctx, toolstats.Event{Level: "attempt", Tool: req.Implementation.ID, Provider: req.Implementation.Provider, Repository: req.Repository.ID})
	defer func() {
		observedErr := err
		if observedErr == nil && out.Verdict != contract.VerdictOK {
			observedErr = contract.Fail(contract.FailureUnspecified, "provider verdict: %s", out.Verdict.String())
		}
		call.End(observedErr)
	}()
	out, err = r.Runner.Run(ctx, req)
	return out, err
}
func resultError(result *orchestrator.Result, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return contract.Fail(contract.FailureUnspecified, "no result")
	}
	for _, step := range result.Steps {
		if step.Failure != "" {
			return contract.Fail(step.FailureKind, "%s", step.Failure)
		}
		if step.Review.Parent != contract.VerdictOK {
			return contract.Fail(contract.FailureUnspecified, "review verdict: %s", step.Review.Parent.String())
		}
	}
	return nil
}

// StartStatsRequest brackets validation at a caller boundary; nested Core calls reuse its ID.
func (c *Core) StartStatsRequest(ctx context.Context, tool, repo string) (context.Context, *toolstats.Call) {
	if toolstats.RequestID(ctx) != "" {
		return ctx, nil
	}
	return c.stats.Begin(ctx, toolstats.Event{Level: "request", Tool: tool, Provider: "atenea", Repository: repo})
}
func statsMCPResult(result any, rpcErr *rpcError) (string, string, string) {
	if rpcErr != nil {
		return "fail", "invalid_request", rpcErr.Message
	}
	if m, ok := result.(map[string]any); ok {
		if failed, _ := m["isError"].(bool); failed {
			code := "tool_failure"
			reason := "Tool returned isError"
			if d, ok := m["structuredContent"].(map[string]any); ok {
				if v, ok := d["error_code"].(string); ok {
					code = v
				}
			}
			if items, ok := m["content"].([]any); ok && len(items) > 0 {
				if item, ok := items[0].(map[string]any); ok {
					if text, ok := item["text"].(string); ok {
						reason = text
					}
				}
			}
			switch code {
			case "profile_denied", "permission_denied", "external_denied":
				return "refused", code, reason
			case "canceled":
				return "cancel", code, reason
			}
			return "fail", code, reason
		}
	}
	return "ok", "", ""
}

func statsRepository(params toolsCallParams) string {
	args, err := params.argumentMap()
	if err != nil {
		return ""
	}
	repo, _ := args[repositoryArg].(string)
	return repo
}

type statsErrorKey struct{}

func setStatsError(ctx context.Context, err error) {
	if err != nil {
		if target, ok := ctx.Value(statsErrorKey{}).(*error); ok {
			*target = err
		}
	}
}
