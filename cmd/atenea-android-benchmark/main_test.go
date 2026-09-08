package main

import "testing"

func TestSummarizeUsesOnlySuccessfulSamples(t *testing.T) {
	op := operation{Samples: []sample{{DurationMS: 10, Bytes: 100, Outcome: "observed", Verification: "observed"}, {DurationMS: 20, Bytes: 200, Outcome: "observed", Verification: "observed"}, {DurationMS: 1, Outcome: "failed", Verification: "unknown"}}}
	summarize(&op)
	if op.MedianMS != 10 || op.P95MS != 10 || op.MedianB != 100 || op.Verification != "observed" {
		t.Fatalf("summary = %#v", op)
	}
}

func TestSummarizeMarksAnAllFailedOperationUnknown(t *testing.T) {
	op := operation{Samples: []sample{{DurationMS: 10, Outcome: "failed", Verification: "unknown"}}}
	summarize(&op)
	if op.Verification != "unknown" || op.MedianMS != 0 || op.P95MS != 0 {
		t.Fatalf("summary = %#v", op)
	}
}

func TestVerificationForDistinguishesSentAndSelectorVerified(t *testing.T) {
	if got := verificationFor("sent"); got != "action_sent" {
		t.Fatalf("sent verification = %q", got)
	}
	if got := verificationFor("selector_verified_action_sent"); got != "selector_verified_action_sent" {
		t.Fatalf("semantic verification = %q", got)
	}
}

func TestNotificationShadeFocused(t *testing.T) {
	if !notificationShadeFocused("  mCurrentFocus=Window{abc u0 NotificationShade}\n") {
		t.Fatal("notification shade focus was not detected")
	}
	if notificationShadeFocused("  mFocusedApp=ActivityRecord{abc u0 app/.Main}\n") {
		t.Fatal("non-focus window dump was treated as a notification shade")
	}
}

func TestHelperFixtureFocusedRequiresCurrentFocus(t *testing.T) {
	if !helperFixtureFocused("mCurrentFocus=Window{abc u0 io.atenea.androidhelper/.MainActivity}\n") {
		t.Fatal("helper fixture current focus was not detected")
	}
	if helperFixtureFocused("mFocusedApp=ActivityRecord{abc u0 io.atenea.androidhelper/.MainActivity}\nmCurrentFocus=Window{abc u0 NotificationShade}\n") {
		t.Fatal("focused app was treated as current focus")
	}
}

func TestHasFailedOperation(t *testing.T) {
	if hasFailedOperation(report{Operations: []operation{{Name: "semantic", Succeeded: 1}}}) {
		t.Fatal("successful report was marked failed")
	}
	if !hasFailedOperation(report{Operations: []operation{{Name: "semantic", Failed: 1}}}) {
		t.Fatal("failed operation was not detected")
	}
}

func TestHasFailedNamedOperation(t *testing.T) {
	report := report{Operations: []operation{{Name: "key_home_sent", Failed: 1}, {Name: "semantic_selector_key_home", Succeeded: 1}}}
	if hasFailedNamedOperation(report, "semantic_selector_key_home") {
		t.Fatal("unrelated failed operation was treated as a semantic failure")
	}
	if !hasFailedNamedOperation(report, "key_home_sent") {
		t.Fatal("named failed operation was not detected")
	}
}
