package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writingPlanFixture is a graph whose one step declares `write`, and settings
// declaring an agent allowed to cause it. Nothing spawns: create compiles, and
// the refusals under test happen before anything is launched.
func writingPlanFixture(t *testing.T, extraRepositories int) (settings, plan string) {
	t.Helper()
	repo := t.TempDir()
	plan = filepath.Join(repo, "plan.toml")
	body := "task = \"a plan that would write\"\nbudget_usd = 1.00\n\n" +
		"[[step]]\nid = \"edit\"\nagent = \"writer\"\n" +
		"objective = \"change the file\"\ncriterion = \"it changed\"\n" +
		"effects = [\"read\", \"write\"]\nbudget_usd = 0.25\n"
	if err := os.WriteFile(plan, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	settings = strings.Replace(socketSettings, `path = "/tmp"`, fmt.Sprintf("path = %q", repo), 1)
	settings = strings.Replace(settings, "[orchestrator]\n",
		"[orchestrator]\nclient_effects = [\"write\", \"external\"]\n", 1)
	for i := range extraRepositories {
		settings += fmt.Sprintf("\n[[repository]]\nid = %q\npath = %q\nlanguages = [\"go\"]\n",
			fmt.Sprintf("second%d", i), t.TempDir())
	}
	settings += "\n[[agent]]\nname = \"writer\"\nkind = \"specialized\"\n" +
		"summary = \"Would write, if anything ever launched it\"\ncommand = \"/bin/true\"\n" +
		"context = [\"repository\"]\neffects = [\"read\", \"write\"]\n" +
		"max_duration = \"5s\"\nmax_tokens = 100\n\n" +
		"  [[agent.result]]\n  name = \"ok\"\n  type = \"bool\"\n  required = true\n  summary = \"it wrote\"\n"
	return settings, plan
}

// narrowed opens a chat that asks to hold less than the machine grants it.
func narrowed(t *testing.T, c *client, grant []string) map[string]any {
	t.Helper()
	out := c.call("initialize", map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities": map[string]any{
			"experimental": map[string]any{
				"atenea": map[string]any{"grant": grant},
			},
		},
		"clientInfo": map[string]any{"name": "omp", "version": "1.0.0"},
	})
	c.notify("notifications/initialized", nil)
	return out
}

// The gap this closes: every other tools/call path is held to the chat's grant
// -- the capability path through Session.Ask, the raw path in rawCall -- and
// these two were not. A client saying it will only read could describe a graph
// whose steps declare write, and then launch it. The effects arrive inside a
// file, so no schema can catch this.
func TestAChatMayNotDescribeAWorkflowItCannotAuthorize(t *testing.T) {
	settings, plan := writingPlanFixture(t, 0)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	result(t, narrowed(t, c, []string{}), "initialize")

	got := result(t, c.call("tools/call", map[string]any{
		"name":      "workflow.create",
		"arguments": map[string]any{"file": plan},
	}), "workflow.create")

	if got["isError"] != true {
		t.Fatalf("a read-only chat wrote down a graph that declares write: %v", got)
	}
	if text := answerText(got); !strings.Contains(text, "write") {
		t.Errorf("refusal = %q, want it to name the effect that was refused", text)
	}
}

// The same chat, granted what the graph declares, gets through. Without this
// the test above would pass on a create that is simply broken.
func TestAChatGrantedWriteMayDescribeAWorkflowThatWrites(t *testing.T) {
	settings, plan := writingPlanFixture(t, 0)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	result(t, narrowed(t, c, []string{"write"}), "initialize")

	got := result(t, c.call("tools/call", map[string]any{
		"name":      "workflow.create",
		"arguments": map[string]any{"file": plan},
	}), "workflow.create")

	if got["isError"] == true {
		t.Fatalf("a chat granted write could not describe a graph that writes: %v", answerText(got))
	}
}

// A launch may arrive on a different connection, with a different grant, long
// after the plan was drawn. So the effects are re-read from the run rather than
// trusted from whoever created it.
func TestALaunchIsHeldToTheGrantOfTheChatThatLaunches(t *testing.T) {
	settings, plan := writingPlanFixture(t, 0)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	author := dial(t)
	result(t, narrowed(t, author, []string{"write"}), "initialize")
	created := result(t, author.call("tools/call", map[string]any{
		"name":      "workflow.create",
		"arguments": map[string]any{"file": plan},
	}), "workflow.create")
	structured, _ := created["structuredContent"].(map[string]any)
	id, _ := structured["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %v", created)
	}

	reader := dial(t)
	result(t, narrowed(t, reader, []string{}), "initialize")
	got := result(t, reader.call("tools/call", map[string]any{
		"name":      "workflow.launch",
		"arguments": map[string]any{"id": id},
	}), "workflow.launch")

	if got["isError"] != true {
		t.Fatalf("a read-only chat launched a graph that declares write: %v", got)
	}
	if text := answerText(got); !strings.Contains(text, "write") {
		t.Errorf("refusal = %q, want it to name the effect that was refused", text)
	}
}

// An unaimed workflow used to resolve its repository from the working directory
// of the PROCESS -- which for the service is wherever its unit file left it,
// typically $HOME. The capability path has always refused this; these two fell
// through it.
func TestAWorkflowMustBeAimedWhenTheMachineHasAChoice(t *testing.T) {
	settings, plan := writingPlanFixture(t, 1)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	result(t, narrowed(t, c, []string{"write"}), "initialize")

	answer := c.call("tools/call", map[string]any{
		"name":      "workflow.create",
		"arguments": map[string]any{"file": plan},
	})
	errObj, ok := answer["error"].(map[string]any)
	if !ok {
		t.Fatalf("a workflow ran against a repository nobody named: %v", answer)
	}
	message, _ := errObj["message"].(string)
	if !strings.Contains(message, "repository is required") {
		t.Errorf("refusal = %q, want it to say the call has to be aimed", message)
	}
}

// With one repository there is no choice to make, so demanding its name would
// be ceremony the CLI does not ask for either.
func TestASingleRepositoryStillNeedsNoAim(t *testing.T) {
	settings, plan := writingPlanFixture(t, 0)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	result(t, narrowed(t, c, []string{"write"}), "initialize")

	got := result(t, c.call("tools/call", map[string]any{
		"name":      "workflow.create",
		"arguments": map[string]any{"file": plan},
	}), "workflow.create")
	if got["isError"] == true {
		t.Fatalf("the only declared repository still had to be named: %v", answerText(got))
	}
}

// A caller cannot aim a tool at a repository the schema never mentions.
func TestTheWorkflowToolsDeclareTheRepositoryArgument(t *testing.T) {
	settings, _ := writingPlanFixture(t, 1)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	result(t, c.handshake("omp"), "initialize")
	listed := result(t, c.call("tools/list", map[string]any{}), "tools/list")
	tools, _ := listed["tools"].([]any)

	seen := 0
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		if name != "workflow.create" && name != "workflow.launch" {
			continue
		}
		seen++
		schema, _ := tool["inputSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if _, ok := properties["repository"]; !ok {
			t.Errorf("%s declares no repository argument: %v", name, properties)
		}
		required, _ := schema["required"].([]any)
		found := false
		for _, item := range required {
			if fmt.Sprint(item) == "repository" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not require a repository on a machine with two: %v", name, required)
		}
	}
	if seen != 2 {
		t.Fatalf("workflow tools on the list = %d, want 2", seen)
	}
}

// readingPlanFixture is the writing fixture's harmless twin: one step, one
// agent, `read` and nothing else. It exists so a launch can be run to the end
// under an ordinary grant, which the writing fixture deliberately cannot do.
// The agent command is /bin/true, so a step is really dispatched and really
// comes back with nothing -- the run finishes, incomplete, which is a real
// outcome and costs nothing to produce.
func readingPlanFixture(t *testing.T) (settings, plan string) {
	t.Helper()
	repo := t.TempDir()
	plan = filepath.Join(repo, "plan.toml")
	body := "task = \"a plan that only reads\"\nbudget_usd = 1.00\n\n" +
		"[[step]]\nid = \"look\"\nagent = \"reader\"\n" +
		"objective = \"read the file\"\ncriterion = \"it was read\"\n" +
		"effects = [\"read\"]\nbudget_usd = 0.25\n"
	if err := os.WriteFile(plan, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	settings = strings.Replace(socketSettings, `path = "/tmp"`, fmt.Sprintf("path = %q", repo), 1)
	settings += "\n[[agent]]\nname = \"reader\"\nkind = \"specialized\"\n" +
		"summary = \"Reads, and reports nothing\"\ncommand = \"/bin/true\"\n" +
		"context = [\"repository\"]\neffects = [\"read\"]\n" +
		"max_duration = \"5s\"\nmax_tokens = 100\n\n" +
		"  [[agent.result]]\n  name = \"ok\"\n  type = \"bool\"\n  required = true\n  summary = \"it read\"\n"
	return settings, plan
}

// workflow.launch is the tool that commits the grant and spends the money, and
// it was the one with no test of its own: create was exercised from several
// angles while launch was reached only by the refusal above, which returns
// before the engine is ever asked to run anything. So the answer's shape --
// the id, the state, the summary and the per-state counts a caller reads to
// know what happened -- was never checked against the code that builds it.
func TestALaunchAnswersWithTheRunItFinished(t *testing.T) {
	settings, plan := readingPlanFixture(t)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	created := result(t, c.call("tools/call", map[string]any{
		"name":      "workflow.create",
		"arguments": map[string]any{"file": plan},
	}), "workflow.create")
	drawn, _ := created["structuredContent"].(map[string]any)
	id, _ := drawn["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %v", created)
	}

	launched := result(t, c.call("tools/call", map[string]any{
		"name":      "workflow.launch",
		"arguments": map[string]any{"id": id},
	}), "workflow.launch")
	if launched["isError"] == true {
		t.Fatalf("launching a read-only graph from a chat that holds read failed: %v", answerText(launched))
	}
	out, _ := launched["structuredContent"].(map[string]any)
	if out["id"] != id {
		t.Errorf("id = %v, want the run that was launched (%s)", out["id"], id)
	}
	// The agent is /bin/true and reports none of the results it declares, so
	// the run really finishes and really has nothing to show for it. Both
	// halves matter: "finished" is the engine saying it ran the graph out, and
	// the counts are how a caller learns that finishing is not succeeding.
	if out["state"] != "finished" {
		t.Errorf("state = %v, want finished: the graph was run to the end", out["state"])
	}
	if summary, _ := out["summary"].(string); summary == "" {
		t.Error("the answer carries no summary, so a caller has nothing to read back")
	}
	counts, _ := out["steps"].(map[string]any)
	if len(counts) == 0 {
		t.Errorf("steps = %v, want a count per state for the one step in the graph", out["steps"])
	}
}

// The two refusals launch owes a caller, and neither is a tool result: a
// launch with nothing to launch is a malformed request, not a graph that went
// badly, so it comes back as a JSON-RPC error against the call.
func TestALaunchWithNoUsableIDIsRefusedByName(t *testing.T) {
	settings, _ := readingPlanFixture(t)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")

	// Whitespace, not the empty string: a trimmed id is the shape a caller
	// actually sends when a template filled in nothing.
	blank := c.call("tools/call", map[string]any{
		"name":      "workflow.launch",
		"arguments": map[string]any{"id": "   "},
	})
	refusal, _ := blank["error"].(map[string]any)
	if refusal == nil {
		t.Fatalf("a launch with a blank id was answered rather than refused: %v", blank)
	}
	if message, _ := refusal["message"].(string); !strings.Contains(message, "id is required") {
		t.Errorf("message = %q, want it to name what was missing", message)
	}

	// An id that no run answers to is the other half: the caller named
	// something, and the honest answer is that it is not here.
	missing := c.call("tools/call", map[string]any{
		"name":      "workflow.launch",
		"arguments": map[string]any{"id": "wf-no-such-run"},
	})
	gone, _ := missing["error"].(map[string]any)
	if gone == nil {
		t.Fatalf("a launch of an unknown id was answered rather than refused: %v", missing)
	}
	if message, _ := gone["message"].(string); !strings.Contains(message, "wf-no-such-run") {
		t.Errorf("message = %q, want it to name the id that was not found", message)
	}
}
