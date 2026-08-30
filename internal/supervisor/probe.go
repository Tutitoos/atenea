package supervisor

// probe speaks just enough MCP to answer one question: is a real MCP server
// listening and willing to open a session. It is a second, small
// implementation of the same wire format internal/adapter/serena/mcp.go
// already speaks, kept apart on purpose -- that file is the adapter's own
// connection, held open and reused across calls; this one is a health check
// that opens a session, learns the answer, and throws it away. Sharing code
// between the two would mean the adapter reaching into a package that exists
// to watch it, which is the wrong direction for that dependency to point.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func probeHTTP(ctx context.Context, client *http.Client, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("answered %s", resp.Status)
	}
	return nil
}

// protocolVersion is the MCP revision the probe declares in its handshake.
// It matches internal/adapter/serena/mcp.go's own constant: a server that
// cannot answer this revision should say so at the handshake, which is
// exactly the failure this probe exists to catch before a real call does.
const protocolVersion = "2025-06-18"

type probeRequest struct {
	Version string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type probeResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *probeError     `json:"error"`
}

type probeError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *probeError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// probeReady sends one MCP initialize request and reports whether a real
// server answered it. It never repairs a session, never sends the
// notifications/initialized that would normally follow, and does not close
// what streamable HTTP calls a session: a probe that behaved like a client
// health-checking a server, not a client settling in to work with one.
func probeReady(ctx context.Context, client *http.Client, endpoint string) error {
	body, err := json.Marshal(probeRequest{
		Version: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "atenea-supervisor", "version": "0"},
		},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Both are advertised for the same reason mcp.go advertises both: a
	// streamable-HTTP server may answer as one document or as an SSE frame,
	// and the probe has to accept whichever mood it is in.
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := readProbePayload(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("answered %s: %s", resp.Status, clipText(text))
	}
	_, err = decodeProbe(text)
	return err
}

// readProbePayload reads one response document without waiting for a
// streamable-HTTP session to close. Serena and other MCP servers keep an SSE
// response open for the life of the session; io.ReadAll would therefore turn a
// successful initialize into a timeout, cancel the request, and make the
// server report ClientDisconnect. A readiness probe only needs the first
// JSON-RPC frame.
func readProbePayload(resp *http.Response) (string, error) {
	limited := io.LimitReader(resp.Body, 64<<10)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(limited)
		var data strings.Builder
		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimSpace(after))
				// A complete JSON frame can be returned before the server emits
				// the event's blank-line terminator; this is the normal one-line
				// shape and avoids holding the stream open unnecessarily.
				if _, err := decodeProbe(data.String()); err == nil {
					return data.String(), nil
				}
			}
			if line == "" && data.Len() > 0 {
				return data.String(), nil
			}
		}
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return data.String(), nil
	}
	body, err := io.ReadAll(limited)
	return string(body), err
}

// decodeProbe reads one JSON-RPC response out of either transport framing,
// the same two shapes mcp.go's own decode handles.
func decodeProbe(text string) (json.RawMessage, error) {
	payload := strings.TrimSpace(text)
	if payload == "" {
		return nil, fmt.Errorf("empty answer")
	}
	if strings.HasPrefix(payload, "event:") || strings.HasPrefix(payload, "data:") {
		payload = sseProbeData(payload)
		if payload == "" {
			return nil, fmt.Errorf("an event with no data: %s", clipText(text))
		}
	}
	var out probeResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("unreadable JSON: %s", clipText(text))
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// sseProbeData pulls the payload out of SSE framing, rejoining a message
// split across several data: lines as the spec asks.
func sseProbeData(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			b.WriteString(strings.TrimSpace(after))
		}
	}
	return b.String()
}

// clipText keeps an error message readable when a server answers with a page
// instead of a sentence.
func clipText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
