package config

import "testing"

func TestDecisionSettingsDefaultToLocalRulesWithoutAService(t *testing.T) {
	settings, err := (fileDecision{}).build("test settings")
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != "rules" || settings.LayaEndpoint != "" || settings.Timeout <= 0 || settings.MinimumConfidence != 0.8 {
		t.Fatalf("decision settings = %+v", settings)
	}
}

func TestDecisionSettingsRequireAValidLayaEndpointWhenEnabled(t *testing.T) {
	for name, raw := range map[string]fileDecision{
		"missing endpoint": {Mode: "observe"},
		"unknown mode":     {Mode: "automatic", LayaEndpoint: "http://127.0.0.1:8000/v1/systemone"},
		"wrong route":      {Mode: "laya", LayaEndpoint: "http://127.0.0.1:8000/predict"},
		"remote plaintext": {Mode: "laya", LayaEndpoint: "http://laya.example.test/v1/systemone"},
		"query string":     {Mode: "laya", LayaEndpoint: "http://127.0.0.1:8000/v1/systemone?token=secret"},
		"embedded secret":  {Mode: "laya", LayaEndpoint: "https://user:secret@example.com/v1/systemone"},
		"invalid key name": {Mode: "laya", LayaEndpoint: "https://example.com/v1/systemone", LayaAPIKeyEnv: "key with spaces"},
		"unknown model":    {Mode: "laya", LayaEndpoint: "https://example.com/v1/systemone", LayaModel: "spanish"},
		"zero confidence":  {Mode: "laya", LayaEndpoint: "https://example.com/v1/systemone", MinimumConfidence: floatPointer(0)},
		"high confidence":  {Mode: "laya", LayaEndpoint: "https://example.com/v1/systemone", MinimumConfidence: floatPointer(1.1)},
		"bad timeout":      {Mode: "laya", LayaEndpoint: "https://example.com/v1/systemone", Timeout: "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := raw.build("test settings"); err == nil {
				t.Fatal("invalid decision settings were accepted")
			}
		})
	}
}

func TestDecisionSettingsAcceptAnExplicitLayaEndpoint(t *testing.T) {
	settings, err := (fileDecision{
		Mode:              "observe",
		LayaEndpoint:      "https://laya.example.test/v1/systemone",
		LayaModel:         "multilingual",
		LayaAPIKeyEnv:     "ATENEA_LAYA_API_KEY",
		Timeout:           "12s",
		MinimumConfidence: floatPointer(0.91),
	}).build("test settings")
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != "observe" || settings.LayaEndpoint != "https://laya.example.test/v1/systemone" ||
		settings.LayaModel != "multilingual" || settings.LayaAPIKeyEnv != "ATENEA_LAYA_API_KEY" ||
		settings.Timeout.String() != "12s" || settings.MinimumConfidence != 0.91 {
		t.Fatalf("settings = %+v", settings)
	}
}

func floatPointer(value float64) *float64 { return &value }
