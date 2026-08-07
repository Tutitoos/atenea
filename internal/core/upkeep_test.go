package core_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The receipt sweep deletes every *.tmp it finds, on the grounds that an
// interrupted dump is a record of a run that never happened that way. That is
// true of a dump nobody is writing. Store.Recover holds a mutex for the whole
// pass and its comment says why: "so that a dump being written right now cannot
// have its temporary file swept out from under the rename". A mutex cannot do
// that job across processes, and the sweep runs in core.New -- so every command
// run beside a live service is a second sweep of the service's directory.
//
// The cost is a run losing its receipt for a reason that has nothing to do with
// the run: the temp is deleted, the rename that follows fails ENOENT, and the
// dump is reported as a checkpoint write failure.
//
// The fix is not a wider lock. Sweeping is upkeep, and upkeep belongs to the one
// process responsible for the state on disk. These two tests are the same fact
// from both sides: the service sweeps, a command does not.

// plantInFlightDump writes the file a live writer would have open: Save creates
// <id>.<rand>.tmp and renames it, so this is what sits there mid-write.
func plantInFlightDump(t *testing.T, stateHome string) string {
	t.Helper()
	dir := filepath.Join(stateHome, "atenea", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("runs dir: %v", err)
	}
	path := filepath.Join(dir, checkpoint.NewID(time.Now())+".7f3a91.tmp")
	if err := os.WriteFile(path, []byte(`{"id":"in flight`), 0o600); err != nil {
		t.Fatalf("plant: %v", err)
	}
	return path
}

func TestACommandCoreDoesNotSweepADumpInFlight(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	inFlight := plantInFlightDump(t, stateHome)

	cfg := loadForUpkeep(t)
	if _, err := core.New(cfg, core.Command); err != nil {
		t.Fatalf("core.New: %v", err)
	}

	if _, err := os.Stat(inFlight); err != nil {
		t.Errorf("a command swept a dump it does not own: %v", err)
	}
}

func TestTheServiceCoreSweepsADumpNobodyIsWriting(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	abandoned := plantInFlightDump(t, stateHome)

	cfg := loadForUpkeep(t)
	if _, err := core.New(cfg, core.Service); err != nil {
		t.Fatalf("core.New: %v", err)
	}

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("the service left an interrupted dump behind: %v", err)
	}
}

func loadForUpkeep(t *testing.T) config.Config {
	t.Helper()
	path := writeTemp(t, catalog)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// holdUpkeepEnv turns this test binary into the service that got there first,
// and heldMark is how it says the upkeep is claimed. The claim has to be made
// by a real second process: a lock that only works inside one process is the
// thing being replaced here, so a second Core built in this one would pass
// against the bug it exists to catch.
const (
	holdUpkeepEnv = "ATENEA_TEST_HOLD_UPKEEP"
	heldMark      = "upkeep.held"
)

// Only one process may perform upkeep, and the role alone cannot promise it:
// two services started by hand would both sweep and both tick, which is the
// same collision with a different cause. So the claim is made on disk, and the
// second service is told no.
//
// Metrics are off in this test on purpose. The base has a lock of its own and
// two processes contending for it would answer slowly instead of clearly; what
// is under test is the upkeep claim, so nothing else is allowed to be the
// reason the second one fails.
func TestASecondServiceIsRefusedTheUpkeep(t *testing.T) {
	if root := os.Getenv(holdUpkeepEnv); root != "" {
		holdUpkeep(t, root)
		return
	}
	root := t.TempDir()
	settleUpkeepEnv(t, root)

	holder := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=60s")
	holder.Env = append(os.Environ(), holdUpkeepEnv+"="+root)
	if err := holder.Start(); err != nil {
		t.Fatalf("starting the first service: %v", err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill() })
	waitForMark(t, filepath.Join(root, heldMark))

	cfg := upkeepSettings(t, root)
	_, err := core.New(cfg, core.Service)
	if err == nil {
		t.Fatal("a second service was allowed to take over the upkeep")
	}
	if kind := contract.KindOf(err); kind != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable: err = %v", kind, err)
	}
	// The refusal has to name the holder, and that is the whole reason the claim
	// is a file with a pid in it rather than a kernel lock: "no" without a pid
	// leaves an operator with two atenea processes and no way to tell which one
	// to stop.
	if pid := strings.TrimSpace(readMark(t, filepath.Join(root, heldMark))); !strings.Contains(err.Error(), pid) {
		t.Errorf("the refusal does not name the holder pid %s: %v", pid, err)
	}

	// The refusal is for the upkeep, not for using Atenea. Every command still
	// works beside a running service; that is the whole point of the split.
	atenea, err := core.New(cfg, core.Command)
	if err != nil {
		t.Fatalf("a command was refused beside a live service: %v", err)
	}
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// A restart is the most ordinary thing an operator does, and it is two services
// in a row: the one stopping has to let go before the next one asks. This runs in
// one process on purpose -- the claim would see its own pid alive in the file and
// refuse, so nothing but a real release can make the second start work.
func TestAServiceThatStopsReleasesTheUpkeep(t *testing.T) {
	root := t.TempDir()
	settleUpkeepEnv(t, root)
	cfg := upkeepSettings(t, root)

	first, err := core.New(cfg, core.Service)
	if err != nil {
		t.Fatalf("first service: %v", err)
	}
	if err := first.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	second, err := core.New(cfg, core.Service)
	if err != nil {
		t.Fatalf("a stopped service kept the upkeep: %v", err)
	}
	if err := second.Shutdown(); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// A lock only excludes people who look in the same place. XDG_RUNTIME_DIR is
// set for a systemd --user service and for a login shell, and unset under cron
// -- so a claim that lived there would put two services in two different files
// and let both of them sweep and tick, which is the bug this whole change
// exists to stop. The claim goes where everybody agrees: the state root, which
// is derived from HOME.
func TestTheUpkeepClaimIgnoresTheRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	cfg := upkeepSettings(t, root)

	first, err := core.New(cfg, core.Service)
	if err != nil {
		t.Fatalf("first service: %v", err)
	}
	defer func() { _ = first.Shutdown() }()

	// The same installation, reached by a process that has no runtime directory.
	// Nothing about the state root changed, so the answer must not change.
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := core.New(cfg, core.Service); err == nil {
		t.Fatal("a service with no XDG_RUNTIME_DIR claimed the upkeep a second time")
	} else if kind := contract.KindOf(err); kind != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable: err = %v", kind, err)
	}
}

// A service killed outright cannot release anything on its way down, and the
// upkeep it held must not stay claimed forever: the next start would be refused
// on behalf of a process that no longer exists.
func TestUpkeepHeldByAProcessThatDiedIsClaimable(t *testing.T) {
	if root := os.Getenv(holdUpkeepEnv); root != "" {
		holdUpkeep(t, root)
		return
	}
	root := t.TempDir()
	settleUpkeepEnv(t, root)

	holder := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=60s")
	holder.Env = append(os.Environ(), holdUpkeepEnv+"="+root)
	if err := holder.Start(); err != nil {
		t.Fatalf("starting the first service: %v", err)
	}
	waitForMark(t, filepath.Join(root, heldMark))
	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = holder.Wait()

	atenea, err := core.New(upkeepSettings(t, root), core.Service)
	if err != nil {
		t.Fatalf("the upkeep stayed claimed by a dead service: %v", err)
	}
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// holdUpkeep is the service that got there first: claim the upkeep, say so, and
// keep holding it until the parent has asked its question and killed us.
func holdUpkeep(t *testing.T, root string) {
	t.Helper()
	settleUpkeepEnv(t, root)
	if _, err := core.New(upkeepSettings(t, root), core.Service); err != nil {
		t.Fatalf("the first service could not start: %v", err)
	}
	// The mark carries the pid so the parent can check the refusal names it.
	mark := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(root, heldMark), []byte(mark), 0o600); err != nil {
		t.Fatalf("mark: %v", err)
	}
	time.Sleep(30 * time.Second)
}

// settleUpkeepEnv points both sides at the same installation: the same state
// root, and the same runtime directory the claim is made in.
func settleUpkeepEnv(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
}

func upkeepSettings(t *testing.T, root string) config.Config {
	t.Helper()
	path := filepath.Join(root, "atenea.toml")
	if err := os.WriteFile(path, []byte(catalog+"\n[metrics]\nenabled = false\n"), 0o600); err != nil {
		t.Fatalf("settings: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func waitForMark(t *testing.T, path string) {
	t.Helper()
	for range 200 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the first service never said it had the upkeep")
}

func readMark(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the mark: %v", err)
	}
	return string(raw)
}
