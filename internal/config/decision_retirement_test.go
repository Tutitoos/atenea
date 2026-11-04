package config_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestRemovedDecisionSettingsAreRefused(t *testing.T) {
	for _, setting := range []string{
		`mode = "observe"`,
		`laya_endpoint = "http://127.0.0.1:8766/v1/systemone"`,
		`laya_model = "multilingual"`,
		`laya_api_key_env = "PRIVATE_CLASSIFIER_TOKEN"`,
		`timeout = "10s"`,
		`minimum_confidence = 0.8`,
		`capture_dir = ""`,
		`capture_dir = "/private/atenea-cases"`,
		`# the table itself, with no key`,
	} {
		t.Run(strings.SplitN(setting, " =", 2)[0], func(t *testing.T) {
			_, err := config.Load(write(t, minimal+"\n[decision]\n"+setting+"\n"))
			if err == nil || !strings.Contains(err.Error(), "unknown key") {
				t.Fatalf("removed decision setting was not refused: %v", err)
			}
		})
	}
}

// The workflow engine and the knowledge store are gone (#212). A settings file
// that still carries their tables must fail at load, not run as if it worked.
func TestRemovedWorkflowAndKnowledgeTablesAreRefused(t *testing.T) {
	for name, table := range map[string]string{
		"workflow":                  "[workflow]\nmax_parallel_agent = 4\nmax_parallel_review = 4\n",
		"workflow, bare table":      "[workflow]\n",
		"workflow_profile":          "[[workflow_profile]]\nname = \"workflow-v1\"\nversion = \"workflow-v1\"\n",
		"workflow_profile, no keys": "[[workflow_profile]]\n",
		"knowledge":                 "[knowledge]\nenabled = false\n",
		"knowledge, enabled":        "[knowledge]\nenabled = true\npath = \"/private/knowledge.sqlite\"\nworkflow_path = \"/private/traces.db\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+"\n"+table))
			if err == nil || !strings.Contains(err.Error(), "unknown key") {
				t.Fatalf("removed table was not refused: %v", err)
			}
		})
	}
}

// The built-in agents are gone (#212): `[model]` was the seam they called a
// model through, `[[agent]]` declared them and `[local_agents]` capped the ones
// a repository added. A settings file that still carries any of them must fail
// at load, not run as if it worked.
func TestRemovedAgentTablesAreRefused(t *testing.T) {
	for name, table := range map[string]string{
		"model":                "[model]\nbinary = \"claude\"\nexplore = \"haiku\"\ntimeout = \"180s\"\n",
		"model, bare table":    "[model]\n",
		"model, codex backend": "[model]\nbackend = \"codex\"\ncodex_native = true\n",
		"agent":                "[[agent]]\nname = \"reader\"\nkind = \"specialized\"\nsummary = \"Reads a file\"\ncommand = \"$atenea\"\nargs = [\"agent-exec\", \"reader\"]\ncontext = [\"repository\"]\neffects = [\"read\"]\nmax_duration = \"10m\"\nmax_tokens = 1000\n",
		"agent, no keys":       "[[agent]]\n",
		"local_agents":         "[local_agents]\neffects = [\"read\"]\ncontext = [\"repository\"]\n",
		"local_agents, bare":   "[local_agents]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+"\n"+table))
			if err == nil || !strings.Contains(err.Error(), "unknown key") {
				t.Fatalf("removed table was not refused: %v", err)
			}
		})
	}
}
