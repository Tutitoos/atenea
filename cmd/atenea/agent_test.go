package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A declared agent type nothing dispatches is a promise with no far side, and
// the settings file cannot notice: `command` and `args` are strings there,
// and the switch that reads them is in this package. The gap only shows up at
// spawn -- after a plan naming the type has been written, compiled, funded
// and accepted -- as `no built-in agent`, which reads like a typo in the plan
// rather than a type this binary forgot to wire.
//
// So the table is the shipped declarations themselves rather than a list
// repeated here: a type added to default.toml is checked the day it is added,
// which a hand-written list is not.
func TestEveryShippedAgentTypeThisBinaryDeclaresIsDispatched(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}

	var checked int
	for _, declared := range cfg.Agents {
		name := declared.Spec.Name
		// Only the ones that name this binary. A settings file may point
		// `command` at any program, and what that program dispatches is not
		// this switch's business.
		if declared.Command != "$atenea" || len(declared.Args) == 0 || declared.Args[0] != "agent-exec" {
			continue
		}
		if len(declared.Args) != 2 {
			t.Errorf("agent %q is declared in %s with args %v: `agent-exec` takes exactly one built-in name",
				name, declaredIn(cfg), declared.Args)
			continue
		}
		checked++

		// A card no agent can read. The refusal under test happens before
		// anything is parsed, so every type fails here on its own terms --
		// and none of them reaches a settings file, a socket or a model.
		kind := declared.Args[1]
		err := cmdAgentRun(kind, strings.NewReader("this is not an assignment"), io.Discard)
		if undispatched(err) {
			t.Errorf("agent %q is declared in %s as `agent-exec %s`, and cmdAgentRun in "+
				"cmd/atenea/agent.go has no case for it: %v",
				name, declaredIn(cfg), kind, err)
		}
	}

	if checked == 0 {
		t.Fatal("no shipped agent type runs this binary; this test would pass for an empty file")
	}
	// Teeth: the check above is only worth making while an unwired name is
	// actually refused this way. If the refusal ever stopped being a
	// not_found, every loop above would pass by saying nothing.
	if err := cmdAgentRun("no-such-built-in", strings.NewReader("{}"), io.Discard); !undispatched(err) {
		t.Errorf("a name this binary does not ship answered %v, so the loop above proves nothing", err)
	}
}

// undispatched reports whether this is the refusal cmdAgentRun's default arm
// hands back, and not some other not_found an agent raised about its own
// work -- a file it could not open, a run it could not find.
func undispatched(err error) bool {
	return contract.KindOf(err) == contract.FailureNotFound &&
		strings.Contains(err.Error(), "no built-in agent")
}

// declaredIn names where the type under test came from, so the failure sends
// a reader to the block to edit rather than to the word "settings". The
// embedded defaults are a real file in this repository, and saying only
// "built-in defaults" would leave the reader looking for it.
func declaredIn(cfg config.Config) string {
	if cfg.Source == config.BuiltIn {
		return "internal/config/default.toml"
	}
	return cfg.Source
}

// truncate's budget n is a byte count, and a naive s[:n-1] can land inside a
// multi-byte rune's encoding -- there is no rule that the cut point falls
// between two runes. Shape that actually trips it: an 8-byte ASCII prefix
// followed by a 3-byte rune, truncated at n=10 so the cut lands after the
// rune's first byte and before its second and third. Under the byte-slice-
// only version of truncate this returned "12345678\xe4…", which
// utf8.ValidString rejects; that is the failure this test defends against.
func TestTruncateNeverCutsAMultiByteRuneInHalf(t *testing.T) {
	s := "12345678" + "世界" + " more text past the cut, so len(s) > n either way"
	got := truncate(s, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate(%q, 10) = %q, which is not valid UTF-8", s, got)
	}
	if want := "12345678…"; got != want {
		t.Errorf("truncate(%q, 10) = %q, want %q", s, got, want)
	}
}

func TestAgentOutputAndWorkspaceHelpers(t *testing.T) {
	declared := config.AgentType{Spec: contract.AgentTypeSpec{Name: "reader"}, Summary: "summarize files"}
	if got := defaultObjective(declared, []string{" README.md "}); got != "read  README.md  and answer" {
		t.Fatalf("defaultObjective(file) = %q", got)
	}
	if got := defaultObjective(declared, nil); got != "summarize files" {
		t.Fatalf("defaultObjective(summary) = %q", got)
	}
	declared.Summary = ""
	if got := defaultObjective(declared, nil); got != "run reader" {
		t.Fatalf("defaultObjective(name) = %q", got)
	}

	cfg := config.Config{Repositories: []contract.Repository{
		contract.NewRepository("zeta", "/zeta", nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
		contract.NewRepository("alpha", "/alpha", nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
	}}
	ws := workspaceFor(cfg, "alpha")
	if ws.RepositoryID != "alpha" || ws.RepositoryRoot != "/alpha" || strings.Join(ws.Repositories, ",") != "alpha,zeta" {
		t.Fatalf("workspaceFor(named) = %#v", ws)
	}
	ws = workspaceFor(cfg, "missing")
	if ws.RepositoryID != "current" || ws.RepositoryRoot == "" {
		t.Fatalf("workspaceFor(missing) = %#v", ws)
	}

	if got := sortedKeys(map[string]any{"z": 1, "a": 2}); strings.Join(got, ",") != "a,z" {
		t.Fatalf("sortedKeys = %v", got)
	}
	if got := resultLine(" first line\nsecond\nthird "); got != "first line… (3 lines)" {
		t.Fatalf("resultLine(multiline) = %q", got)
	}
	if got := resultLine(strings.Repeat("x", 121)); len(got) <= 120 || !strings.HasSuffix(got, "…") {
		t.Fatalf("resultLine(long) = %q", got)
	}
	if got := resultLine(42); got != "42" {
		t.Fatalf("resultLine(short) = %q", got)
	}
}

func TestAgentReportsAndTracesAreReadable(t *testing.T) {
	failed := contract.Report{
		Verdict:    contract.VerdictFailed,
		Reason:     contract.Reason{Kind: contract.FailureInvalidInput, Text: "bad shape"},
		Result:     map[string]any{"z": strings.Repeat("x", 121), "a": "first\nsecond"},
		Discovered: []contract.Discovery{{Level: contract.ContextRepository, Note: "fact"}},
		Notices:    []string{"caveat"},
	}
	assignment := contract.Assignment{ID: "a-1", TypeName: "reader"}
	var out bytes.Buffer
	printReport(&out, assignment, failed, false)
	text := out.String()
	for _, want := range []string{"a-1  reader  failed", "invalid_input: bad shape", "a: first… (2 lines)", "discovered (repository): fact", "notice: caveat"} {
		if !strings.Contains(text, want) {
			t.Errorf("report output missing %q: %s", want, text)
		}
	}
	out.Reset()
	printReport(&out, contract.Assignment{}, failed, false)
	if out.Len() != 0 {
		t.Fatalf("empty assignment printed %q", out.String())
	}

	run := agent.ReviewedRun{Attempts: []agent.Attempt{
		{Work: assignment, Report: failed},
		{Work: contract.Assignment{ID: "a-2", TypeName: "reader"}, Report: contract.Report{Verdict: contract.VerdictOK}, Reviewed: true,
			Review: contract.Assignment{ID: "r-2", TypeName: "reviewer"}, ReviewReport: contract.Report{Verdict: contract.VerdictOK}},
	}}
	out.Reset()
	printReviewed(&out, run, false)
	if !strings.Contains(out.String(), "attempt 1/2") || !strings.Contains(out.String(), "accepted") {
		t.Fatalf("review output = %q", out.String())
	}

	path := filepath.Join(t.TempDir(), "traces.db")
	store, err := openTraces(context.Background(), path, &out, false)
	if err != nil {
		t.Fatalf("openTraces: %v", err)
	}
	if store.Path() != path {
		t.Fatalf("store path = %q", store.Path())
	}
	_ = store.Close()

	now := time.Now()
	rows := []trace.Row{
		{TypeName: "reader", Objective: "inspect", StartedAt: now.Add(-time.Second), EndedAt: now, Verdict: contract.VerdictOK},
		{TypeName: "reader", Objective: "retry", StartedAt: now, EndedAt: now, Attempt: 2, RetryOf: "a-1", Reason: contract.Reason{Kind: contract.FailureInvalidInput, Text: "needs work"}},
		{TypeName: "reviewer", Objective: "audit", StartedAt: now, Reviews: "a-0"},
	}
	out.Reset()
	printTraces(&out, rows, path)
	for _, want := range []string{"STARTED", "reader", "try 2", "redoes a-1", "reviews a-0", "invalid_input: needs work", "3 traces"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("trace output missing %q: %s", want, out.String())
		}
	}
	if got := verdictLabel(trace.Row{Verdict: contract.VerdictIncomplete, Swept: true, EndedAt: now}); got != "incomplete*" {
		t.Fatalf("verdictLabel(swept) = %q", got)
	}
	if got := verdictLabel(trace.Row{}); got != "running" {
		t.Fatalf("verdictLabel(open) = %q", got)
	}
	if got := took(trace.Row{}); got != "-" {
		t.Fatalf("took(open) = %q", got)
	}
	if got := plural(1, "trace", "traces"); got != "1 trace" {
		t.Fatalf("plural(one) = %q", got)
	}
	if got := plural(2, "trace", "traces"); got != "2 traces" {
		t.Fatalf("plural(many) = %q", got)
	}
}

func TestAgentAndTraceCommandValidation(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"reader", "--bad"}, {"reader", "file", "--traces"}} {
		if err := cmdAgent("", args, &out); err == nil {
			t.Errorf("cmdAgent(%v) unexpectedly succeeded", args)
		}
	}
	for _, args := range [][]string{{"file"}, {"--verdict", "not-a-verdict"}, {"--since", "not-a-duration"}} {
		if err := cmdTraces(args, &out); err == nil {
			t.Errorf("cmdTraces(%v) unexpectedly succeeded", args)
		}
	}
	path := filepath.Join(t.TempDir(), "traces.db")
	if err := cmdTraces([]string{"--traces", path}, &out); err != nil {
		t.Fatalf("cmdTraces(empty): %v", err)
	}
	if !strings.Contains(out.String(), "no traces in "+path) {
		t.Fatalf("cmdTraces(empty) = %q", out.String())
	}
}
