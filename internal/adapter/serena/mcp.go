package serena

// The MCP half of the adapter: JSON-RPC over streamable HTTP, and the parsing
// of what Serena hands back.
//
// It is kept apart from serena.go on purpose. That file is about meaning --
// which question is being asked, what the answer has to look like. This one is
// about wire format, and the two rot at different speeds: a new MCP revision
// changes this file and nothing else, while a fifth capability changes the
// other one and nothing here.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// rpcRequest is one JSON-RPC call. ID is omitted for notifications, which is
// what distinguishes them on the wire.
type rpcRequest struct {
	Version string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("serena rpc %d: %s", e.Code, e.Message) }

// toolResult is the shape of an MCP tools/call answer.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// IsError marks a tool that ran and refused, as opposed to a call that
	// never reached the tool. Both are failures; only this one carries a
	// message the far side wrote on purpose.
	IsError bool `json:"isError"`
}

// call runs one tool and returns its text. It assumes the lock is held: every
// exchange with Serena is serialized, including the handshake.
func (r *Runner) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	if err := r.handshake(ctx); err != nil {
		return "", err
	}
	raw, err := r.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var out toolResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("serena %s: unreadable answer: %w", tool, err)
	}
	var text strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	body := text.String()
	if out.IsError {
		// The text is the far side's own words about what went wrong, so it
		// goes back untouched for failureFor to bin. Rewording it here would
		// destroy the only evidence a human has.
		return "", fmt.Errorf("%s", strings.TrimSpace(body))
	}
	return body, nil
}

// handshake establishes the MCP session, once. A session that has gone away is
// not repaired here: the call fails, the catalog marks the provider down, and
// the next run establishes a new one. Reconnecting in silence would hide a
// server that is flapping.
func (r *Runner) handshake(ctx context.Context) error {
	if r.session != "" {
		return nil
	}
	body, err := json.Marshal(rpcRequest{
		Version: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "atenea", "version": "0"},
		},
	})
	if err != nil {
		return err
	}
	session, text, err := r.post(ctx, body, "")
	if err != nil {
		return err
	}
	if session == "" {
		return fmt.Errorf("serena handshake returned no session id: %s", clip(text))
	}
	if _, err := decode(text); err != nil {
		return err
	}
	r.session = session
	// The spec requires this notification before any tool call, and a server
	// that never receives it is entitled to refuse everything afterwards.
	note, err := json.Marshal(rpcRequest{Version: "2.0", Method: "notifications/initialized"})
	if err != nil {
		return err
	}
	if _, _, err := r.post(ctx, note, session); err != nil {
		r.session = ""
		return err
	}
	return nil
}

// rpc sends one request on the established session.
func (r *Runner) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	r.nextID++
	body, err := json.Marshal(rpcRequest{Version: "2.0", ID: r.nextID, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	_, text, err := r.post(ctx, body, r.session)
	if err != nil {
		// A dead session must not be reused: dropping it here is what lets the
		// next commission start clean instead of failing forever.
		r.session = ""
		r.active = ""
		return nil, err
	}
	return decode(text)
}

// post sends one message and returns the session id the server stamped on the
// answer, if any, plus the body.
//
// It returns the header rather than the response on purpose: the body is read
// and closed here, so handing back a *http.Response would be handing back a
// reader that is already spent and a trap for whoever reads this next.
func (r *Runner) post(ctx context.Context, body []byte, session string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Both are advertised because a streamable-HTTP server chooses: it may
	// answer as one JSON document or as a stream of SSE frames, and refusing
	// either would make the adapter depend on which mood the server is in.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("serena answered %s: %s", resp.Status, clip(string(text)))
	}
	return resp.Header.Get("Mcp-Session-Id"), string(text), nil
}

// decode reads one JSON-RPC response out of either transport framing.
func decode(text string) (json.RawMessage, error) {
	payload := strings.TrimSpace(text)
	if payload == "" {
		return nil, nil
	}
	if strings.HasPrefix(payload, "event:") || strings.HasPrefix(payload, "data:") {
		payload = sseData(payload)
		if payload == "" {
			return nil, fmt.Errorf("serena sent an event with no data: %s", clip(text))
		}
	}
	var out rpcResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("serena sent unreadable JSON: %s", clip(text))
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// sseData pulls the payload out of SSE framing. A single logical message may
// be split across several data: lines, which the spec says to rejoin.
func sseData(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			b.WriteString(strings.TrimSpace(after))
		}
	}
	return b.String()
}

// clip keeps an error message readable when a server answers with a page
// instead of a sentence.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
