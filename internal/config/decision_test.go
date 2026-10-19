package config

import "testing"

func TestDecisionSettingsDefaultToLocalRulesWithoutAService(t *testing.T) {
	settings, err := (fileDecision{}).build("test settings")
	if err != nil {
		t.Fatal(err)
	}
	if settings.CaptureDir != "" {
		t.Fatalf("decision settings = %+v", settings)
	}
}

func TestDecisionCaptureRequiresAbsolutePrivateDestinationAtUse(t *testing.T) {
	if _, err := (fileDecision{CaptureDir: "relative/cases"}).build("test settings"); err == nil {
		t.Fatal("relative capture destination accepted")
	}
	settings, err := (fileDecision{CaptureDir: "/private/atenea-cases"}).build("test settings")
	if err != nil || settings.CaptureDir != "/private/atenea-cases" {
		t.Fatalf("capture settings = %+v, %v", settings, err)
	}
}
