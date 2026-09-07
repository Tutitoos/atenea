package config

import (
	"strings"
	"testing"
	"time"
)

func TestWorkflowProfilesMaterializeDigestAndRejectWideningAmbiguity(t *testing.T) {
	base, err := (fileWorkflow{Profile: "balanced", MaxParallelAgent: intPtr(2), MaxParallelReview: intPtr(1)}).build("test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildWorkflowProfiles("test", base, []fileWorkflowProfile{
		{Name: "balanced", Version: "v2", MaxBudgetUSD: floatPtr(3.5), MaxDuration: "2m", MaxRetries: intPtr(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ResolveWorkflowProfile(got.Profiles, got.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if profile.MaxBudgetUSD != 3.5 || profile.MaxDuration != 2*time.Minute || profile.MaxRetries != 2 {
		t.Fatalf("profile = %+v", profile)
	}
	if profile.Digest == "" || !strings.HasPrefix(profile.Digest, "sha256:") {
		t.Fatalf("digest = %q", profile.Digest)
	}
	if _, err := buildWorkflowProfiles("test", base, []fileWorkflowProfile{{Name: "balanced"}, {Name: "balanced"}}); err == nil {
		t.Fatal("duplicate workflow profiles were accepted")
	}
	if _, err := buildWorkflowProfiles("test", base, []fileWorkflowProfile{{Name: "other"}}); err == nil {
		t.Fatal("unknown selected workflow profile was accepted")
	}
}

func intPtr(value int) *int           { return &value }
func floatPtr(value float64) *float64 { return &value }
