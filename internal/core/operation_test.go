package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
)

// The shared catalog leaves graph.search without a health block, which reads
// as unknown and paints the whole screen amber before any of these tests have
// done anything. Health is not what is under test here, so it is pinned: with
// every provider declaring itself alive, an amber light can only have come
// from the copies or the repair, which is what makes asserting on it worth
// anything.
//
// The block is spliced in where graph.search is declared rather than appended,
// because a sub-table written after the repositories would be extending the
// wrong entry.
var healthy = strings.Replace(catalog,
	`  requires_index = true
  min_scale = "large"`,
	`  requires_index = true
  min_scale = "large"

  [implementation.health]
  state = "alive"
  score = 0.8`, 1)

// Copying is a background lane like the other two, and the screen is where a
// lane says what it has been doing. A rhythm that exists only in the settings
// file is a rhythm nobody can prove is running.
func TestCopyingIsOneOfTheBackgroundLanes(t *testing.T) {
	atenea := build(t, healthy)
	defer func() { _ = atenea.Shutdown() }()

	lanes := make([]string, 0, 3)
	for _, lane := range atenea.Status().Maintenance {
		lanes = append(lanes, lane.Name)
	}
	slices.Sort(lanes)
	want := []string{"backup", "metrics.compact", "metrics.flush"}
	if !slices.Equal(lanes, want) {
		t.Errorf("lanes = %v, want %v", lanes, want)
	}
}

// Switching copying off must leave the other two rhythms alone. The lanes
// share one clock so that they cannot fight over the disk, and a shared clock
// is exactly where turning one thing off takes the others with it.
func TestTurningCopyingOffLeavesTheOtherLanes(t *testing.T) {
	atenea := build(t, healthy+"\n[backup]\nenabled = false\n")
	defer func() { _ = atenea.Shutdown() }()

	status := atenea.Status()
	lanes := make([]string, 0, 2)
	for _, lane := range status.Maintenance {
		lanes = append(lanes, lane.Name)
	}
	slices.Sort(lanes)
	if want := []string{"metrics.compact", "metrics.flush"}; !slices.Equal(lanes, want) {
		t.Errorf("lanes = %v, want %v", lanes, want)
	}
	if status.Backups.Enabled {
		t.Error("the screen says copying is on after it was switched off")
	}
	if status.Light != core.LightGreen {
		t.Errorf("light = %v: switching copying off is a choice, not a fault", status.Light)
	}
}

// The screen counts what is on disk, not what the core believes. Somebody
// deleting the folder behind Atenea's back is precisely the case where a
// remembered number would keep saying five copies are safe.
func TestTheScreenCountsTheCopiesThatAreThere(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	dir := filepath.Join(root, "copies")
	atenea, err := core.New(load(t, healthy+"\n[backup]\ndir = \""+dir+"\"\n"))
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = atenea.Shutdown() }()

	if got := atenea.Status().Backups.Count; got != 0 {
		t.Fatalf("a fresh install reported %d copies", got)
	}
	// Two snapshots by hand, named the way the store names them, then one
	// removed from underneath.
	for _, name := range []string{"20260801T000000Z", "20260801T060000Z"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if got := atenea.Status().Backups.Count; got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	if err := os.RemoveAll(filepath.Join(dir, "20260801T060000Z")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := atenea.Status().Backups.Count; got != 1 {
		t.Errorf("count = %d after a copy was deleted, want 1", got)
	}
}

// A copying rhythm that stopped is amber, and the screen has to reach that on
// its own from what is on disk. This is the one fault in the whole operation
// story that produces no error anywhere: the job is not failing, it is simply
// not happening.
func TestACopyingRhythmThatStoppedShowsAmber(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	dir := filepath.Join(root, "copies")
	atenea, err := core.New(load(t, healthy+"\n[backup]\ndir = \""+dir+"\"\nevery = \"1h\"\n"))
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = atenea.Shutdown() }()

	// One period late is timing, not a fault: the beat that takes the copy
	// can be seconds behind and a screen that flapped amber on that would
	// train the operator to ignore it.
	recent := snapshot(t, dir, time.Now().Add(-90*time.Minute))
	if got := atenea.Status().Light; got != core.LightGreen {
		t.Errorf("light = %v one period late, want green", got)
	}

	if err := os.RemoveAll(filepath.Join(dir, recent)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	snapshot(t, dir, time.Now().Add(-5*time.Hour))
	status := atenea.Status()
	if status.Light != core.LightAmber {
		t.Errorf("light = %v with copying five hours overdue, want amber", status.Light)
	}
	if status.Backups.Count != 1 {
		t.Errorf("count = %d, want the stale copy still counted", status.Backups.Count)
	}
}

// An ugly close is repaired before any work is accepted, and the repair is
// visible: amber, a line on the screen, and an entry in the crash notebook.
// Green here would hide that yesterday's history is shorter than it was.
func TestAnUglyCloseIsRepairedAndReported(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	runs := filepath.Join(root, "atenea", "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// What a SIGKILL mid-write leaves: a temporary file that never got its
	// final name, and a receipt whose JSON stops in the middle.
	if err := os.WriteFile(filepath.Join(runs, ".20260801T000000-aaaaaa.json.tmp"), []byte(`{"id":"x"`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runs, "20260801T000001-bbbbbb.json"), []byte(`{"id":"y","ste`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	atenea, err := core.New(load(t, healthy))
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = atenea.Shutdown() }()

	status := atenea.Status()
	if status.Recovered.Clean() {
		t.Fatal("the start after an ugly close reported nothing to repair")
	}
	if status.Recovered.Swept != 1 {
		t.Errorf("swept = %d, want the interrupted dump", status.Recovered.Swept)
	}
	if status.Recovered.Torn != 1 {
		t.Errorf("torn = %d, want the half-written receipt", status.Recovered.Torn)
	}
	if status.Light != core.LightAmber {
		t.Errorf("light = %v after a repair, want amber", status.Light)
	}
	if !strings.Contains(status.Recovered.Summary(), "swept") {
		t.Errorf("summary says nothing about the sweep: %q", status.Recovered.Summary())
	}
	if status.Incidents.Latest.IsZero() {
		t.Error("the repair was not written to the crash notebook")
	}
}

// A clean stop leaves nothing to repair, and the next start must say so by
// being green. This is the case that runs every day; if it were amber the
// warning above would mean nothing.
func TestAStartAfterACleanStopIsGreen(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	settings := load(t, healthy)

	first, err := core.New(settings)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if err := first.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	second, err := core.New(settings)
	if err != nil {
		t.Fatalf("second core.New: %v", err)
	}
	defer func() { _ = second.Shutdown() }()

	status := second.Status()
	if !status.Recovered.Clean() {
		t.Errorf("a clean stop left work for the next start: %s", status.Recovered.Summary())
	}
	if status.Light != core.LightGreen {
		t.Errorf("light = %v after a clean stop, want green", status.Light)
	}
}

// A receipt that survives the repair has to still be readable afterwards. The
// sweep walks the same directory the good records live in, and a repair that
// took a healthy run with it would be worse than the fault it fixes.
func TestARepairLeavesTheGoodReceiptsAlone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	runs := filepath.Join(root, "atenea", "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	good := checkpoint.Run{ID: "20260801T000002-cccccc", Task: "find every TODO"}
	body, err := json.Marshal(good)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	kept := filepath.Join(runs, good.ID+".json")
	if err := os.WriteFile(kept, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runs, "20260801T000003-dddddd.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	atenea, err := core.New(load(t, healthy))
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = atenea.Shutdown() }()

	raw, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("the good receipt did not survive the repair: %v", err)
	}
	var back checkpoint.Run
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the good receipt is no longer readable: %v", err)
	}
	if back.Task != good.Task {
		t.Errorf("task = %q, want %q", back.Task, good.Task)
	}
}

func load(t *testing.T, body string) config.Config {
	t.Helper()
	cfg, err := config.Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// snapshot puts a folder on disk that looks exactly like one the backup store
// took. The name is the record: the store reads a copy's age off the folder
// name and never off the mtime, so a helper that set the clock instead of the
// name would be timestamping a copy the store dates to whenever the name says.
func snapshot(t *testing.T, dir string, at time.Time) string {
	t.Helper()
	name := at.UTC().Format("20060102T150405Z")
	if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return name
}

// filedIncident leaves exactly one real incident on disk and hands back the
// settings that produced it: a copying lane pointed at a folder nobody may
// write in, beaten until it fails, filed by the wrapper that exists for jobs
// nobody is waiting on. The core that ran it is stopped before returning, so
// whoever reads next is a genuinely different process with nothing in memory.
func filedIncident(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	dir := filepath.Join(root, "copies")
	settings := load(t, healthy+"\n[backup]\ndir = \""+dir+"\"\nevery = \"1s\"\n")

	atenea, err := core.New(settings)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if got := atenea.Status().Light; got != core.LightGreen {
		t.Fatalf("light = %v before anything went wrong, want green", got)
	}
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// The rhythm only beats while something holds the core up, which is the
	// service this is standing in for.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := atenea.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	return settings
}

// A background lane that fails reaches the screen across processes.
//
// This is the case the design cares most about: the failing job is inside a
// service nobody is watching, and the operator finds out from a different
// process entirely. The notebook is on disk, which is what makes that possible
// -- an in-memory tally would leave the light green in every process but the
// one that already knows.
func TestAFailingBackgroundLaneTurnsTheLightAmberInEveryProcess(t *testing.T) {
	settings := filedIncident(t)

	reader, err := core.New(settings)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = reader.Shutdown() }()

	status := reader.Status()
	if status.Incidents.Unread == 0 {
		t.Fatal("the failure never reached the notebook")
	}
	if status.Light != core.LightAmber {
		t.Errorf("light = %v in a process that did not run the lane, want amber", status.Light)
	}
	// Amber, never red: copying failing does not stop Atenea answering, and a
	// red light would send somebody looking for an outage that is not there.
	if status.Light == core.LightRed {
		t.Error("a failed background lane was reported as an outage")
	}
}

// Saying the incidents have been read clears the light. Without that the amber
// is permanent and stops meaning anything -- the operator learns to read past
// it, which is the failure mode a warning light exists to avoid. The next beat
// that fails files a new one and the light comes back, which is what makes
// "unread" the honest word for it.
func TestReadingTheIncidentsClearsTheLight(t *testing.T) {
	settings := filedIncident(t)

	reader, err := core.New(settings)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = reader.Shutdown() }()

	if got := reader.Status().Light; got != core.LightAmber {
		t.Fatalf("light = %v with an unread incident, want amber", got)
	}
	cleared, err := reader.ClearIncidents()
	if err != nil {
		t.Fatalf("ClearIncidents: %v", err)
	}
	if cleared == 0 {
		t.Error("nothing was marked read")
	}
	if got := reader.Status().Light; got != core.LightGreen {
		t.Errorf("light = %v after the incidents were read, want green", got)
	}
}
