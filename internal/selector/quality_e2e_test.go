package selector_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestRuntimeIdentitySelectRecordRestartQuality exercises the production
// seams in their order: current runtime identity, selection, outcome recording
// and a selector recreated from the durable ledger. The two providers are
// deliberately equivalent fakes; only their observed outcomes differ.
func TestRuntimeIdentitySelectRecordRestartQuality(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(root, "quality.json")
	repo := contract.NewRepository("repo", root, []string{"go"}, contract.ScaleSmall, contract.VCSAbsent, nil)
	impl := func(id, provider, digest string) contract.Implementation {
		return contract.Implementation{ID: id, Provider: provider, Capability: "code.context", ConfigDigest: digest,
			Health: contract.Health{State: contract.HealthAlive, Score: 0.8},
			Cost:   contract.Cost{Estimated: contract.Sample{Duration: 10}}}
	}
	candidates := []contract.Implementation{impl("route-a", "kivgraph", "cfg-a"), impl("route-b", "tokensave", "cfg-b")}
	providers := map[string]contract.CacheIdentity{
		"route-a": {ToolVersion: "v1", Instance: "cfg-a@session-a", Provider: "kivgraph", Tool: "graph_status", Observed: true},
		"route-b": {ToolVersion: "v1", Instance: "cfg-b@session-b", Provider: "tokensave", Tool: "tokensave_status", Observed: true},
	}
	identity := func(id string) contract.CacheIdentity { return providers[id] }
	observed := func() (map[string]string, map[string]string, map[string]string) {
		versions, instances, digests := map[string]string{}, map[string]string{}, map[string]string{}
		for _, candidate := range candidates {
			current := identity(candidate.ID)
			versions[candidate.ID], instances[candidate.ID], digests[candidate.ID] = current.ToolVersion, current.Instance, candidate.ConfigDigest
		}
		return versions, instances, digests
	}
	selectNow := func(s *selector.Selector, prefer string) selector.Decision {
		versions, instances, digests := observed()
		decision, err := s.Select(selector.Request{Capability: "code.context", Repository: repo, Candidates: candidates,
			Reachable: []string{"route-a", "route-b"}, Measuring: true, Language: "go", Prefer: prefer,
			ObservedVersions: versions, ObservedInstances: instances, ObservedConfigDigests: digests})
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}

	first, err := selector.New(selector.Config{QualityMinimumSamples: 2, QualityPath: ledger})
	if err != nil {
		t.Fatal(err)
	}
	if got := selectNow(first, "").Chosen.ID; got != "route-a" {
		t.Fatalf("initial route = %s, want route-a before outcome evidence", got)
	}
	failed := contract.Outcome{Verdict: contract.VerdictFailed}
	success := contract.Outcome{Verdict: contract.VerdictOK, Result: map[string]any{"matches": []any{}}, Evidence: []contract.QueryEvidence{{Freshness: "fresh", ContentGeneration: 1, SnapshotID: 1, Completeness: "complete"}}}
	for i := range 2 {
		aOutcome, aErr := failed, errors.New("route unavailable")
		if i == 0 {
			aOutcome, aErr = success, nil
		}
		if err := first.RecordOutcome("code.context", repo.ID, "route-a", "go", identity("route-a").ToolVersion, aOutcome, aErr, repo.Path, identity("route-a").Instance, candidates[0].ConfigDigest); err != nil {
			t.Fatal(err)
		}
		if err := first.RecordOutcome("code.context", repo.ID, "route-b", "go", identity("route-b").ToolVersion, success, nil, repo.Path, identity("route-b").Instance, candidates[1].ConfigDigest); err != nil {
			t.Fatal(err)
		}
	}

	// A new selector is the restart boundary. It must use the same current
	// identities and choose the route whose validated outcomes survived it.
	restarted, err := selector.New(selector.Config{QualityMinimumSamples: 2, QualityPath: ledger})
	if err != nil {
		t.Fatal(err)
	}
	decision := selectNow(restarted, "")
	if got := decision.Chosen.ID; got != "route-b" {
		t.Fatalf("restarted route = %s, want route-b from durable quality", got)
	}
	if got := selectNow(restarted, "route-a").Chosen.ID; got != "route-a" {
		t.Fatalf("preference route = %s, want route-a", got)
	}
}
