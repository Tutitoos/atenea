package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/resultcache"
	"github.com/Tutitoos/atenea/internal/sourceidentity"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cachedRunner is deliberately a narrow wrapper. It caches only the
// read-only code.context contract and otherwise delegates byte-for-byte to the
// normal runner chain, keeping write, process and external capabilities out of
// this optimization.
type cachedRunner struct {
	contract.Runner
	cache *resultcache.Cache
}

func (r cachedRunner) Unwrap() contract.Runner { return r.Runner }

// Optional identity seams must pass through the cache wrapper. The cache
// validates current provider state before looking up a result, while the
// underlying stats wrapper still measures only the physical read.
func (r cachedRunner) CacheIdentity(ctx context.Context, req contract.RunRequest) (contract.CacheIdentity, error) {
	if provider, ok := r.Runner.(contract.RuntimeIdentityProvider); ok {
		return provider.RuntimeIdentity(ctx, req)
	}
	if provider, ok := r.Runner.(contract.CacheIdentityProvider); ok {
		return provider.CacheIdentity(ctx, req)
	}
	return contract.CacheIdentity{}, contract.Fail(contract.FailureUnavailable, "no current cache identity for implementation %s", req.Implementation.ID)
}

func (r cachedRunner) RuntimeIdentity(ctx context.Context, req contract.RunRequest) (contract.CacheIdentity, error) {
	if provider, ok := r.Runner.(contract.RuntimeIdentityProvider); ok {
		return provider.RuntimeIdentity(ctx, req)
	}
	if provider, ok := r.Runner.(contract.CacheIdentityProvider); ok {
		return provider.CacheIdentity(ctx, req)
	}
	return contract.CacheIdentity{}, contract.Fail(contract.FailureUnavailable, "no current runtime identity for implementation %s", req.Implementation.ID)
}

type cachedEnvelope struct {
	Outcome   contract.Outcome `json:"outcome"`
	Cacheable bool             `json:"cacheable"`
}

func (r cachedRunner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if !cacheableRequest(req) || r.cache == nil {
		return r.Runner.Run(ctx, req)
	}
	// Validation and authorization happen before even the cheap identity
	// probe. A denied request must not wake a provider merely to decide whether
	// a cached answer exists.
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if missing, ok := req.Allowed(); !ok {
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"%s causes %s, which the request permission does not cover", req.Capability.ID, missing)
	}
	identityProvider, providerOK := cacheIdentityProvider(r.Runner)
	if req.ObservedIdentity == nil && !providerOK {
		// A provider that cannot attest current generation/freshness is never
		// allowed to serve a stale cached answer.
		return r.Runner.Run(ctx, req)
	}
	validationStarted := time.Now()
	validation := &contract.CacheValidation{Called: true, Provider: req.Implementation.Provider, Tool: "graph_status"}
	attachValidation := func(out contract.Outcome, runErr error) (contract.Outcome, error) {
		out.CacheValidation = validation
		return out, runErr
	}
	var observed contract.CacheIdentity
	var err error
	if req.ObservedIdentity != nil {
		observed = *req.ObservedIdentity
		if observed.Provider != "" {
			validation.Provider = observed.Provider
		}
		if observed.Tool != "" {
			validation.Tool = observed.Tool
		}
		if observed.DurationNS > 0 {
			validation.Duration = time.Duration(observed.DurationNS)
		} else {
			validation.Duration = time.Since(validationStarted)
		}
		if observed.Error != "" {
			err = fmt.Errorf("%s", observed.Error)
		}
	} else {
		observed, err = identityProvider(ctx, req)
		validation.Duration = time.Since(validationStarted)
	}
	if err != nil || !cacheableIdentity(observed) {
		validation.ToolVersion, validation.Instance = observed.ToolVersion, observed.Instance
		validation.Generation, validation.Snapshot, validation.Freshness = observed.Generation, observed.Snapshot, observed.Freshness
		if err == nil {
			validation.Error = "identity incomplete or not cacheable"
		} else {
			validation.Error = contract.RedactRaw(err.Error())
		}
		out, runErr := r.Runner.Run(ctx, req)
		return attachValidation(out, runErr)
	}
	validation.ToolVersion, validation.Instance = observed.ToolVersion, observed.Instance
	validation.Generation, validation.Snapshot, validation.Freshness = observed.Generation, observed.Snapshot, observed.Freshness
	identity, err := sourceidentity.Discover(ctx, req.Repository.Path)
	if err != nil || identity.Fingerprint == "" {
		if err != nil {
			validation.Error = "source identity: " + contract.RedactRaw(err.Error())
		} else {
			validation.Error = "source identity unavailable"
		}
		out, runErr := r.Runner.Run(ctx, req)
		return attachValidation(out, runErr)
	}
	permission, err := resultcache.PermissionDigest(req.Permission)
	if err != nil {
		validation.Error = "permission key: " + contract.RedactRaw(err.Error())
		out, runErr := r.Runner.Run(ctx, req)
		return attachValidation(out, runErr)
	}
	key, err := resultcache.CanonicalKey(resultcache.KeyParts{
		PermissionDigest: permission,
		RepositoryID:     req.Repository.ID, RepositoryRoot: identity.Root,
		SourceFingerprint: identity.Fingerprint, Capability: req.Capability.ID,
		CapabilityVersion: req.Capability.Version.String(), Payload: req.Payload,
		Cursor: stringValue(req.Payload["cursor"]), Implementation: req.Implementation.ID,
		Provider: req.Implementation.Provider, ToolVersion: observed.ToolVersion,
		ToolInstance: observed.Instance, ConfigDigest: req.Implementation.ConfigDigest,
		Generation: observed.Generation,
		Snapshot:   observedSnapshot(observed.Snapshot), Freshness: observed.Freshness,
	})
	if err != nil {
		validation.Error = "cache key: " + contract.RedactRaw(err.Error())
		out, runErr := r.Runner.Run(ctx, req)
		return attachValidation(out, runErr)
	}
	encoded, source, err := r.cache.DoIfState(ctx, key, func(callCtx context.Context) ([]byte, error) {
		out, runErr := r.Runner.Run(callCtx, req)
		// The identity used to form the key is a point-in-time observation. A
		// provider may rotate its graph while the read is in flight; share this
		// result with current waiters, but never publish it under the old key.
		cacheable := runErr == nil && cacheableOutcome(out) && outcomeMatchesIdentity(out, observed)
		encoded, marshalErr := json.Marshal(cachedEnvelope{Outcome: out, Cacheable: cacheable})
		if marshalErr != nil {
			if runErr != nil {
				return nil, runErr
			}
			return nil, marshalErr
		}
		// Keep the provider outcome beside its error. The leader needs its
		// measured spend for accounting, and waiters need the useful evidence
		// while being prevented from charging that physical spend again.
		return encoded, runErr
	}, func(raw []byte) bool {
		var envelope cachedEnvelope
		return json.Unmarshal(raw, &envelope) == nil && envelope.Cacheable
	})
	if err != nil {
		// Preserve the identity receipt even when the full provider call fails.
		// The provider error is returned separately and must not masquerade as
		// a cache-validation error. A waiter still records that it joined the
		// physical failed call, so accounting cannot mistake it for another
		// provider attempt.
		var envelope cachedEnvelope
		if len(encoded) > 0 {
			_ = json.Unmarshal(encoded, &envelope)
		}
		out, _ := attachValidation(envelope.Outcome, nil)
		if source == resultcache.SourceWaiter {
			out.Coalesced = true
			out.Spent = contract.Sample{}
			out.SpentUSD = 0
			out.SpentUSDKnown = false
			if validation != nil {
				out.Spent.Duration = validation.Duration
			}
		}
		return out, err
	}
	var envelope cachedEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		out, _ := attachValidation(contract.Outcome{}, nil)
		return out, err
	}
	out := envelope.Outcome
	out.CacheValidation = validation
	if source != resultcache.SourceLeader {
		// A waiter may have joined a physical call whose answer was partial and
		// therefore not retained. It receives the answer for correctness, but
		// must not charge the provider a second time. Keep CacheHit false and
		// expose the distinct coalescing state.
		if source == resultcache.SourceStored {
			out.CacheHit = true
			out.CacheVersion = resultcache.Version
		} else {
			out.Coalesced = true
		}
		// A local hit or coalesced waiter has no provider spend or latency.
		out.Spent = contract.Sample{}
		if validation != nil {
			out.Spent.Duration = validation.Duration
		}
		out.SpentUSD = 0
		out.SpentUSDKnown = false
	}
	return out, nil
}

func cacheIdentityProvider(runner contract.Runner) (func(context.Context, contract.RunRequest) (contract.CacheIdentity, error), bool) {
	if provider, ok := optional[contract.RuntimeIdentityProvider](runner); ok {
		return provider.RuntimeIdentity, true
	}
	if provider, ok := optional[contract.CacheIdentityProvider](runner); ok {
		return provider.CacheIdentity, true
	}
	return nil, false
}

func cacheableRequest(req contract.RunRequest) bool {
	if req.Capability.ID != "code.context" || len(req.Capability.Effects) != 1 || req.Capability.Effects[0] != contract.EffectRead {
		return false
	}
	if req.Repository.Path == "" || req.Repository.ID == "" {
		return false
	}
	if containsSensitive(req.Payload) {
		return false
	}
	return true
}

func cacheableOutcome(out contract.Outcome) bool {
	if out.Verdict != contract.VerdictOK || out.Result == nil || out.OutOfScope != 0 || len(out.Notices) > 0 || containsSensitive(out.Result) || contract.StructuralPartial(out.Result) || len(out.Evidence) == 0 {
		return false
	}
	var generation, snapshot int
	var complete, fresh bool
	for _, ev := range out.Evidence {
		if ev.Truncated {
			return false
		}
		if ev.Completeness != "" {
			if !strings.EqualFold(ev.Completeness, "complete") {
				return false
			}
			complete = true
		}
		if ev.Freshness != "" {
			if !strings.EqualFold(ev.Freshness, "fresh") {
				return false
			}
			fresh = true
		}
		if ev.ContentGeneration > 0 {
			if generation != 0 && ev.ContentGeneration != generation {
				return false
			}
			generation = ev.ContentGeneration
		} else if ev.ContentGeneration < 0 {
			return false
		}
		if ev.SnapshotID > 0 {
			if snapshot != 0 && ev.SnapshotID != snapshot {
				return false
			}
			snapshot = ev.SnapshotID
		} else if ev.SnapshotID < 0 {
			return false
		}
	}
	return complete && fresh && generation > 0 && snapshot > 0
}

func cacheableIdentity(identity contract.CacheIdentity) bool {
	return identity.Generation > 0 && identity.Snapshot > 0 && strings.EqualFold(identity.Freshness, "fresh") && strings.TrimSpace(identity.ToolVersion) != "" && strings.TrimSpace(identity.Instance) != ""
}

func outcomeMatchesIdentity(out contract.Outcome, identity contract.CacheIdentity) bool {
	if strings.TrimSpace(out.ToolVersion) != "" && out.ToolVersion != identity.ToolVersion {
		return false
	}
	if strings.TrimSpace(out.ToolInstance) != "" && out.ToolInstance != identity.Instance {
		return false
	}
	var generation, snapshot int
	var freshness string
	for _, ev := range out.Evidence {
		if ev.ContentGeneration > 0 {
			if generation != 0 && generation != ev.ContentGeneration {
				return false
			}
			generation = ev.ContentGeneration
		}
		if ev.SnapshotID > 0 {
			if snapshot != 0 && snapshot != ev.SnapshotID {
				return false
			}
			snapshot = ev.SnapshotID
		}
		if strings.TrimSpace(ev.Freshness) != "" {
			if freshness != "" && !strings.EqualFold(freshness, ev.Freshness) {
				return false
			}
			freshness = ev.Freshness
		}
	}
	return generation == identity.Generation && snapshot == identity.Snapshot && strings.EqualFold(freshness, identity.Freshness)
}

func containsSensitive(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.EqualFold(strings.TrimSpace(key), "sensitive") {
				if yes, ok := child.(bool); ok && yes {
					return true
				}
			}
			if containsSensitive(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsSensitive(child) {
				return true
			}
		}
	}
	return false
}

func stringValue(v any) string { s, _ := v.(string); return s }
func observedSnapshot(v int) string {
	if v < 1 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}
