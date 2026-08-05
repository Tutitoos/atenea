package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stubBase answers from a map instead of a disk. What matters at this seam is
// not how the numbers were stored but that they arrive, arrive per repository,
// and that a base which cannot answer does not take the commission down.
type stubBase struct {
	costs map[string]metrics.Baseline
	err   error

	mu    sync.Mutex
	asked []string
}

func (s *stubBase) Baselines(_ context.Context, capability, repository string) (map[string]metrics.Baseline, error) {
	s.mu.Lock()
	s.asked = append(s.asked, capability+"@"+repository)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.costs, nil
}

func (s *stubBase) asks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

// twins are two providers the funnel cannot tell apart on anything but cost:
// same capability, same health, no constraints. Whatever settles a choice
// between them settled it on the numbers.
func twins(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := reg.AddCapability(codeSearch()); err != nil {
		t.Fatalf("AddCapability: %v", err)
	}
	// The estimates say fast is the cheap one. Every test below either leaves
	// that unchallenged or hands the base a measurement that says otherwise.
	impls := []contract.Implementation{
		{
			ID: "fast", Provider: "fast", Capability: "code.search",
			Health: contract.Health{State: contract.HealthAlive, Score: 1},
			Cost: contract.Cost{
				Estimated: contract.Sample{Duration: 10 * time.Millisecond, Tokens: 10},
			},
		},
		{
			ID: "slow", Provider: "slow", Capability: "code.search",
			Health: contract.Health{State: contract.HealthAlive, Score: 1},
			Cost: contract.Cost{
				Estimated: contract.Sample{Duration: 90 * time.Millisecond, Tokens: 90},
			},
		},
	}
	for _, impl := range impls {
		if err := reg.AddImplementation(impl); err != nil {
			t.Fatalf("AddImplementation: %v", err)
		}
	}
	repo := contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	if err := reg.AddRepository(repo); err != nil {
		t.Fatalf("AddRepository: %v", err)
	}
	return reg
}

// priced builds an agent over the twins with a base attached, and asks one
// capability. One ask is one trip through the funnel, which is the smallest
// shape that shows what the base did to it.
func priced(t *testing.T, base orchestrator.Base) *orchestrator.Result {
	t.Helper()
	reg := twins(t)
	runner := &fakeRunner{serves: []string{"fast", "slow"}}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     reg,
		Chooser:     chooser,
		Runner:      runner,
		Checkpoints: store,
		Base:        base,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	result, err := agent.Ask(context.Background(), orchestrator.Question{
		Capability: "code.search",
		Repository: "api",
		Payload:    map[string]any{"query": "TODO"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	return result
}

// only returns the single step an ask produces, so the assertions below can
// talk about the decision without indexing into a slice every time.
func only(t *testing.T, result *orchestrator.Result) orchestrator.StepResult {
	t.Helper()
	if len(result.Steps) != 1 {
		t.Fatalf("an ask produced %d steps, want 1", len(result.Steps))
	}
	return result.Steps[0]
}

// With nothing measured, the funnel has only the settings file to go on. This
// is the cold start, and it has to keep working: a core is useful on the day
// it is installed, before it knows anything.
func TestWithNoBaseTheFunnelRanksOnTheEstimates(t *testing.T) {
	step := only(t, priced(t, nil))
	if step.Decision.Chosen.ID != "fast" {
		t.Errorf("chose %s, want the cheaper estimate", step.Decision.Chosen.ID)
	}
	if !strings.Contains(step.Decision.Reason, "estimated") {
		t.Errorf("reason %q does not admit the number was a guess", step.Decision.Reason)
	}
}

// The whole brick in one test: the settings file says fast is cheaper, the
// base says it is not, and the base wins. A design where the estimate keeps
// winning after the real numbers exist would be a design that never learns.
func TestAMeasurementOutranksTheEstimateThatContradictsIt(t *testing.T) {
	base := &stubBase{costs: map[string]metrics.Baseline{
		"fast": {
			Spent:       contract.Sample{Duration: 900 * time.Millisecond, Tokens: 900},
			Attempts:    5,
			Successes:   5,
			ToolVersion: "1.0",
		},
		"slow": {
			Spent:       contract.Sample{Duration: 20 * time.Millisecond, Tokens: 20},
			Attempts:    5,
			Successes:   5,
			ToolVersion: "1.0",
		},
	}}
	step := only(t, priced(t, base))
	if step.Decision.Chosen.ID != "slow" {
		t.Errorf("chose %s, want the one the base measured cheaper", step.Decision.Chosen.ID)
	}
	if !strings.Contains(step.Decision.Reason, "measured") {
		t.Errorf("reason %q does not say the numbers were real", step.Decision.Reason)
	}
}

// Cost is asked for per repository because cost is not a property of a tool.
// A base asked globally would hand a warm-index figure to a repository that
// has no index, which is the confident kind of wrong.
func TestTheBaseIsAskedForThisRepositoryAndCapability(t *testing.T) {
	base := &stubBase{}
	priced(t, base)
	asks := base.asks()
	if len(asks) != 1 || asks[0] != "code.search@api" {
		t.Errorf("the base was asked %v, want one ask for code.search@api", asks)
	}
}

// A base that cannot be read is a degraded funnel, not a failed commission.
// The work still has estimates to rank on, so it goes ahead -- but silently
// going ahead would leave a decision nobody could reproduce afterwards.
func TestAnUnreadableBaseIsAnnouncedAndTheWorkStillHappens(t *testing.T) {
	base := &stubBase{err: errors.New("database is locked")}
	step := only(t, priced(t, base))
	if step.Review.Child != contract.VerdictOK {
		t.Errorf("verdict %v, want the step to have run anyway", step.Review.Child)
	}
	found := ""
	for _, notice := range step.Decision.Notices {
		if strings.Contains(notice, "database is locked") {
			found = notice
		}
	}
	if found == "" {
		t.Fatalf("notices %v never mention why the base was skipped", step.Decision.Notices)
	}
	if !strings.Contains(found, "declared estimates") {
		t.Errorf("notice %q does not say what was ranked on instead", found)
	}
}

// A provider the base has never seen keeps the estimate it declared. Zeroing
// it instead would make every newcomer look free and win every choice it was
// offered, which is the opposite of what break-in mode is for.
func TestAnUnmeasuredProviderKeepsItsDeclaredEstimate(t *testing.T) {
	base := &stubBase{costs: map[string]metrics.Baseline{
		"fast": {
			Spent:       contract.Sample{Duration: 900 * time.Millisecond, Tokens: 900},
			Attempts:    5,
			Successes:   5,
			ToolVersion: "1.0",
		},
	}}
	step := only(t, priced(t, base))
	// slow has no measurements at all, so it is owed the base its first ones
	// and the break-in turn hands it the work regardless of the two figures.
	if step.Decision.Chosen.ID != "slow" {
		t.Fatalf("chose %s, want the unmeasured one to get its turn", step.Decision.Chosen.ID)
	}
	chosen := step.Decision.Chosen
	if chosen.Cost.Estimated.Tokens != 90 {
		t.Errorf("the estimate arrived as %d tokens, want the declared 90",
			chosen.Cost.Estimated.Tokens)
	}
	if chosen.Cost.Samples != 0 {
		t.Errorf("an unmeasured provider arrived with %d samples", chosen.Cost.Samples)
	}
}
