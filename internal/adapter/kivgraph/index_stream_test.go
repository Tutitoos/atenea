package kivgraph_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
)

// fakeIndexer writes a shell script that stands in for `kivgraph index`,
// emitting n progress events and then the result document.
func fakeIndexer(t *testing.T, progress int, passed bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake indexer is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "kivgraph")
	// Each progress line carries a payload, so the count below is what puts
	// the stream past the 8 KiB the old tail kept.
	body := "#!/bin/sh\n" +
		fmt.Sprintf("i=0; while [ $i -lt %d ]; do\n", progress) +
		`  printf '{"event":"progress","file":"%s"}\n' "internal/some/rather/long/path/to/a/file/number-$i.go"` + "\n" +
		"  i=$((i+1))\ndone\n" +
		fmt.Sprintf(`printf '{"event":"result","result":{"generation_id":"000011","passed":%t,`+
			`"counts":{"symbols":19885,"edges":72034}}}\n'`, passed) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake indexer: %v", err)
	}
	return path
}

// The defect: stdout was captured into a buffer that keeps only the last
// 8 KiB, cutting wherever the byte fell, and that buffer was then parsed as
// complete JSONL. The first surviving line of any longer output is half an
// object, so repository.index -- the only mutating capability in the system --
// answered "returned invalid JSONL" for an index that had completed perfectly.
//
// 200 progress events is roughly 16 KiB, which for a real workspace is a small
// run: the counters this fake reports are the ones the shipped documentation
// records from an actual index.
func TestAnIndexLongerThanTheOldBufferIsStillRead(t *testing.T) {
	report, err := kivgraph.RunConfiguredIndex(t.Context(),
		fakeIndexer(t, 200, true), nil, t.TempDir(), "full")
	if err != nil {
		t.Fatalf("RunConfiguredIndex: %v", err)
	}
	if report.Generation != "000011" {
		t.Errorf("generation = %q, want the one the result event carries", report.Generation)
	}
	if report.Nodes != 19885 || report.Edges != 72034 {
		t.Errorf("counts = %d nodes, %d edges; want the result event's",
			report.Nodes, report.Edges)
	}
}

// The short case still works, which is the one that used to.
func TestAShortIndexIsReadTheSameWay(t *testing.T) {
	report, err := kivgraph.RunConfiguredIndex(t.Context(),
		fakeIndexer(t, 2, true), nil, t.TempDir(), "full")
	if err != nil {
		t.Fatalf("RunConfiguredIndex: %v", err)
	}
	if report.Generation != "000011" {
		t.Errorf("generation = %q, want the one the result event carries", report.Generation)
	}
}

// A provider that reports failure is reported as failing, not as unreadable.
func TestAnIndexThatDidNotPassSaysSo(t *testing.T) {
	_, err := kivgraph.RunConfiguredIndex(t.Context(),
		fakeIndexer(t, 200, false), nil, t.TempDir(), "full")
	if err == nil {
		t.Fatal("an index the provider says did not pass was accepted")
	}
	if !strings.Contains(err.Error(), "did not pass") {
		t.Errorf("error = %q, want it to say the provider refused", err)
	}
}

// And a stream with no result event is still the provider's silence rather
// than a parse error.
func TestAnIndexWithNoResultEventSaysSo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kivgraph")
	if err := os.WriteFile(path,
		[]byte("#!/bin/sh\nprintf '{\"event\":\"progress\"}\\n'\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := kivgraph.RunConfiguredIndex(t.Context(), path, nil, t.TempDir(), "full")
	if err == nil {
		t.Fatal("a stream with no result event was accepted")
	}
	if !strings.Contains(err.Error(), "no result event") {
		t.Errorf("error = %q, want it to name what was missing", err)
	}
}
