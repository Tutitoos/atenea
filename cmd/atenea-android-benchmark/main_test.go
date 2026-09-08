package main

import "testing"

func TestSummarizeUsesOnlySuccessfulSamples(t *testing.T) {
	op := operation{Samples: []sample{{DurationMS: 10, Bytes: 100, Outcome: "observed", Verification: "observed"}, {DurationMS: 20, Bytes: 200, Outcome: "observed", Verification: "observed"}, {DurationMS: 1, Outcome: "failed", Verification: "unknown"}}}
	summarize(&op)
	if op.MedianMS != 10 || op.P95MS != 10 || op.MedianB != 100 || op.Verification != "observed" {
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
