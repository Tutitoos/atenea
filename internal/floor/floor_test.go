package floor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func measurement(repo, agent, model string) floor.Measurement {
	return floor.Measurement{
		Repository:       repo,
		Agent:            agent,
		Model:            model,
		USD:              0.35,
		PrefixTokens:     25340,
		USDPerToken:      0.35 / 25340,
		CacheWriteTokens: 25340,
		InputTokens:      120,
		OutputTokens:     40,
		Cold:             true,
		CLIVersion:       "claude-code/1.2.3",
		MeasuredAt:       time.Date(2026, 8, 14, 19, 40, 0, 0, time.UTC),
	}
}

// A Put has to survive being read back by a different Store over the same
// path: the workflow engine that checks a floor and the `atenea floor
// measure` that wrote it are two different process runs, never one Store
// instance held open across both.
func TestPutAndGetRoundTripThroughARealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "floors.json")
	writer, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := measurement("atenea", "explore", "claude-opus-5")
	if err := writer.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reader, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, ok, err := reader.Get("atenea", "explore", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want the row Put just wrote")
	}
	if got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
}

// A re-measurement of the same pair is a replacement, not an addition -- a
// row half from one CLI version and half from another would not describe
// anything that was ever actually measured.
func TestPutReplacesTheSamePairAndLeavesOthersAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	other := measurement("atenea", "explore", "claude-sonnet-5")
	if err := store.Put(other); err != nil {
		t.Fatalf("Put other: %v", err)
	}
	original := measurement("atenea", "explore", "claude-opus-5")
	original.USD = 0.35
	if err := store.Put(original); err != nil {
		t.Fatalf("Put original: %v", err)
	}

	replacement := measurement("atenea", "explore", "claude-opus-5")
	replacement.USD = 0.41
	replacement.CLIVersion = "claude-code/1.3.0"
	replacement.MeasuredAt = original.MeasuredAt.Add(24 * time.Hour)
	if err := store.Put(replacement); err != nil {
		t.Fatalf("Put replacement: %v", err)
	}

	got, ok, err := store.Get("atenea", "explore", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want the replaced row")
	}
	if got != replacement {
		t.Errorf("Get = %+v, want the replacement %+v", got, replacement)
	}

	untouched, ok, err := store.Get("atenea", "explore", "claude-sonnet-5")
	if err != nil {
		t.Fatalf("Get other: %v", err)
	}
	if !ok {
		t.Fatal("Get other: ok = false, want the untouched row still there")
	}
	if untouched != other {
		t.Errorf("Get other = %+v, want it unchanged at %+v", untouched, other)
	}

	rows, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2: %+v", len(rows), rows)
	}
}

// This is the exact defect the shared key fixes: measuring the same
// repository and the same model for two different agents used to collide,
// because the key was (repository, model) and Agent did not exist -- a Put
// for plan silently replaced the row Put for explore. Measured 2026-08-14,
// same repository (taxiprime-backend) and same model (claude-opus-5):
// explore cost $0.28 / 27,666 cache-write tokens, plan cost $0.06 / 4,991 --
// two genuinely different floors that a shared (repository, model) key
// would merge into one.
func TestPutTwoAgentsAgainstTheSameRepositoryAndModelKeepsBothRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	explore := measurement("taxiprime-backend", "explore", "claude-opus-5")
	explore.USD = 0.28
	explore.CacheWriteTokens = 27666
	if err := store.Put(explore); err != nil {
		t.Fatalf("Put explore: %v", err)
	}
	plan := measurement("taxiprime-backend", "plan", "claude-opus-5")
	plan.USD = 0.06
	plan.CacheWriteTokens = 4991
	if err := store.Put(plan); err != nil {
		t.Fatalf("Put plan: %v", err)
	}

	rows, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2 (one per agent): %+v", len(rows), rows)
	}

	gotExplore, ok, err := store.Get("taxiprime-backend", "explore", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get explore: %v", err)
	}
	if !ok || gotExplore != explore {
		t.Errorf("Get explore = %+v, ok=%v, want %+v, ok=true", gotExplore, ok, explore)
	}

	gotPlan, ok, err := store.Get("taxiprime-backend", "plan", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if !ok || gotPlan != plan {
		t.Errorf("Get plan = %+v, ok=%v, want %+v, ok=true", gotPlan, ok, plan)
	}
}

// A pair nobody has measured -- including every pair on a machine whose
// cache file was never written -- is the ordinary state a caller checks for
// before deciding the refusal is off, not a fault.
func TestGetOnAnUnknownPairIsAPlainNoNotAnError(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, ok, err := store.Get("atenea", "explore", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get on a file that was never written: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true on a file that was never written")
	}

	if err := store.Put(measurement("atenea", "explore", "claude-opus-5")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, ok, err = store.Get("other-repo", "explore", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get on an unmeasured repository: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true for a repository nothing measured")
	}
}

// A row measured for one agent must not answer a query for another: the
// whole point of the key is that explore's and plan's numbers never stand
// in for each other.
func TestGetWithTheWrongAgentIsAPlainNo(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Put(measurement("atenea", "explore", "claude-opus-5")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, ok, err := store.Get("atenea", "plan", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get with the wrong agent: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true for an agent nothing measured on this repository/model")
	}
}

// A row Put before Agent was part of the key (see Measurement's own doc)
// has Agent == "", which is not a real agent name -- it cannot match a
// query for "explore" or "plan", so it reads as unmeasured rather than
// being guessed at.
func TestGetOnALegacyRowWithNoAgentReadsAsUnmeasured(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Put(measurement("atenea", "", "claude-opus-5")); err != nil {
		t.Fatalf("Put legacy row: %v", err)
	}

	for _, agent := range []string{"explore", "plan"} {
		_, ok, err := store.Get("atenea", agent, "claude-opus-5")
		if err != nil {
			t.Fatalf("Get(%q): %v", agent, err)
		}
		if ok {
			t.Errorf("Get(%q): ok = true against a legacy row with no Agent, want unmeasured", agent)
		}
	}

	rows, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Agent != "" {
		t.Errorf("List = %+v, want the legacy row still present with Agent == \"\"", rows)
	}
}

// List has to come back in the same order every time, independent of the
// order rows were Put in: the status screen prints it directly, and a
// listing that reordered itself from one run to the next would read as
// changing data rather than the same three rows read twice.
func TestListIsStableRegardlessOfPutOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, m := range []floor.Measurement{
		measurement("zeta", "plan", "claude-opus-5"),
		measurement("atenea", "plan", "claude-sonnet-5"),
		measurement("atenea", "explore", "claude-opus-5"),
	} {
		if err := store.Put(m); err != nil {
			t.Fatalf("Put %s/%s/%s: %v", m.Repository, m.Agent, m.Model, err)
		}
	}

	first, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	second, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("List returned %d then %d rows, want 3 both times", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("row %d differed between two List calls: %+v vs %+v", i, first[i], second[i])
		}
	}
	want := []string{"atenea/explore/claude-opus-5", "atenea/plan/claude-sonnet-5", "zeta/plan/claude-opus-5"}
	for i, m := range first {
		if got := m.Repository + "/" + m.Agent + "/" + m.Model; got != want[i] {
			t.Errorf("row %d = %q, want %q", i, got, want[i])
		}
	}
}

// A file that is not the JSON this package writes is refused by name, never
// silently treated as empty: silently starting over would erase every row a
// person or another tool put there without saying so.
func TestACorruptFileIsRefusedNamingThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floors.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, _, err := store.Get("atenea", "explore", "claude-opus-5"); err == nil {
		t.Fatal("Get on a corrupt file: want an error")
	} else if !containsPath(err, path) {
		t.Errorf("Get error %q does not name the corrupt file %q", err, path)
	}

	putErr := store.Put(measurement("atenea", "explore", "claude-opus-5"))
	if putErr == nil {
		t.Fatal("Put on a corrupt file: want an error, not a silent reset")
	}
	if !containsPath(putErr, path) {
		t.Errorf("Put error %q does not name the corrupt file %q", putErr, path)
	}
	if contract.KindOf(putErr) == contract.FailureUnspecified {
		t.Errorf("corrupt-file error was not sorted into a contract.Failure bin: %v", putErr)
	}

	// The refusal must not have touched the file it refused.
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read fixture back: %v", readErr)
	}
	if string(body) != "not json" {
		t.Errorf("corrupt file was rewritten to %q", body)
	}
}

func containsPath(err error, path string) bool {
	return err != nil && strings.Contains(err.Error(), path)
}

// Put on a fresh target directory -- one that does not exist until write
// creates it -- must leave nothing behind but the final file: no partial
// *.tmp is an artifact of the temp-then-rename path being exercised for the
// first time in that directory, and a leftover would mean the rename step
// was skipped rather than completed.
func TestWriteIsAtomicNoPartialFileInAFreshDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "atenea")
	path := filepath.Join(dir, "floors.json")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("fixture directory already exists: %v", err)
	}

	store, err := floor.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Put(measurement("atenea", "explore", "claude-opus-5")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "floors.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want exactly [floors.json]", names)
	}
}

// The rate is a property of the model, not of any one (repository, agent)
// pair: three cold rows for the same model spread across two repositories
// and two agents, measured at different times, must all feed the same
// PriceForModel(model) answer, and the answer must be the most recently
// measured one -- a rate from last week has no reason to win over a rate
// measured an hour ago on a completely different repository.
func TestPriceForModelReturnsTheMostRecentColdRowAcrossRepositoriesAndAgents(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	oldest := measurement("atenea", "explore", "claude-opus-5")
	oldest.USDPerToken = 1.0e-5
	oldest.MeasuredAt = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if err := store.Put(oldest); err != nil {
		t.Fatalf("Put oldest: %v", err)
	}

	middle := measurement("taxiprime-backend", "plan", "claude-opus-5")
	middle.USDPerToken = 1.1e-5
	middle.MeasuredAt = time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	if err := store.Put(middle); err != nil {
		t.Fatalf("Put middle: %v", err)
	}

	newest := measurement("taxiprime-backend", "explore", "claude-opus-5")
	newest.USDPerToken = 1.0149e-5
	newest.MeasuredAt = time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	if err := store.Put(newest); err != nil {
		t.Fatalf("Put newest: %v", err)
	}

	usdPerToken, measuredAt, ok := store.PriceForModel("claude-opus-5")
	if !ok {
		t.Fatal("PriceForModel: ok = false, want true with three cold rows on record")
	}
	if usdPerToken != newest.USDPerToken {
		t.Errorf("PriceForModel usdPerToken = %v, want %v (the most recent cold row)", usdPerToken, newest.USDPerToken)
	}
	if !measuredAt.Equal(newest.MeasuredAt) {
		t.Errorf("PriceForModel measuredAt = %v, want %v", measuredAt, newest.MeasuredAt)
	}
}

// A warm row's USDPerToken was itself borrowed from an earlier PriceForModel
// call, not derived from its own receipt: chaining through it would drift
// the rate away from any real measurement, so PriceForModel must skip warm
// rows even when one is more recent than every cold row on record.
func TestPriceForModelIgnoresWarmRows(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	cold := measurement("atenea", "explore", "claude-opus-5")
	cold.Cold = true
	cold.USDPerToken = 1.0149e-5
	cold.MeasuredAt = time.Date(2026, 8, 14, 19, 40, 0, 0, time.UTC)
	if err := store.Put(cold); err != nil {
		t.Fatalf("Put cold: %v", err)
	}

	warm := measurement("atenea", "explore", "claude-opus-5")
	warm.Agent = "plan"
	warm.Cold = false
	warm.USDPerToken = 4.28e-7 // what a warm receipt would imply if taken at face value; must not win
	warm.MeasuredAt = time.Date(2026, 8, 15, 20, 40, 0, 0, time.UTC)
	if err := store.Put(warm); err != nil {
		t.Fatalf("Put warm: %v", err)
	}

	usdPerToken, _, ok := store.PriceForModel("claude-opus-5")
	if !ok {
		t.Fatal("PriceForModel: ok = false, want true with a cold row on record")
	}
	if usdPerToken != cold.USDPerToken {
		t.Errorf("PriceForModel usdPerToken = %v, want %v (the cold row, not the later warm one)", usdPerToken, cold.USDPerToken)
	}
}

// A model nobody has ever measured cold -- including on a store that holds
// cold rows for other models entirely -- is a clean false, not a zero rate
// mistaken for a real one.
func TestPriceForModelOnAnUnknownModelIsAPlainNo(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	usdPerToken, _, ok := store.PriceForModel("claude-opus-5")
	if ok {
		t.Fatalf("PriceForModel on an empty store: ok = true, usdPerToken = %v", usdPerToken)
	}

	if err := store.Put(measurement("atenea", "explore", "claude-sonnet-5")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	usdPerToken, _, ok = store.PriceForModel("claude-opus-5")
	if ok {
		t.Fatalf("PriceForModel(claude-opus-5) with only claude-sonnet-5 on record: ok = true, usdPerToken = %v", usdPerToken)
	}
}

// reviewer and filereader call no model at all, so measuring them
// correctly produces USD == 0 and PrefixTokens == 0 -- a legitimate cold
// reading, not a sign that nothing was measured. Get has to hand that row
// back with ok == true; treating a zero floor as absent would make the
// caller re-measure an agent that will only ever cost zero, forever.
func TestZeroFloorRowRoundTripsAsMeasuredNotAsAbsent(t *testing.T) {
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	want := measurement("atenea", "reviewer", "claude-opus-5")
	want.USD = 0
	want.PrefixTokens = 0
	want.USDPerToken = 0
	want.CacheWriteTokens = 0
	want.InputTokens = 0
	want.OutputTokens = 0
	want.Cold = true
	if err := store.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := store.Get("atenea", "reviewer", "claude-opus-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false on a zero-floor row, want true -- zero is a measured value, not absence")
	}
	if got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}

	rows, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0] != want {
		t.Errorf("List = %+v, want the zero-floor row present and unchanged", rows)
	}
}

// StartWeight moves when the measurement moves: it is a live
// derivation off Prefix, InputTokens and OutputTokens, never a stored
// number of its own. Two rows, prefixes in a clean 2x ratio and input and
// output held at zero to isolate it, must weigh in the same ratio.
func TestStartWeightMovesWithTheMeasurementItIsDerivedFrom(t *testing.T) {
	small := measurement("taxiprime-backend", "reader", "claude-opus-5")
	small.PrefixTokens = 10_000
	small.InputTokens = 0
	small.OutputTokens = 0

	large := small
	large.PrefixTokens = 20_000

	if got, want := small.StartWeight(), 20_000; got != want {
		t.Errorf("StartWeight (prefix 10,000) = %d, want %d", got, want)
	}
	if got, want := large.StartWeight(), 40_000; got != want {
		t.Errorf("StartWeight (prefix 20,000) = %d, want %d", got, want)
	}
}

// The re-derivation: both weights read StartTokens, so the block that arrives
// with a turn's first tool call is in them. Measured 2026-08-15 on
// taxiprime-backend/explore -- prefix 5,647, first call 41,927 -- the block is
// 7.4x the prefix, so a weight that ignored it understated what a step pays
// before it can read a second thing by the same factor.
func TestBothWeightsIncludeTheFirstCallBlock(t *testing.T) {
	probed := floor.Measurement{PrefixTokens: 5_647, FirstCallTokens: 41_927}
	prefixOnly := floor.Measurement{PrefixTokens: 5_647}

	if got, want := probed.StartTokens(), 47_574; got != want {
		t.Fatalf("StartTokens = %d, want %d", got, want)
	}
	if got, want := probed.StartWeight(), 2*47_574; got != want {
		t.Errorf("StartWeight = %d, want %d -- the whole start as cache write", got, want)
	}
	if got, want := probed.WarmStartWeight(), 47_574/10; got != want {
		t.Errorf("WarmStartWeight = %d, want %d -- the whole start as cache read", got, want)
	}
	// The factor the rule was wrong by, stated rather than implied.
	ratio := float64(probed.WarmStartWeight()) / float64(prefixOnly.WarmStartWeight())
	if ratio < 8.3 || ratio > 8.5 {
		t.Errorf("probed/prefix-only warm weight = %.2fx, want ~8.4x", ratio)
	}
}

// An unprobed row answers its prefix alone, which is what it was measured to
// be. Honest for an agent type with no tools, an understatement for one whose
// first call nobody has priced -- and never a zero, which would read as free.
func TestARowWithNoFirstCallWeighsItsPrefixAlone(t *testing.T) {
	row := floor.Measurement{PrefixTokens: 12_000, InputTokens: 2, OutputTokens: 4}
	if got, want := row.StartTokens(), 12_000; got != want {
		t.Errorf("StartTokens = %d, want %d", got, want)
	}
	if got, want := row.WarmStartWeight(), 2+4*5+1_200; got != want {
		t.Errorf("WarmStartWeight = %d, want %d", got, want)
	}
}

// A row Put before PrefixTokens existed carries the same number in
// CacheWriteTokens alone (see Measurement's own doc and Prefix), and
// StartWeight has to read its weight off that fallback rather than
// answering zero -- zero means unknown, and this row is not. The figures
// are the real ones on this machine: taxiprime-backend/explore,
// input_tokens 2, output_tokens 4, cache_write_tokens 26,603, prefix_tokens
// 0.
func TestStartWeightReadsALegacyRowsWeightOffCacheWriteRatherThanZero(t *testing.T) {
	legacy := floor.Measurement{
		Repository:       "taxiprime-backend",
		Agent:            "explore",
		Model:            "claude-opus-5",
		PrefixTokens:     0,
		CacheWriteTokens: 26_603,
		InputTokens:      2,
		OutputTokens:     4,
	}
	if got, want := legacy.Prefix(), 26_603; got != want {
		t.Errorf("Prefix = %d, want %d off the CacheWriteTokens fallback", got, want)
	}
	if got, want := legacy.StartWeight(), 53_228; got != want {
		t.Errorf("StartWeight = %d, want %d -- a legacy row answering zero would read "+
			"as unmeasured rather than as the real weight it carries", got, want)
	}
}

// The two readings come off ONE stored row -- PrefixTokens is cache-state
// invariant by construction -- and they are twenty times apart because a
// cache write is priced x2 and a cache read x0.1. This is the split the
// admission rule turns on: refuse a step against the warm one, report the
// cold one as what establishing the prefix costs whichever run of the hour
// is first. A row where they came back equal would mean the fallback in one
// of them stopped reading the same prefix as the other.
func TestTheColdAndWarmFirstEventsAreTheSamePrefixAtTwoPrices(t *testing.T) {
	row := floor.Measurement{
		Repository:   "taxiprime-backend",
		Agent:        "explore",
		Model:        "claude-opus-5",
		PrefixTokens: 26_603,
		InputTokens:  2,
		OutputTokens: 4,
	}
	// allowance.Weigh(2, 4, 0, 26603) and allowance.Weigh(2, 4, 26603, 0):
	// 22 tokens of input and output either way, then 53,206 against 2,660.
	if got, want := row.StartWeight(), 53_228; got != want {
		t.Errorf("StartWeight = %d, want %d", got, want)
	}
	if got, want := row.WarmStartWeight(), 2_682; got != want {
		t.Errorf("WarmStartWeight = %d, want %d -- the same prefix read from cache, "+
			"not written to it", got, want)
	}
}

// The warm reading takes the same legacy fallback the cold one does: a row
// Put before PrefixTokens existed still prices, off CacheWriteTokens. If it
// answered zero the engine would read the row as carrying no token count and
// refuse every step against it -- see checkFunding's unmeasured branch.
func TestWarmStartWeightReadsTheLegacyFallbackToo(t *testing.T) {
	legacy := floor.Measurement{
		Repository:       "taxiprime-backend",
		Agent:            "explore",
		Model:            "claude-opus-5",
		PrefixTokens:     0,
		CacheWriteTokens: 26_603,
		InputTokens:      2,
		OutputTokens:     4,
	}
	if got, want := legacy.WarmStartWeight(), 2_682; got != want {
		t.Errorf("WarmStartWeight = %d, want %d off the CacheWriteTokens fallback",
			got, want)
	}
}

// The real row and the real arithmetic: the explore measurement on this
// machine, plus the first-call block the 2026-08-15 probes found, priced both
// ways. The cold figure is cross-checked against a live receipt -- the probe
// that made one tool call was billed $0.4935 where this says $0.47 -- and the
// warm one is that over twenty, which is what a step actually pays.
func TestWarmUSDPricesThePrefixAndTheFirstCallAtCacheReadPrice(t *testing.T) {
	row := floor.Measurement{
		Repository:      "taxiprime-backend",
		Agent:           "explore",
		Model:           "claude-opus-5",
		USD:             0.2667,
		PrefixTokens:    26_603,
		FirstCallTokens: 41_930,
	}
	if got, want := row.StartTokens(), 68_533; got != want {
		t.Errorf("StartTokens = %d, want %d", got, want)
	}
	// 0.2667 x 68,533/26,603 x 0.05.
	if got := row.WarmUSD(); got < 0.034 || got > 0.035 {
		t.Errorf("WarmUSD = %v, want ~$0.0344 -- the whole start read from cache", got)
	}
	// The cold-equivalent price of the same start, which is what the row's own
	// USD column understated by 2.6x while it named the prefix alone.
	if cold := row.USD * float64(row.StartTokens()) / float64(row.Prefix()); cold < 0.68 || cold > 0.69 {
		t.Errorf("cold start = %v, want ~$0.687", cold)
	}
}

// A row nobody has probed for a first tool call answers its prefix and says
// so by pricing the prefix alone -- an understatement a caller can see, never
// a zero it would read as free. Cross-checked against the mechanical rows,
// where a zero dollar figure is the measurement.
func TestWarmUSDIsUnknownRatherThanFreeWhenThereIsNothingToDeriveFrom(t *testing.T) {
	noFirstCall := floor.Measurement{USD: 0.05, PrefixTokens: 4_728}
	// 0.05 x 4,728/4,728 x 0.05: the prefix, warm, and nothing claimed about
	// a first call nobody measured.
	if got := noFirstCall.WarmUSD(); got < 0.0024 || got > 0.0026 {
		t.Errorf("WarmUSD = %v, want ~$0.0025 off the prefix alone", got)
	}
	mechanical := floor.Measurement{USD: 0, PrefixTokens: 0}
	if got := mechanical.WarmUSD(); got != 0 {
		t.Errorf("WarmUSD = %v, want 0 for a row with no dollar figure to scale", got)
	}
}
