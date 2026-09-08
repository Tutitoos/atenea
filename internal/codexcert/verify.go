package codexcert

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PresentationContract is hashed into every certificate so changes invalidate it.
const PresentationContract = "atenea-codex-presentation/v1: nonce; one markdown notice before correlated MCP call; completed MCP JSONL markdown payload; activity; full checklist; 20-segment progress bar; completed turn; reconnect without replay"

const checklistLine = "- [x] **P30.** Certificación Codex"
const progressLine = "**Progreso:** `████████████████████` 100 % · 1/1 puntos completados"

// CLIObservation is the minimal non-content evidence extracted from JSONL.
type CLIObservation struct {
	Nonce, RunID, WorkflowID, InvocationID                                              string
	NoticeSequence, RequestSequence, ResponseSequence, ActivitySequence, RenderSequence int
	NoticeCount, RequestCount                                                           int
	ChecklistCount                                                                      int
	ProgressBar                                                                         string
	ReconnectReplay                                                                     bool
}

// VerifyCLIJSONL reads structured Codex output without retaining its text.
// Only typed item events count: agent text that merely contains protocol words
// cannot impersonate a tool request or result.
func VerifyCLIJSONL(raw []byte, nonce, runID, workflowID, invocationID, resultProof string, checklistCount int) error {
	obs := CLIObservation{Nonce: nonce, RunID: runID, WorkflowID: workflowID, InvocationID: invocationID, NoticeSequence: -1, RequestSequence: -1, ResponseSequence: -1, ActivitySequence: -1, RenderSequence: -1}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	sequence := 0
	toolID := ""
	totalNotices, totalToolCalls := 0, 0
	turnCompleted, turnFailed := -1, false
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		sequence++
		eventType, _ := event["type"].(string)
		if eventType == "turn.completed" {
			turnCompleted = sequence
		}
		if eventType == "turn.failed" {
			turnFailed = true
		}
		item, _ := event["item"].(map[string]any)
		itemType, _ := item["type"].(string)
		itemID, _ := item["id"].(string)
		text, _ := item["text"].(string)
		expectedNotice := fmt.Sprintf("**ATENEA · workflow.status** — consulto %s para verificar actividad y progreso.", invocationID)
		if eventType == "item.completed" && itemType == "agent_message" && strings.Contains(text, "ATENEA ·") {
			totalNotices++
			if strings.TrimSpace(text) == expectedNotice {
				obs.NoticeCount++
				if obs.NoticeSequence < 0 {
					obs.NoticeSequence = sequence
				}
			}
		}
		if eventType == "item.started" && itemType == "mcp_tool_call" {
			totalToolCalls++
			if item["server"] == "atenea" && item["tool"] == "workflow.status" && matchingArguments(item["arguments"], nonce, runID, workflowID, invocationID) {
				obs.RequestCount++
				toolID = itemID
				if obs.RequestSequence < 0 {
					obs.RequestSequence = sequence
				}
			}
		}
		if eventType == "item.completed" && itemType == "mcp_tool_call" && itemID == toolID && item["status"] == "completed" {
			if mcpResultFailed(item["result"]) {
				continue
			}
			result := flattenStrings(item["result"])
			if strings.Contains(result, resultProof) && strings.Contains(result, "activity=completed") && strings.Contains(result, runID) && strings.Contains(result, workflowID) {
				obs.ResponseSequence, obs.ActivitySequence = sequence, sequence
				activityAt, checklistAt, progressAt := strings.Index(result, "activity=completed"), strings.Index(result, checklistLine), strings.Index(result, progressLine)
				if activityAt >= 0 && checklistAt > activityAt && progressAt > checklistAt {
					obs.ChecklistCount = strings.Count(result, "[x]") + strings.Count(result, "[ ]")
					obs.ProgressBar = progressBar(result)
					obs.RenderSequence = sequence
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if obs.NoticeCount != 1 || obs.RequestCount != 1 || totalNotices != 1 || totalToolCalls != 1 {
		return fmt.Errorf("expected one correlated notice and tool call, got %d and %d", obs.NoticeCount, obs.RequestCount)
	}
	if obs.NoticeSequence < 0 || obs.RequestSequence <= obs.NoticeSequence || obs.ResponseSequence <= obs.RequestSequence || obs.ActivitySequence < obs.ResponseSequence || obs.RenderSequence < obs.ActivitySequence || turnCompleted <= obs.RenderSequence || turnFailed {
		return fmt.Errorf("codex CLI evidence order failed: notice=%d request=%d response=%d activity=%d render=%d notices=%d/%d calls=%d/%d checklist=%d bar_segments=%d", obs.NoticeSequence, obs.RequestSequence, obs.ResponseSequence, obs.ActivitySequence, obs.RenderSequence, obs.NoticeCount, totalNotices, obs.RequestCount, totalToolCalls, obs.ChecklistCount, len([]rune(obs.ProgressBar)))
	}
	if obs.ChecklistCount != checklistCount {
		return fmt.Errorf("checklist has %d items, want %d", obs.ChecklistCount, checklistCount)
	}
	if obs.ProgressBar != "████████████████████" {
		return errors.New("progress bar, percentage or counter does not match the deterministic contract")
	}
	if obs.ReconnectReplay {
		return errors.New("reconnect replayed a tool call")
	}
	return nil
}

func mcpResultFailed(value any) bool {
	if result, ok := value.(map[string]any); ok {
		failed, _ := result["isError"].(bool)
		return failed
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	var result map[string]any
	if json.Unmarshal([]byte(text), &result) != nil {
		return false
	}
	failed, _ := result["isError"].(bool)
	return failed
}

func matchingArguments(raw any, nonce, runID, workflowID, invocationID string) bool {
	args, ok := raw.(map[string]any)
	if !ok {
		if text, textOK := raw.(string); textOK {
			ok = json.Unmarshal([]byte(text), &args) == nil
		}
	}
	cursor, cursorOK := args["after_cursor"].(float64)
	return ok && cursorOK && cursor == 0 && args["nonce"] == nonce && args["run_id"] == runID && args["workflow_id"] == workflowID && args["invocation_id"] == invocationID
}

// VerifyCLIReconnectJSONL proves a fresh real client reads from cursor one
// without replaying the completed activity or checklist.
func VerifyCLIReconnectJSONL(raw []byte, nonce, runID, workflowID, invocationID, resultProof string) error {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	notice, started, completed, rendered := 0, 0, 0, 0
	totalNotices, totalToolCalls, sequence := 0, 0, 0
	noticeSequence, requestSequence, responseSequence, renderSequence := -1, -1, -1, -1
	replayedPresentation := false
	toolID := ""
	turnCompleted, turnFailed := -1, false
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		sequence++
		eventType, _ := event["type"].(string)
		if eventType == "turn.completed" {
			turnCompleted = sequence
		}
		if eventType == "turn.failed" {
			turnFailed = true
		}
		item, _ := event["item"].(map[string]any)
		itemType, _ := item["type"].(string)
		text, _ := item["text"].(string)
		expectedNotice := fmt.Sprintf("**ATENEA · workflow.status** — reanudo %s desde cursor 1 sin repetir actividad.", invocationID)
		if eventType == "item.completed" && itemType == "agent_message" {
			if strings.Contains(text, "ATENEA ·") {
				totalNotices++
				if strings.TrimSpace(text) == expectedNotice {
					notice++
					noticeSequence = sequence
				}
			}
			if strings.Contains(text, "[x]") || strings.Contains(text, "[ ]") || strings.Contains(text, "Progreso") || strings.Contains(text, "activity=completed") {
				replayedPresentation = true
			}
		}
		if eventType == "item.started" && itemType == "mcp_tool_call" {
			totalToolCalls++
			if item["server"] == "atenea" && item["tool"] == "workflow.status" && matchingReconnectArguments(item["arguments"], nonce, runID, workflowID, invocationID) {
				started++
				requestSequence = sequence
				toolID, _ = item["id"].(string)
			}
		}
		if eventType == "item.completed" && itemType == "mcp_tool_call" && item["id"] == toolID && item["status"] == "completed" {
			if mcpResultFailed(item["result"]) {
				continue
			}
			result := flattenStrings(item["result"])
			if strings.Contains(result, resultProof) && strings.Contains(result, "cursor=1") && strings.Contains(result, "activity=[]") && strings.Contains(result, "notices=[]") && !strings.Contains(result, "[x]") {
				completed++
				responseSequence = sequence
				rendered++
				renderSequence = sequence
			}
		}
		if eventType == "item.completed" && itemType == "agent_message" && rendered == 0 && strings.Contains(text, resultProof) && strings.Contains(text, "cursor") && !strings.Contains(text, "[x]") {
			rendered++
			renderSequence = sequence
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if notice != 1 || started != 1 || completed != 1 || rendered != 1 || totalNotices != 1 || totalToolCalls != 1 || replayedPresentation {
		return fmt.Errorf("reconnect evidence counts notice=%d started=%d completed=%d rendered=%d", notice, started, completed, rendered)
	}
	if noticeSequence < 0 || requestSequence <= noticeSequence || responseSequence <= requestSequence || renderSequence < responseSequence || turnCompleted <= renderSequence || turnFailed {
		return errors.New("reconnect evidence is not ordered notice, request, response, render")
	}
	return nil
}

func matchingReconnectArguments(raw any, nonce, runID, workflowID, invocationID string) bool {
	args, ok := raw.(map[string]any)
	if !ok {
		if text, textOK := raw.(string); textOK {
			ok = json.Unmarshal([]byte(text), &args) == nil
		}
	}
	cursor, cursorOK := args["after_cursor"].(float64)
	return ok && cursorOK && cursor == 1 && args["nonce"] == nonce && args["run_id"] == runID && args["workflow_id"] == workflowID && args["invocation_id"] == invocationID
}

// VerifyDesktopText validates a bounded accessibility observation in memory.
func VerifyDesktopText(text, nonce, runID, workflowID, invocationID, resultProof, desktopProcessReceipt string, checklistCount int) error {
	// Electron exposes adjacent Markdown spans as separate accessibility nodes.
	// Treat node boundaries like ordinary whitespace while preserving every
	// visible token and the ordering checks below.
	text = strings.Join(strings.Fields(text), " ")
	noticeLabel := "ATENEA · codex.certify.challenge"
	for _, value := range []string{nonce, runID, workflowID, invocationID, resultProof, "desktop_process_receipt=" + desktopProcessReceipt, "activity=completed", noticeLabel, "consulto", "para verificar actividad y progreso.", "P30. Certificación Codex", "████████████████████", "100 %", "1/1 puntos completados"} {
		if !strings.Contains(text, value) {
			return fmt.Errorf("desktop observation is missing %q", value)
		}
	}
	if strings.Count(text, noticeLabel) != 1 {
		return errors.New("desktop observation must contain exactly one ATENEA notice")
	}
	noticeAt, proofAt := strings.Index(text, noticeLabel), strings.Index(text, resultProof)
	checklistAt, progress := strings.Index(text, "P30. Certificación Codex"), strings.Index(text, "████████████████████")
	if noticeAt < 0 || proofAt <= noticeAt || checklistAt <= proofAt || progress <= checklistAt {
		return errors.New("desktop evidence is not ordered notice, invocation, progress")
	}
	if got := strings.Count(text, "P30. Certificación Codex"); got != checklistCount {
		return fmt.Errorf("desktop checklist has %d items, want %d", got, checklistCount)
	}
	if progressBar(text) != "████████████████████" {
		return errors.New("desktop progress bar, percentage or counter does not match the deterministic contract")
	}
	return nil
}

func flattenStrings(value any) string {
	var out strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out.WriteString(x)
			out.WriteByte('\n')
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			for key, item := range x {
				out.WriteString(key)
				out.WriteByte(' ')
				walk(item)
			}
		}
	}
	walk(value)
	return out.String()
}

func progressBar(text string) string {
	for _, line := range strings.Split(text, "\n") {
		start := strings.IndexAny(line, "█░")
		if start < 0 {
			continue
		}
		var out []rune
		for _, r := range line[start:] {
			if r != '█' && r != '░' {
				break
			}
			out = append(out, r)
		}
		if len(out) > 0 {
			return string(out)
		}
	}
	return ""
}
