package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// MethodStats is the internal socket method for activity snapshots.
const MethodStats = "atenea/stats"

// statsBasePath resolves the routing metrics path without opening its store.
func statsBasePath(cfg config.Config) string {
	if cfg.Metrics.Path != "" {
		return cfg.Metrics.Path
	}
	return metrics.DefaultPath()
}

// statsCatalog builds a declared inventory without contacting providers.
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

// readStats combines activity, catalog, and separately bounded legacy measurements.
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

// statsQuery handles the internal statistics RPC without requiring MCP initialization.
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

// Unwrap preserves access to the underlying runner for existing integrations.
func (r statsRunner) Unwrap() contract.Runner { return r.Runner }

// Run records one implementation attempt linked to the original request.
func (r statsRunner) Run(ctx context.Context, req contract.RunRequest) (out contract.Outcome, err error) {
	_, call := r.store.Begin(ctx, toolstats.Event{Level: "attempt", Tool: req.Implementation.ID, Provider: req.Implementation.Provider, Repository: req.Repository.ID})
	defer func() {
		call.Event.Metadata.ReceiptID = checkpoint.RunID(ctx)
		call.Event.Metadata.ProviderVersion = out.ToolVersion
		observedErr := err
		if observedErr == nil && out.Verdict != contract.VerdictOK {
			observedErr = contract.Fail(contract.FailureUnspecified, "provider verdict: %s", out.Verdict.String())
		}
		call.End(observedErr)
	}()
	out, err = r.Runner.Run(ctx, req)
	return out, err
}

// resultError preserves structured execution failures when the outer call succeeds.
func resultError(result *orchestrator.Result, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return contract.Fail(contract.FailureUnspecified, "no result")
	}
	for _, step := range result.Steps {
		if step.Failure != "" {
			return &contract.Failure{Kind: step.FailureKind, Code: step.FailureCode, Message: step.Failure}
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
		return checkpoint.WithRequestID(ctx, toolstats.RequestID(ctx)), nil
	}
	ctx, call := c.stats.Begin(ctx, toolstats.Event{Level: "request", Tool: tool, Provider: "atenea", Repository: repo})
	return checkpoint.WithRequestID(ctx, toolstats.RequestID(ctx)), call
}

// statsRepository extracts repository metadata from a tool request.
func statsRepository(params toolsCallParams) string {
	args, err := params.argumentMap()
	if err != nil {
		return ""
	}
	repo, _ := args[repositoryArg].(string)
	return repo
}

type statsErrorKey struct{}

// setStatsError passes a structured workflow failure to the request recorder.
func setStatsError(ctx context.Context, err error) {
	if err != nil {
		if target, ok := ctx.Value(statsErrorKey{}).(*error); ok {
			*target = err
		}
	}
}

// StatsErrorsFromDisk reads bounded diagnostics without constructing a Core.
func StatsErrorsFromDisk(ctx context.Context, cfg config.Config, q toolstats.ErrorQuery) (toolstats.ErrorPage, error) {
	return toolstats.New(toolstats.Path(statsBasePath(cfg))).Errors(ctx, q)
}

// MethodStatsErrors is the IPC method for paginated failure diagnostics.
const MethodStatsErrors = "atenea/stats/errors"

func (v *conversation) statsErrors(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var q toolstats.ErrorQuery
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	page, err := v.core.stats.Errors(ctx, q)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	return page, nil
}
