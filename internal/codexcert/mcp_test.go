package codexcert

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func callCertificationMCP(t *testing.T, challenge Challenge, cursorPath string, cursor int) map[string]any {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "workflow.status", "arguments": map[string]any{"nonce": challenge.Nonce, "run_id": challenge.RunID, "workflow_id": challenge.WorkflowID, "invocation_id": challenge.InvocationID, "after_cursor": cursor}}}
	data, _ := json.Marshal(request)
	var out bytes.Buffer
	if err := ServeMCP(bytes.NewReader(append(data, '\n')), &out, challenge, cursorPath); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	return result
}

func TestCertificationMCPRequiresRealCursorContinuity(t *testing.T) {
	challenge := Challenge{Nonce: "nonce", RunID: "run", WorkflowID: "workflow", InvocationID: "invocation", ResultProof: "proof", ChecklistCount: 1}
	cursorPath := filepath.Join(t.TempDir(), "challenge.cursor")
	if got := callCertificationMCP(t, challenge, cursorPath, 1); got["isError"] != true {
		t.Fatal("cursor one passed before cursor zero")
	}
	if got := callCertificationMCP(t, challenge, cursorPath, 0); got["isError"] != false {
		t.Fatal("cursor zero did not establish continuity")
	}
	if got := callCertificationMCP(t, challenge, cursorPath, 0); got["isError"] != true {
		t.Fatal("cursor zero was replayed")
	}
	if got := callCertificationMCP(t, challenge, cursorPath, 1); got["isError"] != false {
		t.Fatal("cursor one did not resume established continuity")
	}
	if got := callCertificationMCP(t, challenge, cursorPath, 1); got["isError"] != true {
		t.Fatal("cursor one was replayed")
	}
}
