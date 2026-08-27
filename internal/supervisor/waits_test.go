package supervisor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The gate on the margins, because the alternative is remembering.
//
// This package's tests are the ones that watch a clock: a process told to live
// 50ms, an idle window of a second, a restart budget that resets after a
// stable run. Every assertion about them compares a MEASURED duration against
// a threshold the test set, and the margin between the two is the whole
// difference between testing the supervisor and testing the runner.
//
// It has bitten twice on CI's Intel macOS leg, a fortnight apart, on two
// different assertions that pass locally every time:
//
//   - TestProcessStabilityResetsTheRestartBudget, "Restarts = 4, want 3": a
//     process told to live 50ms was observed living past a StableAfter of
//     100ms, so a crash read as a stable run and the budget reset twice.
//   - TestTheIdleReaperStopsAnUnusedServerButNotOneInUse, "condition not met
//     within 3s": a one-second idle window that the reaper did notice, later
//     than a three-second ceiling allowed for.
//
// Neither was a defect in this package. Both were a number somebody guessed
// once, and a guess is what this test replaces.
var waitForCall = regexp.MustCompile(`waitFor\(t,\s*([^,]+),`)

func TestEveryWaitCeilingIsTheOneThatWasReasonedAbout(t *testing.T) {
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no tests found; this would pass for an empty package")
	}
	found := 0
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for number, line := range strings.Split(string(body), "\n") {
			match := waitForCall.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			found++
			// waitFor polls and returns the instant its condition holds, so a
			// generous ceiling costs nothing on a machine that is not busy.
			// There is no reason for any of them to be a different number,
			// and every reason for none of them to be a small one.
			if got := strings.TrimSpace(match[1]); got != "waitCeiling" {
				t.Errorf("%s:%d waits %s rather than waitCeiling: a ceiling tuned by hand is a "+
					"ceiling somebody guessed, and these have failed CI twice",
					path, number+1, got)
			}
		}
	}
	if found == 0 {
		t.Error("nothing in this package waits for anything, which is not what these tests do")
	}
}

// A ceiling short enough to trip on a loaded machine is the failure this is
// about, so the constant itself is held above the range that produced one.
func TestTheWaitCeilingIsGenerous(t *testing.T) {
	// The observed failure was a three-second ceiling on a one-second window.
	// Anything in that neighborhood is the same guess wearing a bigger number.
	if waitCeiling < 10*time.Second {
		t.Errorf("waitCeiling = %s, which is the range that has already failed CI twice", waitCeiling)
	}
}
