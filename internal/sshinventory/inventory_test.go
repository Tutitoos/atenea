package sshinventory

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func aliases(hosts []Host) []string {
	result := make([]string, len(hosts))
	for index, host := range hosts {
		result[index] = host.Alias
	}
	return result
}

func TestScanIncludesAndConditionalHost(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, filepath.Join(root, "parts", "20-last"), "Host bravo CHARLIE\n")
	writeFixture(t, filepath.Join(root, "parts", "10-first"), "Host alpha *.example !blocked\n")
	writeFixture(t, filepath.Join(root, "nested", "child"), "Host beta\n")
	writeFixture(t, config, "Include=parts/*\nHost=delta echo\nHost outer\n  Include = nested/child\nHost * !blocked\n  Hostname example.invalid\n")

	got, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "bravo", "CHARLIE", "delta", "echo", "outer"}; !reflect.DeepEqual(aliases(got.Hosts), want) {
		t.Fatalf("aliases = %q, want %q", aliases(got.Hosts), want)
	}
	if len(got.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", got.Diagnostics)
	}
	if got.Hosts[0].Source != filepath.Join(root, "parts", "10-first") || got.Hosts[0].Line != 1 {
		t.Fatalf("lost source provenance: %+v", got.Hosts[0])
	}
	// The nested Host beta cannot be active because its Include is inside Host outer.
	for _, host := range got.Hosts {
		if host.Conditional {
			t.Fatalf("unexpected conditional host: %+v", host)
		}
	}
}

func TestScanNeverRunsMatchExecAndMarksConditionalIncludes(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "would-have-run")
	config := filepath.Join(root, "config")
	writeFixture(t, filepath.Join(root, "inside"), "Host uncertain\n")
	writeFixture(t, config, "Host certain\nMatch exec \"touch "+marker+"\"\n  Include inside\nHost certain\n")

	got, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Match exec ran or marker check failed: %v", err)
	}
	if want := []string{"certain", "uncertain"}; !reflect.DeepEqual(aliases(got.Hosts), want) {
		t.Fatalf("aliases = %q, want %q", aliases(got.Hosts), want)
	}
	if !got.Hosts[1].Conditional || got.Hosts[0].Conditional {
		t.Fatalf("wrong conditional states: %+v", got.Hosts)
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "match_requires_selected_resolution" {
		t.Fatalf("Match resolution diagnostic missing: %+v", got.Diagnostics)
	}
}

func TestScanBoundsCyclesAndTracksIncludeChanges(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	child := filepath.Join(root, "parts", "a")
	writeFixture(t, config, "Include parts/*\nHost base\n")
	writeFixture(t, child, "Host included\nInclude config\n")
	first, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"included", "base"}; !reflect.DeepEqual(aliases(first.Hosts), want) {
		t.Fatalf("aliases = %q, want %q", aliases(first.Hosts), want)
	}
	// Relative Includes always use the top-level config directory.
	if len(first.Diagnostics) != 1 || first.Diagnostics[0].Code != "include_cycle" {
		t.Fatalf("expected bounded cycle: %+v", first.Diagnostics)
	}
	writeFixture(t, filepath.Join(root, "parts", "b"), "Host new\n")
	second, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot == second.Snapshot || !strings.Contains(strings.Join(aliases(second.Hosts), ","), "new") {
		t.Fatalf("Include change did not invalidate snapshot: before=%+v after=%+v", first, second)
	}
}

func TestScanUnresolvedDynamicIncludeAndSyntax(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Include ${DYNAMIC}/config\nHost \"unterminated\nHost stable\n")
	got, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"stable"}; !reflect.DeepEqual(aliases(got.Hosts), want) {
		t.Fatalf("aliases = %q, want %q", aliases(got.Hosts), want)
	}
	if len(got.Diagnostics) != 2 || got.Diagnostics[0].Code != "dynamic_include" || got.Diagnostics[1].Code != "invalid_syntax" {
		t.Fatalf("missing diagnostics: %+v", got.Diagnostics)
	}
}

func TestScanCaseAndStaticIncludeConditions(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, filepath.Join(root, "child"), "Host mixed.lab blocked.lab\n")
	writeFixture(t, config, "Host *.lab !blocked.lab\n Include child\nHost MIXED.LAB\nHost mixed.lab\n")
	got, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mixed.lab", "MIXED.LAB"}; !reflect.DeepEqual(aliases(got.Hosts), want) {
		t.Fatalf("aliases = %q, want %q", aliases(got.Hosts), want)
	}
	if got.Hosts[0].Conditional || got.Hosts[0].Source != filepath.Join(root, "child") {
		t.Fatalf("static Include lost provenance: %+v", got.Hosts[0])
	}
}
