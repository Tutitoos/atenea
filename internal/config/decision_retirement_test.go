package config_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestRemovedDecisionClassifierSettingsAreRefused(t *testing.T) {
	for _, setting := range []string{
		`mode = "observe"`,
		`laya_endpoint = "http://127.0.0.1:8766/v1/systemone"`,
		`laya_model = "multilingual"`,
		`laya_api_key_env = "PRIVATE_CLASSIFIER_TOKEN"`,
		`timeout = "10s"`,
		`minimum_confidence = 0.8`,
	} {
		t.Run(strings.SplitN(setting, " =", 2)[0], func(t *testing.T) {
			_, err := config.Load(write(t, minimal+"\n[decision]\n"+setting+"\n"))
			if err == nil || !strings.Contains(err.Error(), "unknown key") {
				t.Fatalf("removed decision setting was not refused: %v", err)
			}
		})
	}
}
