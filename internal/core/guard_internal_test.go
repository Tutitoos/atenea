package core

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// probingRunner is a Runner that also answers IndexProber, which is what makes
// guard wrap it in a guardedProber rather than a plain guardedRunner.
type probingRunner struct {
	probed chan string
	answer bool
}

func (p *probingRunner) ID() string                { return "fake" }
func (p *probingRunner) Serves(id string) bool     { return id == "fake.status" }
func (p *probingRunner) Implementations() []string { return []string{"fake.status"} }
func (p *probingRunner) Capabilities() []string    { return []string{"graph.status"} }
func (p *probingRunner) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	return contract.Outcome{}, nil
}

func (p *probingRunner) ProbeIndex(_ context.Context, root string) (bool, string, error) {
	p.probed <- root
	return p.answer, "fake", nil
}

// The probe has to be bracketed the way a call is.
//
// On an on_demand process a probe is usually the first thing to wake the
// server, and without Acquire before EnsureReady the idle reaper is free to
// stop it in the gap -- turning "is there an index" into "the provider is
// down". This path had no test at all, which is how the Acquire and the
// EnsureReady came to be in the wrong order in the first place.
func TestAProbeHoldsTheProcessTheWayACallDoes(t *testing.T) {
	procs, err := supervisor.New()
	if err != nil {
		t.Fatalf("supervisor.New: %v", err)
	}
	runner := &probingRunner{probed: make(chan string, 1), answer: true}
	guarded := guard(runner, procs, func(contract.Repository) string { return "absent" })

	prober, ok := guarded.(contract.IndexProber)
	if !ok {
		t.Fatalf("guard returned %T, want something that still probes: a runner "+
			"that loses IndexProber on the way through the guard stops being asked at all", guarded)
	}

	// No such process is supervised, so EnsureReady refuses and the probe
	// never reaches the runner. That is the contract: an unknown instance is
	// an error about the machine, not a claim about the index.
	ready, provider, err := prober.ProbeIndex(context.Background(), t.TempDir())
	if err == nil {
		t.Fatalf("a probe against an unsupervised process answered %v/%q instead of failing",
			ready, provider)
	}
	select {
	case root := <-runner.probed:
		t.Errorf("the runner was probed at %q despite its process not being ready", root)
	default:
	}
}

// A runner with no index to probe must come back as itself, or the guard would
// be quietly adding a capability the provider does not have.
func TestARunnerThatDoesNotProbeIsNotGivenAProbe(t *testing.T) {
	procs, err := supervisor.New()
	if err != nil {
		t.Fatalf("supervisor.New: %v", err)
	}
	guarded := guard(plainRunner{}, procs, func(contract.Repository) string { return "x" })
	if _, ok := guarded.(contract.IndexProber); ok {
		t.Error("the guard turned a runner with no index probe into one that has one")
	}
}

type plainRunner struct{}

func (plainRunner) ID() string                { return "plain" }
func (plainRunner) Serves(id string) bool     { return id == "ripgrep" }
func (plainRunner) Implementations() []string { return []string{"ripgrep"} }
func (plainRunner) Capabilities() []string    { return []string{"code.search"} }
func (plainRunner) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	return contract.Outcome{}, nil
}

// The viewer and the MCP child are two processes, and the spec says so.
//
// Sharing an id or a transport between them would make the dashboard's health
// stand in for the MCP child's: the viewer answers HTTP on a port while the
// child that actually serves symbols is the one being supervised, so a dead
// child behind a live viewer would read as healthy.
func TestTheKivgraphViewerIsSupervisedAsItsOwnHTTPProcess(t *testing.T) {
	spec := kivgraphDashboardSpec(config.ManagedProcess{
		Command:      "kivgraph",
		Args:         []string{"dashboard"},
		Lifecycle:    supervisor.Persistent,
		Port:         8080,
		RestartLimit: 2,
		RestartDelay: time.Second,
		StableAfter:  time.Minute,
		ReadyTimeout: 5 * time.Second,
		StopGrace:    time.Second,
	})

	if spec.ID == "kivgraph" {
		t.Error("the viewer shares the MCP child's process id: a dead child behind " +
			"a live viewer would read as healthy")
	}
	if spec.ID != "kivgraph-dashboard" {
		t.Errorf("id = %q, want the viewer's own", spec.ID)
	}
	if spec.Readiness != supervisor.ReadinessHTTP {
		t.Errorf("readiness = %v, want HTTP: the viewer speaks HTTP and the MCP child speaks stdio",
			spec.Readiness)
	}
	if spec.EndpointPath != "/" {
		t.Errorf("endpoint = %q, want the viewer's root", spec.EndpointPath)
	}
	if spec.Port != 8080 {
		t.Errorf("port = %d, want the one the settings declared", spec.Port)
	}
}
