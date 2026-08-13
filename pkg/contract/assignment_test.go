package contract_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func testLimits() contract.Limits {
	return contract.Limits{MaxDuration: 10 * time.Minute, MaxTokens: 100_000}
}

func testTask() contract.Task {
	return contract.Task{
		Objective: "find every caller of Runner.Run",
		Files:     []string{"internal/core/core.go"},
		Criterion: "a list of file:line positions, each one a real call site",
	}
}

func testSpec() contract.AgentTypeSpec {
	return contract.AgentTypeSpec{
		Name: "explorer",
		Kind: contract.AgentSpecialized,
		Result: []contract.Field{
			{Name: "summary", Type: contract.TypeString, Required: true, Summary: "what was found"},
		},
	}
}

func testRoot(t *testing.T) contract.Assignment {
	t.Helper()
	root := contract.RootAssignment("root", "orchestrator", contract.AgentOrchestrator,
		testTask(), testLimits())
	root.Context = []contract.ContextLevel{contract.ContextRepository}
	root.Effects = []contract.Effect{contract.EffectRead, contract.EffectWrite}
	if err := root.Validate(); err != nil {
		t.Fatalf("root assignment: %v", err)
	}
	return root
}

// A root is depth 1 and carries no parent; the two facts have to agree or the
// chain a receipt is filed under is broken.
func TestRootAssignmentIsDepthOneWithNoParent(t *testing.T) {
	root := testRoot(t)
	if root.Depth != 1 {
		t.Fatalf("depth = %d, want 1", root.Depth)
	}
	if root.ParentID != "" {
		t.Fatalf("parent = %q, want none", root.ParentID)
	}

	orphan := root
	orphan.Depth = 2
	if err := orphan.Validate(); err == nil {
		t.Fatal("a depth-2 assignment with no parent id was accepted")
	}

	rooted := root
	rooted.ParentID = "somebody"
	if err := rooted.Validate(); err == nil {
		t.Fatal("a depth-1 assignment with a parent id was accepted")
	}
}

// Rule: a child can never declare more effects than its parent. Authority runs
// downwards, and Child is the only door, so the widened card never exists.
func TestChildCannotWidenItsParentsEffects(t *testing.T) {
	root := testRoot(t)

	_, err := root.Child("kid", "explorer", contract.AgentSpecialized, testTask(),
		[]contract.Effect{contract.EffectRead, contract.EffectExternal}, testLimits())
	if err == nil {
		t.Fatal("child was granted external, which its parent does not hold")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	if !strings.Contains(err.Error(), "external") {
		t.Fatalf("error does not name the effect: %v", err)
	}

	// A subset is fine, and it is a subset that is copied -- not the parent's
	// whole list.
	child, err := root.Child("kid", "explorer", contract.AgentSpecialized, testTask(),
		[]contract.Effect{contract.EffectRead}, testLimits())
	if err != nil {
		t.Fatalf("narrowing refused: %v", err)
	}
	if child.Causes(contract.EffectWrite) {
		t.Fatal("child inherited write it was not granted")
	}
	if !child.Causes(contract.EffectRead) {
		t.Fatal("child lost the effect it was granted")
	}
	if child.ParentID != root.ID {
		t.Fatalf("parent = %q, want %q", child.ParentID, root.ID)
	}
}

// The same rule for the other two grants: time and tokens narrow downwards too.
func TestChildCannotWidenItsParentsLimits(t *testing.T) {
	root := testRoot(t)
	wider := contract.Limits{MaxDuration: time.Hour, MaxTokens: 100_000}

	if _, err := root.Child("kid", "explorer", contract.AgentSpecialized, testTask(),
		[]contract.Effect{contract.EffectRead}, wider); err == nil {
		t.Fatal("child was granted more wall clock than its parent holds")
	}

	richer := contract.Limits{MaxDuration: time.Minute, MaxTokens: 1_000_000}
	if _, err := root.Child("kid", "explorer", contract.AgentSpecialized, testTask(),
		[]contract.Effect{contract.EffectRead}, richer); err == nil {
		t.Fatal("child was granted more tokens than its parent holds")
	}
}

// Rule: depth is capped at three levels. The third may not hand out work, and
// a card claiming depth four is refused even if somebody builds it by hand.
func TestDepthIsCappedAtThreeLevels(t *testing.T) {
	one := testRoot(t)
	read := []contract.Effect{contract.EffectRead}

	two, err := one.Child("two", "explorer", contract.AgentSpecialized, testTask(), read, testLimits())
	if err != nil {
		t.Fatalf("level 2: %v", err)
	}
	three, err := two.Child("three", "explorer", contract.AgentSpecialized, testTask(), read, testLimits())
	if err != nil {
		t.Fatalf("level 3: %v", err)
	}
	if three.Depth != contract.MaxAgentDepth {
		t.Fatalf("depth = %d, want %d", three.Depth, contract.MaxAgentDepth)
	}

	_, err = three.Child("four", "explorer", contract.AgentSpecialized, testTask(), read, testLimits())
	if err == nil {
		t.Fatal("a fourth level was handed out")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}

	forged := three
	forged.Depth = contract.MaxAgentDepth + 1
	if err := forged.Validate(); err == nil {
		t.Fatal("a hand-built depth-4 card validated")
	}
}

// Rule: incomplete is a distinct state from bad, and neither may be read as
// the other. A caller that folds them discards sound work or trusts unsound
// work.
func TestIncompleteIsNotFailed(t *testing.T) {
	if contract.VerdictIncomplete == contract.VerdictFailed {
		t.Fatal("incomplete and failed are the same value")
	}
	if contract.VerdictIncomplete.String() != "incomplete" {
		t.Fatalf("name = %q", contract.VerdictIncomplete.String())
	}

	parsed, err := contract.ParseVerdict("incomplete")
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if parsed != contract.VerdictIncomplete {
		t.Fatalf("parsed = %v", parsed)
	}

	partial := contract.Report{
		Result:  map[string]any{"summary": "three of five files read"},
		Verdict: contract.VerdictIncomplete,
		Reason: contract.Reason{
			Kind: contract.FailureTimeout,
			Text: "two files left when the clock ran out",
		},
	}
	if err := partial.Validate(testSpec()); err != nil {
		t.Fatalf("a reasoned incomplete report was refused: %v", err)
	}
	if partial.Failed() {
		t.Fatal("Failed() answered true for an incomplete report")
	}
	if !partial.Incomplete() {
		t.Fatal("Incomplete() answered false for an incomplete report")
	}

	bad := partial
	bad.Verdict = contract.VerdictFailed
	if bad.Incomplete() {
		t.Fatal("Incomplete() answered true for a failed report")
	}
	if !bad.Failed() {
		t.Fatal("Failed() answered false for a failed report")
	}
}

// Rule: an empty result requires a reason. A report that answers nothing and
// calls it ok reads as success to every counter that never opens it.
func TestEmptyResultRequiresAReason(t *testing.T) {
	spec := testSpec()

	silent := contract.Report{Verdict: contract.VerdictOK}
	err := silent.Validate(spec)
	if err == nil {
		t.Fatal("an empty ok report with no reason was accepted")
	}
	if !strings.Contains(err.Error(), "empty result") {
		t.Fatalf("error does not name the rule: %v", err)
	}

	explained := contract.Report{
		Verdict: contract.VerdictOK,
		Reason: contract.Reason{
			Kind: contract.FailureNotFound,
			Text: "the symbol has no callers in this repository",
		},
	}
	if err := explained.Validate(spec); err != nil {
		t.Fatalf("an explained empty result was refused: %v", err)
	}

	// Every verdict that is not a plain ok needs one too, with or without a
	// result: the verdict is the claim and the reason is the evidence.
	for _, v := range []contract.Verdict{
		contract.VerdictFailed, contract.VerdictIncomplete, contract.VerdictCanceled,
	} {
		r := contract.Report{Result: map[string]any{"summary": "partial"}, Verdict: v}
		if err := r.Validate(spec); err == nil {
			t.Fatalf("verdict %s was accepted with no reason", v)
		}
	}
}

// A reason is a bin plus prose, and half of one is not a reason: the bin alone
// never says which thing, and the prose alone is something every consumer
// would have to pattern-match on.
func TestReasonNeedsBothHalves(t *testing.T) {
	spec := testSpec()

	binOnly := contract.Report{
		Verdict: contract.VerdictFailed,
		Result:  map[string]any{"summary": "x"},
		Reason:  contract.Reason{Kind: contract.FailureTimeout},
	}
	if err := binOnly.Validate(spec); err == nil {
		t.Fatal("a reason with no text was accepted")
	}

	proseOnly := contract.Report{
		Verdict: contract.VerdictFailed,
		Result:  map[string]any{"summary": "x"},
		Reason:  contract.Reason{Text: "it broke"},
	}
	if err := proseOnly.Validate(spec); err == nil {
		t.Fatal("a reason with no bin was accepted")
	}
}

// The result is judged against the shape the agent type declared, strictly:
// an unknown field is refused, because the schema handed to whoever produced
// it says additionalProperties is false.
func TestResultIsCheckedAgainstTheDeclaredShape(t *testing.T) {
	spec := testSpec()

	wrong := contract.Report{
		Verdict: contract.VerdictOK,
		Result:  map[string]any{"summary": "found it", "extra": 1},
	}
	if err := wrong.Validate(spec); err == nil {
		t.Fatal("a result carrying an undeclared field was accepted")
	}

	missing := contract.Report{
		Verdict: contract.VerdictOK,
		Result:  map[string]any{"summary": 42},
	}
	if err := missing.Validate(spec); err == nil {
		t.Fatal("a result with the wrong type was accepted")
	}

	right := contract.Report{
		Verdict: contract.VerdictOK,
		Result:  map[string]any{"summary": "found it"},
	}
	if err := right.Validate(spec); err != nil {
		t.Fatalf("a conforming result was refused: %v", err)
	}

	schema, err := spec.ResultSchema()
	if err != nil {
		t.Fatalf("ResultSchema: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %v", schema["additionalProperties"])
	}
}

// A discovery over the cap is shortened rather than dropped, and the cut
// leaves a mark: a clipped fact must never be indistinguishable from a whole
// one.
func TestOverlongDiscoveryIsTruncatedWithAWarning(t *testing.T) {
	long := strings.Repeat("a", contract.MaxDiscoveryLength+40)
	report := contract.Report{
		Result:  map[string]any{"summary": "ok"},
		Verdict: contract.VerdictOK,
		Discovered: []contract.Discovery{
			{Level: contract.ContextRepository, Note: long},
			{Level: contract.ContextRepository, Note: "short enough"},
		},
	}

	if err := report.Validate(testSpec()); err == nil {
		t.Fatal("an over-long discovery validated without being normalized")
	}

	out := report.Normalize()
	if got := utf8.RuneCountInString(out.Discovered[0].Note); got != contract.MaxDiscoveryLength {
		t.Fatalf("length = %d, want %d", got, contract.MaxDiscoveryLength)
	}
	if len(out.Notices) != 1 {
		t.Fatalf("notices = %v, want exactly one warning", out.Notices)
	}
	if !strings.Contains(out.Notices[0], "truncated") {
		t.Fatalf("notice does not warn: %q", out.Notices[0])
	}
	if out.Discovered[1].Note != "short enough" {
		t.Fatal("a discovery inside the cap was altered")
	}
	if err := out.Validate(testSpec()); err != nil {
		t.Fatalf("normalized report still refused: %v", err)
	}
	if utf8.RuneCountInString(report.Discovered[0].Note) != contract.MaxDiscoveryLength+40 {
		t.Fatal("Normalize mutated the report it was called on")
	}
}

// The cap counts characters, not bytes: cutting a multi-byte rune in half
// would turn valid text into invalid text.
func TestTruncationCountsRunesNotBytes(t *testing.T) {
	note := strings.Repeat("é", contract.MaxDiscoveryLength+10)
	report := contract.Report{
		Result:     map[string]any{"summary": "ok"},
		Verdict:    contract.VerdictOK,
		Discovered: []contract.Discovery{{Level: contract.ContextGlobal, Note: note}},
	}
	out := report.Normalize()
	if !utf8.ValidString(out.Discovered[0].Note) {
		t.Fatal("truncation produced invalid utf-8")
	}
	if got := utf8.RuneCountInString(out.Discovered[0].Note); got != contract.MaxDiscoveryLength {
		t.Fatalf("runes = %d, want %d", got, contract.MaxDiscoveryLength)
	}
}

// A task without a criterion cannot be reviewed, so it is refused at the
// boundary rather than discovered by the parent that has nothing to judge
// against.
func TestTaskRequiresACriterion(t *testing.T) {
	root := testRoot(t)
	vague := contract.Task{Objective: "make it better"}
	if _, err := root.Child("kid", "explorer", contract.AgentSpecialized, vague,
		[]contract.Effect{contract.EffectRead}, testLimits()); err == nil {
		t.Fatal("a task with no criterion was handed out")
	}
}

// Both kinds carry the same card. The kind is a field, which is what keeps
// there being one agent contract instead of two.
func TestBothKindsUseTheSameCard(t *testing.T) {
	if contract.AgentSpecialized == contract.AgentOrchestrator {
		t.Fatal("the two kinds are the same value")
	}
	if contract.AgentSpecialized.String() != "specialized" {
		t.Fatalf("name = %q", contract.AgentSpecialized.String())
	}
	parsed, err := contract.ParseAgentType("specialized")
	if err != nil {
		t.Fatalf("ParseAgentType: %v", err)
	}
	if parsed != contract.AgentSpecialized {
		t.Fatalf("parsed = %v", parsed)
	}

	root := testRoot(t)
	child, err := root.Child("kid", "explorer", contract.AgentSpecialized, testTask(),
		[]contract.Effect{contract.EffectRead}, testLimits())
	if err != nil {
		t.Fatalf("specialized child: %v", err)
	}
	if child.Kind != contract.AgentSpecialized {
		t.Fatalf("kind = %v", child.Kind)
	}
	if child.Version != root.Version {
		t.Fatalf("version = %v, want the parent's %v", child.Version, root.Version)
	}
}

// Clone is a deep copy: a card handed to a child must not share backing arrays
// with the parent that stamped it.
func TestAssignmentCloneDoesNotShareState(t *testing.T) {
	root := testRoot(t)
	copied := root.Clone()
	copied.Effects[0] = contract.EffectExternal
	copied.Task.Files[0] = "elsewhere.go"
	if root.Effects[0] == contract.EffectExternal {
		t.Fatal("clone shares the effects array")
	}
	if root.Task.Files[0] == "elsewhere.go" {
		t.Fatal("clone shares the files array")
	}
}
