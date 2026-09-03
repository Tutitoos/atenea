package core_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The kivgraph and tokensave wiring in this package had no test at all, while
// the fixture code it sits beside was fully covered -- so the four refusals
// below, each of which the source argues for at length, were only ever
// exercised by somebody editing a settings file by hand. What makes them worth
// pinning is that every one of them is a refusal: the code's opinion is that
// these settings are wrong, and an untested opinion is one that can be deleted
// by accident and noticed by nobody.
//
// startupFor loads one settings body and returns whatever core.New made of it,
// which is where all four decisions are taken.
func startupFor(t *testing.T, runners, extra string) error {
	t.Helper()
	body := strings.Replace(socketSettings, `runners = ["local"]`, runners, 1) + extra
	cfg, err := config.Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_, err = core.New(cfg, core.Command)
	return err
}

// A supervised stdio server has no address, only two pipes, so naming the
// runner without a process block declares a far side nothing could ever dial.
// Refused at startup rather than at the first call, because the first call is
// somebody's real work and the settings file is the thing that is wrong.
//
// kivgraph used to be in this list and no longer belongs in it: 0.9.2 serves
// the same tools from `kivgraph daemon` over HTTP, so a file naming that runner
// with no process block means the daemon, not a mistake. See
// TestKivgraphWithNoProcessBlockDialsTheDaemonInstead.
func TestAStdioRunnerWithNoProcessBlockIsRefusedAtStartup(t *testing.T) {
	for _, runner := range []string{"tokensave"} {
		t.Run(runner, func(t *testing.T) {
			err := startupFor(t, `runners = ["local", "`+runner+`"]`, "")
			if err == nil {
				t.Fatalf("a settings file naming %s with no process block started a core", runner)
			}
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Errorf("kind = %v, want invalid_input: the settings file is what is wrong", got)
			}
			if !strings.Contains(err.Error(), "no address to dial") {
				t.Errorf("err = %v, want it to say why a stdio server needs a process block", err)
			}
		})
	}
}

// The other half of that retirement, and the reason the runner has two modes at
// all: with no process block kivgraph is reached at an endpoint, so the core
// starts. Nothing is dialed here -- building a runner opens no socket -- which
// is exactly what makes this a startup assertion and not a daemon test.
func TestKivgraphWithNoProcessBlockDialsTheDaemonInstead(t *testing.T) {
	if err := startupFor(t, `runners = ["local", "kivgraph"]`, ""); err != nil {
		t.Fatalf("kivgraph with no process block was refused: %v", err)
	}
}

// kivgraph publishes one global corpus read by every repository alike, so
// per_repository has no meaning for it: it is refused rather than silently
// collapsed into the one shared server, because a caller who wrote it meant
// something the graph cannot do.
func TestPerRepositoryIsRefusedForKivgraph(t *testing.T) {
	err := startupFor(t, `runners = ["local", "kivgraph"]`,
		"\n  [orchestrator.kivgraph.process]\n  command = \"/bin/true\"\n"+
			"  lifecycle = \"on_demand\"\n  args = [\"{{project}}\"]\n  instance = \"per_repository\"\n")
	if err == nil {
		t.Fatal("kivgraph accepted instance = per_repository")
	}
	if !strings.Contains(err.Error(), "one global graph") {
		t.Errorf("err = %v, want it to explain that the graph is global", err)
	}
	if !strings.Contains(err.Error(), string(config.InstanceShared)) {
		t.Errorf("err = %v, want it to name the policy that is meaningful", err)
	}
}

// tokensave is the same refusal read the other way round: it serves one rooted
// project, and a second copy pointed at the same root would index the same
// files twice into the same database.
func TestPerRepositoryIsRefusedForTokensave(t *testing.T) {
	err := startupFor(t, `runners = ["local"]`,
		"\n[orchestrator.tokensave]\nroot = \"/tmp\"\n\n  [orchestrator.tokensave.process]\n"+
			"  command = \"/bin/true\"\n  lifecycle = \"on_demand\"\n"+
			"  args = [\"{{project}}\"]\n  instance = \"per_repository\"\n")
	if err == nil {
		t.Fatal("tokensave accepted instance = per_repository")
	}
	if !strings.Contains(err.Error(), "one rooted") {
		t.Errorf("err = %v, want it to explain that tokensave serves one root", err)
	}
	// Refused from the supervisor's own build, so it does not depend on
	// tokensave being one of the declared runners: a process Atenea would
	// launch is refused whether or not anything is going to talk to it.
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", got)
	}
}

// The root is not optional and not guessable. tokensave speaks paths relative
// to the project it serves, in both directions, so a core built without one
// would be translating against a beginning it invented.
func TestTokensaveWithoutARootIsRefused(t *testing.T) {
	err := startupFor(t, `runners = ["local", "tokensave"]`,
		"\n  [orchestrator.tokensave.process]\n  command = \"/bin/true\"\n  lifecycle = \"on_demand\"\n")
	if err == nil {
		t.Fatal("tokensave started with no root declared")
	}
	if !strings.Contains(err.Error(), "orchestrator.tokensave.root is required") {
		t.Errorf("err = %v, want it to name the key that is missing", err)
	}
	// And the same declaration with a root is accepted, so the refusal above
	// is about the missing root and not about the block being unbuildable.
	if err := startupFor(t, `runners = ["local", "tokensave"]`,
		"\n[orchestrator.tokensave]\nroot = \"/tmp\"\n\n  [orchestrator.tokensave.process]\n"+
			"  command = \"/bin/true\"\n  lifecycle = \"on_demand\"\n"); err != nil {
		t.Errorf("a complete tokensave declaration was refused: %v", err)
	}
}
