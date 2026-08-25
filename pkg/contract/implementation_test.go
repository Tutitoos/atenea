package contract_test

import (
	"math"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func ripgrep() contract.Implementation {
	return contract.Implementation{
		ID:         "ripgrep",
		Provider:   "ripgrep",
		Capability: "code.search",
		Cost: contract.Cost{
			Estimated: contract.Sample{Duration: 80 * time.Millisecond, Tokens: 400},
		},
	}
}

func TestImplementationValidateAcceptsAPlainProvider(t *testing.T) {
	if err := ripgrep().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestImplementationValidateRejectsBadDefinitions(t *testing.T) {
	cases := map[string]func(*contract.Implementation){
		"uppercase id":        func(i *contract.Implementation) { i.ID = "RipGrep" },
		"empty provider":      func(i *contract.Implementation) { i.Provider = "" },
		"undotted capability": func(i *contract.Implementation) { i.Capability = "search" },
		"uppercase language":  func(i *contract.Implementation) { i.Constraints.Languages = []string{"Go"} },
		"reserved prefix":     func(i *contract.Implementation) { i.ID = "raw.serena.find_symbol" },
		"inverted scale": func(i *contract.Implementation) {
			i.Constraints.MinScale = contract.ScaleLarge
			i.Constraints.MaxScale = contract.ScaleSmall
		},
		"health score above one": func(i *contract.Implementation) { i.Health.Score = 1.5 },
		// NaN is listed beside the range cases because a range test cannot
		// catch it: every comparison against NaN is false, so `< 0 || > 1`
		// reads as a bound and admits the one value outside every bound. A
		// NaN score loses every tie-break it enters, so the provider carrying
		// it is ranked last for ever and the funnel never explains why.
		"health score of NaN": func(i *contract.Implementation) { i.Health.Score = math.NaN() },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			impl := ripgrep()
			mutate(&impl)
			if err := impl.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// Cost is hybrid on purpose: the estimate gets things moving, and real
// measurements take over the moment there are enough of them.
func TestCostPrefersMeasurementsOnceThereAreEnough(t *testing.T) {
	cost := contract.Cost{
		Estimated: contract.Sample{Duration: time.Second, Tokens: 1000},
		Measured:  contract.Sample{Duration: 90 * time.Millisecond, Tokens: 120},
		Samples:   1,
	}
	if got := cost.Effective(2); got != cost.Estimated {
		t.Fatalf("with 1 of 2 samples the estimate must still rule, got %+v", got)
	}
	cost.Samples = 2
	if got := cost.Effective(2); got != cost.Measured {
		t.Fatalf("with enough samples the measurement must rule, got %+v", got)
	}
}

func TestHealthRankOrdersTheFunnel(t *testing.T) {
	order := []contract.HealthState{
		contract.HealthAlive,
		contract.HealthDegraded,
		contract.HealthUnknown,
		contract.HealthDown,
	}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() >= order[i].Rank() {
			t.Fatalf("%s should rank before %s", order[i-1], order[i])
		}
	}
}

// Degraded means slow or working without its index, not dead. It has to survive
// the funnel, otherwise a warm-up period would look like an outage.
func TestOnlyDownIsUnusable(t *testing.T) {
	usable := map[contract.HealthState]bool{
		contract.HealthAlive:    true,
		contract.HealthDegraded: true,
		contract.HealthUnknown:  true,
		contract.HealthDown:     false,
	}
	for state, want := range usable {
		if got := (contract.Health{State: state}).Usable(); got != want {
			t.Errorf("%s.Usable() = %v, want %v", state, got, want)
		}
	}
}

func TestParseScaleAndHealthState(t *testing.T) {
	if s, err := contract.ParseScale(""); err != nil || s != contract.ScaleUnspecified {
		t.Fatalf("empty scale = %v, %v", s, err)
	}
	if s, err := contract.ParseScale("large"); err != nil || s != contract.ScaleLarge {
		t.Fatalf("large = %v, %v", s, err)
	}
	if _, err := contract.ParseScale("huge"); err == nil {
		t.Fatal("unknown scale should fail")
	}
	if h, err := contract.ParseHealthState(""); err != nil || h != contract.HealthUnknown {
		t.Fatalf("empty state = %v, %v", h, err)
	}
	if _, err := contract.ParseHealthState("sick"); err == nil {
		t.Fatal("unknown state should fail")
	}
}

func TestParseVCS(t *testing.T) {
	if v, err := contract.ParseVCS(""); err != nil || v != contract.VCSUnspecified {
		t.Fatalf("empty vcs = %v, %v", v, err)
	}
	if v, err := contract.ParseVCS("present"); err != nil || v != contract.VCSPresent {
		t.Fatalf("present = %v, %v", v, err)
	}
	if v, err := contract.ParseVCS("absent"); err != nil || v != contract.VCSAbsent {
		t.Fatalf("absent = %v, %v", v, err)
	}
	if _, err := contract.ParseVCS("maybe"); err == nil {
		t.Fatal("unknown vcs should fail")
	}
}

func TestImplementationCloneDoesNotShareLanguages(t *testing.T) {
	original := ripgrep()
	original.Constraints.Languages = []string{"go"}
	clone := original.Clone()
	clone.Constraints.Languages[0] = "rust"
	if original.Constraints.Languages[0] != "go" {
		t.Fatal("clone shared the language slice")
	}
}
