package core

// DetectIndexes' own logic -- which runners get asked, which repositories,
// what a probe error does and does not touch -- is what these tests cover.
// ProbeIndex's own classification of a real codebase-memory-mcp answer is
// already covered where that adapter lives; here a scripted double stands
// in for any contract.IndexProber, the same way maintenance_test.go reaches
// for a real component only when there is something worth faking.

import (
	"context"
	"errors"
	"testing"

	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// fakeProber is a contract.Runner that also answers contract.IndexProber,
// with a scripted (ready, hint, err) triple per repository root.
type fakeProber struct {
	id      string
	answers map[string]struct {
		ready bool
		hint  string
		err   error
	}
	probed []string
}

func (f *fakeProber) ID() string                { return f.id }
func (f *fakeProber) Serves(string) bool        { return true }
func (f *fakeProber) Implementations() []string { return nil }
func (f *fakeProber) Capabilities() []string    { return []string{"code.search"} }
func (f *fakeProber) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	return contract.Outcome{}, contract.Fail(contract.FailureNotFound, "fakeProber does not run anything")
}
func (f *fakeProber) ProbeIndex(_ context.Context, root string) (bool, string, error) {
	f.probed = append(f.probed, root)
	a := f.answers[root]
	return a.ready, a.hint, a.err
}

// fakeRunner is a contract.Runner that does NOT implement contract.IndexProber
// -- the other half of the contract: a runner with nothing to answer is left
// out of the sweep entirely, not reported as a failure.
type fakeRunner struct{ id string }

func (f *fakeRunner) ID() string                { return f.id }
func (f *fakeRunner) Serves(string) bool        { return true }
func (f *fakeRunner) Implementations() []string { return nil }
func (f *fakeRunner) Capabilities() []string    { return []string{"code.search"} }
func (f *fakeRunner) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	return contract.Outcome{}, contract.Fail(contract.FailureNotFound, "fakeRunner does not run anything")
}

func coreWithRepos(t *testing.T, runners []contract.Runner, repos ...contract.Repository) *Core {
	t.Helper()
	catalog := registry.New()
	for _, repo := range repos {
		if err := catalog.AddRepository(repo); err != nil {
			t.Fatalf("AddRepository %s: %v", repo.ID, err)
		}
	}
	return &Core{catalog: catalog, runners: runners}
}

func ready(root string) map[string]struct {
	ready bool
	hint  string
	err   error
} {
	return map[string]struct {
		ready bool
		hint  string
		err   error
	}{root: {ready: true}}
}

func TestDetectIndexesCorrectsTheCatalog(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	prober := &fakeProber{id: "codebase-memory", answers: ready(repo.Path)}
	c := coreWithRepos(t, []contract.Runner{prober}, repo)

	reports, err := c.DetectIndexes(context.Background(), "")
	if err != nil {
		t.Fatalf("DetectIndexes: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want exactly one", reports)
	}
	got := reports[0]
	if got.Repository != "api" || got.Provider != "codebase-memory" || !got.Ready || got.Hint != "" || got.Err != "" {
		t.Fatalf("report = %+v", got)
	}
	fresh, err := c.catalog.Repository("api")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if !fresh.IndexedBy("codebase-memory") {
		t.Error("a ready probe did not correct the catalog's indexed_by")
	}
}

// A runner with no index state to report is not a hole in the sweep: it is
// simply not part of it, the same as a provider health never probed.
func TestDetectIndexesSkipsRunnersWithoutIndexProber(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	prober := &fakeProber{id: "codebase-memory", answers: ready(repo.Path)}
	plain := &fakeRunner{id: "serena"}
	c := coreWithRepos(t, []contract.Runner{plain, prober}, repo)

	reports, err := c.DetectIndexes(context.Background(), "")
	if err != nil {
		t.Fatalf("DetectIndexes: %v", err)
	}
	if len(reports) != 1 || reports[0].Provider != "codebase-memory" {
		t.Fatalf("reports = %+v, want only the prober's own", reports)
	}
}

// A probe that could not reach a verdict must not correct the catalog on a
// guess: "could not tell" and "confirmed absent" are different facts, and
// only the second may touch indexed_by.
func TestDetectIndexesDoesNotCorrectTheCatalogOnProbeError(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", nil, contract.ScaleSmall, contract.VCSUnspecified, []string{"codebase-memory"})
	boom := errors.New("codebase-memory-mcp crashed")
	prober := &fakeProber{id: "codebase-memory", answers: map[string]struct {
		ready bool
		hint  string
		err   error
	}{repo.Path: {err: boom}}}
	c := coreWithRepos(t, []contract.Runner{prober}, repo)

	reports, err := c.DetectIndexes(context.Background(), "")
	if err != nil {
		t.Fatalf("DetectIndexes: %v", err)
	}
	if len(reports) != 1 || reports[0].Err != boom.Error() || reports[0].Ready {
		t.Fatalf("report = %+v", reports[0])
	}
	fresh, err := c.catalog.Repository("api")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if !fresh.IndexedBy("codebase-memory") {
		t.Error("a failed probe must leave the catalog's prior belief untouched")
	}
}

func TestDetectIndexesNarrowsToOneRepositoryWhenNamed(t *testing.T) {
	api := contract.NewRepository("api", "/srv/api", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	web := contract.NewRepository("web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	prober := &fakeProber{id: "codebase-memory", answers: map[string]struct {
		ready bool
		hint  string
		err   error
	}{api.Path: {ready: true}, web.Path: {ready: true}}}
	c := coreWithRepos(t, []contract.Runner{prober}, api, web)

	reports, err := c.DetectIndexes(context.Background(), "web")
	if err != nil {
		t.Fatalf("DetectIndexes: %v", err)
	}
	if len(reports) != 1 || reports[0].Repository != "web" {
		t.Fatalf("reports = %+v, want only web", reports)
	}
	if len(prober.probed) != 1 || prober.probed[0] != web.Path {
		t.Errorf("probed = %v, want only %s", prober.probed, web.Path)
	}
}

func TestDetectIndexesUnknownRepositoryIsNotFound(t *testing.T) {
	c := coreWithRepos(t, nil)
	if _, err := c.DetectIndexes(context.Background(), "nope"); contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not found", contract.KindOf(err))
	}
}

// A canceled context must stop the sweep before another probe runs, not
// merely refuse to start one: a caller that hit "detect" and gave up should
// not still pay for every repository left in the loop.
func TestDetectIndexesStopsOnCanceledContext(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	prober := &fakeProber{id: "codebase-memory", answers: ready(repo.Path)}
	c := coreWithRepos(t, []contract.Runner{prober}, repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reports, err := c.DetectIndexes(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(reports) != 0 {
		t.Errorf("reports = %+v, want none", reports)
	}
	if len(prober.probed) != 0 {
		t.Errorf("probed = %v, want no probe to have run", prober.probed)
	}
}
