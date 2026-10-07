package passthrough

import (
	"context"
	"encoding/json"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// CatalogContract carries observed contracts and an opaque dispatch lease.
// CallContract can send only to the process which issued that lease.
type CatalogContract struct {
	Tools                     []Tool
	Version, WorkingDirectory string
	lease                     *stdioContractLease
}

// Bound reports whether this catalog carries a transport dispatch lease.
func (c CatalogContract) Bound() bool { return c.lease != nil }

// ContractBackend ties validation and dispatch to one upstream lifecycle.
type ContractBackend interface {
	ContractTools(context.Context) (CatalogContract, error)
	CallContract(context.Context, string, map[string]any, CatalogContract) (json.RawMessage, error)
}

type stdioContractLease struct {
	backend            *stdioBackend
	proc               *process
	generation         uint64
	modern             bool
	version, workspace string
}

func staleContract() error {
	return &contract.Failure{Kind: contract.FailureInvalidInput, Code: "compatibility_unverified", HealthNeutral: true, Message: "Upstream process/catalog changed after validation; no tool action was sent. Observe state before deciding whether to retry."}
}

func (b *stdioBackend) contractCurrentLocked(lease *stdioContractLease) bool {
	if lease == nil || lease.backend != b || lease.proc != b.proc || lease.generation != b.generation.Load() || lease.modern != b.activeModern || lease.version != b.Version() || lease.workspace != b.workingDirectory {
		return false
	}
	select {
	case <-lease.proc.gone:
		return false
	default:
		return true
	}
}

// ContractTools loads the catalog from an exact process, without transparent
// restart. A response from a retired child never qualifies its successor.
func (b *stdioBackend) ContractTools(ctx context.Context) (CatalogContract, error) {
	proc, err := b.ensure(ctx)
	if err != nil {
		return CatalogContract{}, err
	}
	b.mu.Lock()
	lease := &stdioContractLease{backend: b, proc: proc, generation: b.generation.Load(), modern: b.activeModern, version: b.Version(), workspace: b.workingDirectory}
	current := b.contractCurrentLocked(lease)
	supports := !lease.modern || b.discovery.SupportsTools()
	b.mu.Unlock()
	if !current {
		return CatalogContract{}, staleContract()
	}
	if !supports {
		return CatalogContract{}, b.fail(contract.FailureUnavailable, "server did not advertise tools capability")
	}
	tools, err := b.catalog.get(ctx, lease.generation, func() ([]Tool, cacheHint, error) {
		raw, err := b.sendWithMode(ctx, proc, "tools/list", map[string]any{}, lease.modern)
		if err != nil {
			return nil, cacheHint{}, err
		}
		tools, drift, err := toolsFromReport(raw, b.allowed, b.fail, lease.modern)
		b.setCatalogDrift(drift)
		if err != nil {
			return nil, cacheHint{}, err
		}
		hint := cacheHint{Cache: true}
		if lease.modern {
			hint, err = modernCacheHint(raw)
		}
		return tools, hint, err
	})
	if err != nil {
		return CatalogContract{}, err
	}
	b.mu.Lock()
	current = b.contractCurrentLocked(lease)
	b.mu.Unlock()
	if !current {
		return CatalogContract{}, staleContract()
	}
	return CatalogContract{Tools: tools, Version: lease.version, WorkingDirectory: lease.workspace, lease: lease}, nil
}

// CallContract checks the lease, then sends to that same child without ensure.
// A later death can produce an uncertain result on the old process, never an
// automatic action on a replacement process.
func (b *stdioBackend) CallContract(ctx context.Context, tool string, args map[string]any, catalog CatalogContract) (json.RawMessage, error) {
	if !b.Allows(tool) {
		return nil, b.fail(contract.FailurePermissionDenied, "tool %q is not in this backend's tools", tool)
	}
	b.mu.Lock()
	lease := catalog.lease
	current := b.contractCurrentLocked(lease) && catalog.Version == lease.version && catalog.WorkingDirectory == lease.workspace
	b.mu.Unlock()
	if !current {
		return nil, staleContract()
	}
	if args == nil {
		args = map[string]any{}
	}
	return b.sendWithMode(ctx, lease.proc, "tools/call", map[string]any{"name": tool, "arguments": args}, lease.modern)
}
