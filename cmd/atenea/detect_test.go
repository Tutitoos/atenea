package main

// detect end to end: the real CLI dispatch, the real Core.DetectIndexes, and
// -- for the case that actually reaches a provider -- a stand-in for
// codebase-memory-mcp shaped exactly like the fixture the adapter's own
// tests use. Everything below the command line is production code; only the
// far side is faked, and it has to be: this suite must not depend on a real
// graph index existing on the machine that runs it.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDetectReportsWhenNoAttachedProviderCanTell uses the package's own
// default fixture, which dispatches to omp -- a runner with no index state
// to report. A sweep that finds nobody to ask has to say so, not print
// nothing.
func TestDetectReportsWhenNoAttachedProviderCanTell(t *testing.T) {
	out, err := cli(t, "--config", settingsFile(t), "detect")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(out, "no attached provider can report index readiness") {
		t.Fatalf("out = %q", out)
	}
}

// detectFixture wires a single codebasememory runner at the given fake
// binary against two repositories, so a test can drive one to "ready" and
// the other to "not ready" from the same process.
func detectFixture(t *testing.T, binary string, repos ...[2]string) string {
	t.Helper()
	dir := t.TempDir()
	var body strings.Builder
	body.WriteString("contract = \"3.0.0\"\n\n[orchestrator]\nrunners = [\"codebasememory\"]\n\n")
	body.WriteString("  [orchestrator.codebasememory]\n  binary = " + quoteTOML(binary) + "\n")
	body.WriteString("  implementations = [\"codebase-memory.index\"]\n")
	for _, repo := range repos {
		id, path := repo[0], repo[1]
		body.WriteString("\n[[repository]]\nid = " + quoteTOML(id) + "\npath = " + quoteTOML(path) +
			"\nlanguages = [\"go\"]\nscale = \"small\"\n")
	}
	path := filepath.Join(dir, "atenea.toml")
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func quoteTOML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// fakeIndexStatus is a codebase-memory-mcp stand-in that answers index_status
// with "ready" for any project path containing readyMarker, and with a
// project-not-found error for everything else -- the same shape
// failureFor's own tests already pin.
func fakeIndexStatus(t *testing.T, readyMarker string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary below is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codebase-memory-mcp")
	script := "#!/bin/sh\nbody=\"$(cat)\"\ncase \"$2\" in\n" +
		"index_status)\n  case \"$body\" in\n" +
		"    *" + readyMarker + "*) echo '{\"status\":\"ready\"}'; exit 0 ;;\n" +
		"    *) echo '{\"error\":\"project not found\"}' >&2; exit 1 ;;\n" +
		"  esac ;;\n" +
		"*) echo '{\"error\":\"unknown tool\"}' >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake binary: %v", err)
	}
	return path
}

func TestDetectReportsReadyAndNotReadyPerRepository(t *testing.T) {
	binary := fakeIndexStatus(t, "ready-repo")
	readyDir := filepath.Join(t.TempDir(), "ready-repo")
	staleDir := filepath.Join(t.TempDir(), "stale-repo")
	settingsPath := detectFixture(t, binary, [2]string{"ready", readyDir}, [2]string{"stale", staleDir})

	out, err := cli(t, "--config", settingsPath, "detect")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(out, "ready") || !strings.Contains(out, "codebase-memory") {
		t.Errorf("out is missing the ready repository's line:\n%s", out)
	}
	if !strings.Contains(out, "not ready") || !strings.Contains(out, "project not found") {
		t.Errorf("out is missing the stale repository's line:\n%s", out)
	}
}

// --repo narrows the sweep to one repository, the same flag select already
// uses for the same reason: naming the target beats scanning the catalog.
func TestDetectRepoFlagNarrowsToOneRepository(t *testing.T) {
	binary := fakeIndexStatus(t, "ready-repo")
	readyDir := filepath.Join(t.TempDir(), "ready-repo")
	staleDir := filepath.Join(t.TempDir(), "stale-repo")
	settingsPath := detectFixture(t, binary, [2]string{"ready", readyDir}, [2]string{"stale", staleDir})

	out, err := cli(t, "--config", settingsPath, "detect", "--repo", "ready")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if strings.Contains(out, "stale") {
		t.Errorf("--repo ready must not mention stale:\n%s", out)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("out is missing the named repository:\n%s", out)
	}
}

func TestDetectRepoFlagOnAnUnknownRepositoryFails(t *testing.T) {
	binary := fakeIndexStatus(t, "ready-repo")
	settingsPath := detectFixture(t, binary, [2]string{"api", filepath.Join(t.TempDir(), "api")})
	if _, err := cli(t, "--config", settingsPath, "detect", "--repo", "nope"); err == nil {
		t.Fatal("detect --repo nope should fail")
	}
}

// --json is read by a script, not an eye: the same facts, structured.
func TestDetectJSONReportsReadyAndNotReady(t *testing.T) {
	binary := fakeIndexStatus(t, "ready-repo")
	readyDir := filepath.Join(t.TempDir(), "ready-repo")
	staleDir := filepath.Join(t.TempDir(), "stale-repo")
	settingsPath := detectFixture(t, binary, [2]string{"ready", readyDir}, [2]string{"stale", staleDir})

	out, err := cli(t, "--config", settingsPath, "detect", "--json")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	var reports []jsonIndexReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("--json wrote invalid json: %v\n%s", err, out)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %+v, want two", reports)
	}
	byRepo := map[string]jsonIndexReport{}
	for _, r := range reports {
		byRepo[r.Repository] = r
	}
	if !byRepo["ready"].Ready {
		t.Errorf("ready = %+v, want Ready=true", byRepo["ready"])
	}
	if byRepo["stale"].Ready || byRepo["stale"].Hint == "" {
		t.Errorf("stale = %+v, want Ready=false with a hint", byRepo["stale"])
	}
}

// The empty sweep still has to be valid, parseable json -- an empty array,
// not a bare "no provider" sentence meant for an eye.
func TestDetectJSONOnAnEmptySweepIsAnEmptyArray(t *testing.T) {
	var out bytes.Buffer
	printIndexReportsJSON(&out, nil)
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("out = %q, want an empty json array", out.String())
	}
}
