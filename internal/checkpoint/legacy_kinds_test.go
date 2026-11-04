package checkpoint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
)

// The task and plan commissions are gone, but the runs they left on disk are
// still somebody's history. Kind is a plain string, so a receipt of a removed
// kind has to keep loading with every field intact; this pins the two shapes
// those commissions wrote.
func TestReceiptsOfRemovedKindsStillDecode(t *testing.T) {
	cases := map[string]struct {
		id, body, kind string
		wantSteps      int
	}{
		"task": {
			id:   "20260803T125305-92dc90",
			kind: "task",
			body: `{"id":"20260803T125305-92dc90","kind":"task","task":"find every TODO","repositories":["api"],
"budget_usd":0.5,"contract_version":"4.0.0","started":"2026-08-03T12:53:05Z","updated":"2026-08-03T12:53:06Z",
"closed":true,"verdict":"ok",
"plan":{"task":"find every TODO","steps":[
 {"id":"explore-api","capability":"code.search","repository":"api","payload":{"query":"find every TODO"}},
 {"id":"search-api","capability":"code.search","repository":"api","payload":{"query":"find every TODO"},"needs":["explore-api"]}]},
"steps":[{"id":"explore-api","repository":"api","implementation":"ripgrep","verdict":"ok","review":"ok","duration_ms":12},
 {"id":"search-api","repository":"api","implementation":"ripgrep","verdict":"ok","review":"ok","duration_ms":15}]}`,
			wantSteps: 2,
		},
		"plan": {
			id:   "20260803T130000-aa11bb",
			kind: "plan",
			body: `{"id":"20260803T130000-aa11bb","kind":"plan","task":"review login","repositories":["api","web"],
"budget_usd":0,"started":"2026-08-03T13:00:00Z","updated":"2026-08-03T13:00:01Z","closed":false,"verdict":"",
"plan":{"task":"review login","steps":[
 {"id":"search-api","capability":"code.search","repository":"api","payload":{"query":"login"}}]},
"steps":[{"id":"search-api","repository":"api","implementation":"ripgrep","verdict":"ok","review":"ok","duration_ms":9}]}`,
			wantSteps: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.id+".json"), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			store, err := checkpoint.New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			read, err := store.Load(tc.id)
			if err != nil {
				t.Fatalf("a %s receipt no longer loads: %v", tc.kind, err)
			}
			if read.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", read.Kind, tc.kind)
			}
			if len(read.Steps) != tc.wantSteps || read.Steps[0].Implementation != "ripgrep" {
				t.Errorf("steps = %+v, want %d recorded by ripgrep", read.Steps, tc.wantSteps)
			}
			if len(read.Plan.Steps) == 0 || read.Plan.Task != read.Task {
				t.Errorf("plan = %+v did not survive", read.Plan)
			}
			ids, err := store.List()
			if err != nil || len(ids) != 1 || ids[0] != tc.id {
				t.Errorf("List = %v, %v, want the one receipt", ids, err)
			}
		})
	}
}
