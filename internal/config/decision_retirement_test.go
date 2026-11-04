package config_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
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
			refusedByName(t, err, "[decision]")
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
			refusedByName(t, err, strings.SplitN(table, "\n", 2)[0])
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
			refusedByName(t, err, strings.SplitN(table, "\n", 2)[0])
		})
	}
}

// refusedByName asserts the refusal names the retired section and the doc that
// explains the edit, not the bare "unknown key" a typo gets.
func refusedByName(t *testing.T, err error, section string) {
	t.Helper()
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("removed table was not refused as invalid_input: %v", err)
	}
	for _, want := range []string{section + " was removed in contract 5.0.0", "migration-5.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("error %q is the bare typo message", err)
	}
}

// Every retired section gets its own sentence, and a file carrying several
// is told about all of them at once.
func TestEveryRetiredSectionIsNamedWithItsRemedy(t *testing.T) {
	for section, table := range map[string]string{
		"[model]":              "[model]\nbinary = \"claude\"\n",
		"[[agent]]":            "[[agent]]\nname = \"reader\"\n",
		"[local_agents]":       "[local_agents]\neffects = [\"read\"]\n",
		"[workflow]":           "[workflow]\nmax_parallel_agent = 4\n",
		"[[workflow_profile]]": "[[workflow_profile]]\nname = \"workflow-v1\"\n",
		"[decision]":           "[decision]\ncapture_dir = \"\"\n",
		"[knowledge]":          "[knowledge]\nenabled = false\n",
	} {
		t.Run(section, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+"\n"+table))
			refusedByName(t, err, section)
		})
	}
	_, err := config.Load(write(t, minimal+"\n[workflow]\n[knowledge]\n"))
	if err == nil || !strings.Contains(err.Error(), "[workflow] was removed") || !strings.Contains(err.Error(), "[knowledge] was removed") {
		t.Fatalf("two retired tables were not both named: %v", err)
	}
}
