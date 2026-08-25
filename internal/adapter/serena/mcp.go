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
	"strconv"
	"strings"

	"github.com/Tutitoos/atenea/internal/toolversion"
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

// call runs one tool on c and returns its text.
//
// It assumes c.mu is held, which serializes COMMISSIONS on this endpoint and
// not the calls inside one: symbol.overview's locateAll fans out up to
// maxConcurrentSymbolLookups of these inside a single hold. An earlier version
// of this comment claimed everything here was serialized including the
// handshake, and the handshake was written to match -- a check-then-act with
// the lock released in between, which under that fan-out let several
// goroutines each decide no session existed and each run initialize. What
// serializes the handshake is c.handshakeMu; what protects the fields is
// c.wireMu; c.mu protects neither from a sibling.
func (r *Runner) call(ctx context.Context, c *conn, tool string, args map[string]any) (string, error) {
	if err := r.handshake(ctx, c); err != nil {
		return "", err
	}
	raw, err := r.rpc(ctx, c, "tools/call", map[string]any{"name": tool, "arguments": args})
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
		if body == "" {
			// isError with nothing in content is a server that flagged a
			// failure and then said nothing about it. An error built from
			// that would trim down to "", which is worse than no evidence:
			// it looks like the call answered instead of the call failing
			// silently. The raw frame is the only text left to show.
			return "", fmt.Errorf("serena %s reported an error with no message: %s", tool, clip(string(raw)))
		}
		return "", fmt.Errorf("%s", strings.TrimSpace(body))
	}
	return body, nil
}

// handshake establishes the MCP session on c, once. A session that has gone
// away is not repaired here: the call fails, the catalog marks the provider
// down, and the next run establishes a new one. Reconnecting in silence would
// hide a server that is flapping.
func (r *Runner) handshake(ctx context.Context, c *conn) error {
	c.wireMu.Lock()
	established := c.session != ""
	c.wireMu.Unlock()
	if established {
		return nil
	}
	// The established session is read without handshakeMu above so the
	// ordinary call pays nothing, and re-read under it below because the first
	// read decides nothing: between the two, a sibling goroutine may have
	// completed the whole exchange. Only one initialize per session gets sent.
	c.handshakeMu.Lock()
	defer c.handshakeMu.Unlock()
	c.wireMu.Lock()
	established = c.session != ""
	c.wireMu.Unlock()
	if established {
		return nil
	}
	body, err := json.Marshal(rpcRequest{
		Version: "2.0",
		ID:      handshakeID,
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
	reply, err := r.post(ctx, c, body, "")
	if err != nil {
		return err
	}
	if reply.session == "" {
		return fmt.Errorf("serena handshake returned no session id: %s", clip(reply.body))
	}
	result, err := decode(reply, handshakeID)
	if err != nil {
		return err
	}
	// The version rides in on the handshake Atenea already pays for, so
	// filing measurements under the language server that actually produced
	// them costs nothing extra. A server that does not introduce itself
	// leaves it empty, which is a fact rather than a guess.
	var hello struct {
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	c.wireMu.Lock()
	if json.Unmarshal(result, &hello) == nil {
		c.version = toolversion.Clean(hello.ServerInfo.Version)
	}
	c.session = reply.session
	c.wireMu.Unlock()
	// The spec requires this notification before any tool call, and a server
	// that never receives it is entitled to refuse everything afterwards.
	note, err := json.Marshal(rpcRequest{Version: "2.0", Method: "notifications/initialized"})
	if err != nil {
		return err
	}
	if _, err := r.post(ctx, c, note, reply.session); err != nil {
		c.wireMu.Lock()
		c.session = ""
		c.wireMu.Unlock()
		return err
	}
	return nil
}

// rpc sends one request on c's established session.
func (r *Runner) rpc(ctx context.Context, c *conn, method string, params any) (json.RawMessage, error) {
	c.wireMu.Lock()
	c.nextID++
	id := c.nextID
	session := c.session
	c.wireMu.Unlock()
	body, err := json.Marshal(rpcRequest{Version: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	reply, err := r.post(ctx, c, body, session)
	if err != nil {
		// A dead session must not be reused: dropping it here is what lets the
		// next commission start clean instead of failing forever.
		//
		// c.active is deliberately NOT cleared here. Which project this Serena
		// is pointed at is the business of whoever holds c.mu -- activate sets
		// it and clears it on its own failure -- and one of locateAll's
		// sixteen concurrent siblings failing its POST says nothing about the
		// project the other fifteen are still asking about. Clearing it from
		// here made the next commission re-activate for no reason, which on a
		// monorepo is the project walk that hangs.
		c.wireMu.Lock()
		c.session = ""
		c.wireMu.Unlock()
		return nil, err
	}
	return decode(reply, id)
}

// handshakeID is the JSON-RPC id the initialize request carries. It is a
// constant because decode has to know which reply in an SSE stream belongs to
// it, and the handshake is the one request whose id is not drawn from nextID.
const handshakeID = 1

// answer is one HTTP exchange, read to completion.
//
// contentType is carried because it is what decides the framing: a streamable
// HTTP server may answer one request as a JSON document and the next as an SSE
// stream, and the server says which in the header rather than leaving it to be
// guessed from the first few bytes of the body.
type answer struct {
	session     string
	contentType string
	body        string
}

// post sends one message to c's endpoint and returns what came back.
//
// It returns the read body rather than the response on purpose: the body is
// read and closed here, so handing back a *http.Response would be handing back
// a reader that is already spent and a trap for whoever reads this next.
func (r *Runner) post(ctx context.Context, c *conn, body []byte, session string) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return answer{}, err
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
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, err
	}
	if resp.StatusCode >= 400 {
		return answer{}, fmt.Errorf("serena answered %s: %s", resp.Status, clip(string(text)))
	}
	return answer{
		session:     resp.Header.Get("Mcp-Session-Id"),
		contentType: resp.Header.Get("Content-Type"),
		body:        string(text),
	}, nil
}

// decode reads the JSON-RPC response to request id out of either framing.
//
// The id matters because an SSE body is a STREAM: a server is free to send
// progress notifications, or replies to other requests, in the same response,
// each as its own event. The previous version of this concatenated the data of
// every event in the body into one string and parsed that, which produces
// valid JSON only when exactly one event ever arrives.
func decode(reply answer, id int) (json.RawMessage, error) {
	payload := strings.TrimSpace(reply.body)
	if payload == "" {
		return nil, nil
	}
	if isEventStream(reply) {
		found, ok := sseReply(reply.body, id)
		if !ok {
			return nil, fmt.Errorf("serena sent no event carrying a reply to request %d: %s", id, clip(reply.body))
		}
		payload = found
	}
	var out rpcResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("serena sent unreadable JSON: %s", clip(reply.body))
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// isEventStream decides the framing from the header the server set, which is
// the field that exists to say so.
//
// The body is still sniffed when the header says nothing useful. That is not
// belt and braces for its own sake: a proxy that drops or rewrites
// Content-Type would otherwise send a body that is plainly SSE into the JSON
// decoder, and "unreadable JSON" is a much worse thing to tell a reader than
// the answer they actually got.
func isEventStream(reply answer) bool {
	if media, _, ok := strings.Cut(reply.contentType, ";"); ok || media != "" {
		if strings.EqualFold(strings.TrimSpace(media), "text/event-stream") {
			return true
		}
		if strings.TrimSpace(media) != "" {
			return false
		}
	}
	trimmed := strings.TrimSpace(reply.body)
	return strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, "data:")
}

// sseReply finds the event carrying the reply to id and returns its data.
//
// Events are separated by a blank line and a single event's data may be split
// across several data: lines, which the spec says to rejoin with a newline.
// Both rules are the point: joining across the blank line is what turned two
// perfectly good events into one unparseable string.
func sseReply(text string, id int) (string, bool) {
	for _, event := range strings.Split(normalizeNewlines(text), "\n\n") {
		var data []string
		for _, line := range strings.Split(event, "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimSpace(after))
			}
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		var framed struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal([]byte(payload), &framed) != nil {
			continue
		}
		if matchesID(framed.ID, id) {
			return payload, true
		}
	}
	return "", false
}

// matchesID compares a reply's id with the one that was sent. The comparison
// is textual because a server is free to echo the number as a JSON string, and
// an adapter that refused that would be right about the spec and useless.
func matchesID(raw json.RawMessage, id int) bool {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return text != "" && text == strconv.Itoa(id)
}

// normalizeNewlines makes the blank-line split work on a body framed with
// CRLF, which the SSE grammar allows and some proxies produce.
func normalizeNewlines(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
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
