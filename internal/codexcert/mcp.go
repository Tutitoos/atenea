package codexcert

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ServeMCP exposes one read-only deterministic workflow.status challenge. It
// is launched only inside the disposable Codex CLI certification sandbox.
func ServeMCP(in io.Reader, out io.Writer, challenge Challenge, cursorPath string) error {
	scanner := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "atenea-codex-certification", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "workflow.status", "description": "Read the deterministic ATENEA certification workflow status.",
				"inputSchema": map[string]any{"type": "object", "required": []string{"nonce", "run_id", "workflow_id", "invocation_id", "after_cursor"}, "properties": map[string]any{
					"nonce": map[string]any{"type": "string"}, "run_id": map[string]any{"type": "string"}, "workflow_id": map[string]any{"type": "string"}, "invocation_id": map[string]any{"type": "string"}, "after_cursor": map[string]any{"type": "integer"},
				}},
			}}}
		case "tools/call":
			args := request.Params.Arguments
			cursor, cursorOK := args["after_cursor"].(float64)
			if request.Params.Name != "workflow.status" || len(args) != 5 || !cursorOK || (cursor != 0 && cursor != 1) || args["nonce"] != challenge.Nonce || args["run_id"] != challenge.RunID || args["workflow_id"] != challenge.WorkflowID || args["invocation_id"] != challenge.InvocationID {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "uncorrelated certification challenge"}}, "isError": true}
			} else if cursor == 1 {
				if err := consumeReconnectCursor(cursorPath, challenge); err != nil {
					result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "cursor continuity rejected"}}, "isError": true}
					break
				}
				body := fmt.Sprintf("nonce=%s\nrun=%s\nworkflow=%s\ninvocation=%s\nresult_proof=%s\ncursor=1\nactivity=[]\nnotices=[]\n", challenge.Nonce, challenge.RunID, challenge.WorkflowID, challenge.InvocationID, challenge.ResultProof)
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": body}}, "isError": false}
			} else {
				if err := establishCursor(cursorPath, challenge); err != nil {
					result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "cursor continuity rejected"}}, "isError": true}
					break
				}
				body := fmt.Sprintf("nonce=%s\nrun=%s\nworkflow=%s\ninvocation=%s\nresult_proof=%s\nactivity=completed\n\n- [x] **P30.** Certificación Codex\n\n**Progreso:** `████████████████████` 100 %% · 1/1 puntos completados", challenge.Nonce, challenge.RunID, challenge.WorkflowID, challenge.InvocationID, challenge.ResultProof)
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": body}}, "isError": false}
			}
		default:
			if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not supported"}}); err != nil {
				return err
			}
			continue
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func cursorDigest(challenge Challenge) string {
	return Hash(challenge.Nonce + ":" + challenge.RunID + ":" + challenge.WorkflowID + ":" + challenge.InvocationID + ":" + challenge.ResultProof)
}

func establishCursor(path string, challenge Challenge) error {
	if path == "" {
		return errors.New("cursor state path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(cursorDigest(challenge) + "\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func consumeReconnectCursor(path string, challenge Challenge) error {
	data, err := os.ReadFile(path)
	if err != nil || string(data) != cursorDigest(challenge)+"\n" {
		return errors.New("cursor zero was not delivered by this challenge")
	}
	marker := strings.TrimSuffix(path, ".cursor") + ".reconnect"
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}
