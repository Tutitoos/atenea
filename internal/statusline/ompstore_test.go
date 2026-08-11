package statusline_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The limits widget reads another product's private store: omp keeps one usage
// report per provider in `~/.omp/agent/agent.db`, under a `cache` key, and this
// repository has no contract with any of it -- no version, no schema, no promise.
// Everything the widget needs is read out of that JSON by hand.
//
// The failure that follows from it is silent by construction. A renamed field, a
// moved table or a different wrapper all land in the widget's `catch`, and until
// 2026-08-11 that produced an absent section -- indistinguishable from a provider
// with nothing live to say, which is the most ordinary state on that screen. The
// widget now says `sin lectura` when it cannot read what it found; this test is the
// other half, and the earlier one: it fails on the machine where the store lives,
// before anybody has to notice a section that stopped appearing.
//
// It cannot run in CI. There is no omp there, so there is no store, and a test that
// invented one would be testing this file's idea of the format rather than the
// format -- which is the exact error the widget itself was built on. So it skips,
// loudly enough to read in `go test -v`, and shouts only on a machine that has the
// thing it is checking.
func TestOmpUsageReportStillLooksLikeTheWidgetReadsIt(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	store := filepath.Join(home, ".omp", "agent", "agent.db")
	if _, err := os.Stat(store); err != nil {
		t.Skipf("no omp store at %s: nothing to check on this machine", store)
	}
	// Read with the sqlite CLI rather than a driver: this module has no sqlite
	// dependency and adding one to serve a test that skips everywhere but here
	// would cost every build to check one machine.
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skipf("no sqlite3 binary: %v", err)
	}

	// Read-only, and while omp may be writing: the store is in WAL.
	out, err := exec.Command(sqlite, "-readonly", "-json", store,
		`SELECT key, value FROM cache WHERE key LIKE 'usage_cache:report:%'`).CombinedOutput()
	if err != nil {
		t.Fatalf("the widget reads `cache` from omp's store and this query failed -- "+
			"the table or the store moved: %v\n%s", err, out)
	}

	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if len(out) > 0 {
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatalf("unparseable rows: %v\n%s", err, out)
		}
	}
	if len(rows) == 0 {
		t.Fatal("omp's store carries no `usage_cache:report:*` key: the key convention " +
			"the widget matches on has changed, and the widget would draw nothing")
	}

	type limit struct {
		ID    string `json:"id"`
		Scope struct {
			Tier string `json:"tier"`
		} `json:"scope"`
		Window struct {
			ID         string   `json:"id"`
			DurationMS *float64 `json:"durationMs"`
		} `json:"window"`
		Amount struct {
			Unit         string   `json:"unit"`
			UsedFraction *float64 `json:"usedFraction"`
			Remaining    *float64 `json:"remaining"`
		} `json:"amount"`
		Status string `json:"status"`
	}
	type report struct {
		Provider  string   `json:"provider"`
		FetchedAt *float64 `json:"fetchedAt"`
		Limits    []limit  `json:"limits"`
	}

	// The cached value is wrapped as `{ value, expiresAt }`, and the widget unwraps
	// exactly that. A report that stopped being wrapped would read as empty.
	found := map[string]report{}
	for _, row := range rows {
		var wrapper struct {
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(row.Value), &wrapper); err != nil {
			t.Errorf("%s: value is not JSON: %v", row.Key, err)
			continue
		}
		if len(wrapper.Value) == 0 {
			t.Errorf("%s: no `value` inside the cached entry: the wrapper the widget "+
				"unwraps is gone", row.Key)
			continue
		}
		raw := wrapper.Value
		// Some rows carry the report as a nested JSON string, and the widget accepts
		// both shapes.
		var nested string
		if err := json.Unmarshal(raw, &nested); err == nil {
			raw = json.RawMessage(nested)
		}
		var rep report
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Errorf("%s: report is not the shape the widget parses: %v", row.Key, err)
			continue
		}
		if rep.Provider == "" {
			t.Errorf("%s: report carries no `provider`, which is the field the widget "+
				"selects on -- the key is not parsed for it on purpose", row.Key)
			continue
		}
		if _, seen := found[rep.Provider]; !seen {
			found[rep.Provider] = rep
		}
	}

	// Only the two the widget draws. A provider absent here is not a failure: it
	// means this machine does not use it, which is a legitimate reason for that
	// section to be silent.
	for _, provider := range []string{"anthropic", "openai-codex"} {
		t.Run(provider, func(t *testing.T) {
			rep, ok := found[provider]
			if !ok {
				t.Skipf("no %s report on this machine: that section is legitimately silent", provider)
			}
			if rep.FetchedAt == nil {
				t.Fatal("no `fetchedAt`: without a date there is no age, and an undated " +
					"limit figure is the one thing this widget refuses to print")
			}
			if len(rep.Limits) == 0 {
				t.Fatal("report carries no limits at all")
			}

			// One usable window is the bar's whole requirement: a percentage of a known
			// whole, inside a window of known length.
			usable := 0
			for _, l := range rep.Limits {
				if l.Amount.Unit != "percent" || l.Amount.UsedFraction == nil || l.Amount.Remaining == nil {
					continue
				}
				if l.Window.ID == "" {
					t.Errorf("%s: no `window.id`, which is what the widget prints as `5h` or `7d`", l.ID)
					continue
				}
				if l.Window.DurationMS == nil {
					t.Errorf("%s: no `window.durationMs`: the rule that drops a reading older "+
						"than its own window has nothing to compare against", l.ID)
					continue
				}
				usable++
			}
			if usable == 0 {
				t.Fatalf("not one of %d limits is readable as a percentage of a known whole: "+
					"the widget would draw `sin lectura`", len(rep.Limits))
			}
		})
	}
}
