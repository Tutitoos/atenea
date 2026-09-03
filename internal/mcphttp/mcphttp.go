// Package mcphttp is a client for one MCP server reached over streamable
// HTTP: JSON-RPC requests posted to one endpoint, answered either as a plain
// JSON document or as a stream of Server-Sent Events, a session established
// once by an initialize handshake and carried on every request after it as
// Mcp-Session-Id.
//
// This transport is shared by HTTP MCP providers. Adapters decide which tools
// to call and how to interpret their results; this package handles framing,
// authentication and session lifecycle only.
package mcphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// protocolVersion is the MCP revision this client speaks. It is stated
// rather than negotiated down: a server that cannot answer it should say so
// at the handshake, not halfway through a call.
const protocolVersion = "2025-06-18"

// handshakeID is the JSON-RPC id the initialize request carries. It is a
// constant because decode has to know which reply in an SSE stream belongs
// to it, and the handshake is the one request whose id is not drawn from
// nextID.
const handshakeID = 1

// defaultClientName is what clientInfo.name says when a caller does not name
// itself. A blank name is a legitimate thing to send, but a server operator
// staring at connection logs deserves better than an empty string.
const defaultClientName = "mcphttp"

// Options configure a Client.
type Options struct {
	// Endpoint is the absolute http(s) URL of the MCP server, e.g.
	// "http://127.0.0.1:7788/mcp". Required.
	Endpoint string
	// Headers are set on every request this client sends, initialize
	// included. This is how a caller reaches a server that requires
	// authentication -- an "Authorization: Bearer <token>" entry -- without
	// this package knowing anything about how that token was obtained or
	// what it means.
	Headers map[string]string
	// Timeout caps one Call, in addition to whatever deadline the caller's
	// own context already carries. Zero means no extra ceiling: the context
	// alone decides. A caller with its own per-call deadline, wrapping this
	// client's endpoint alongside others under one budget, should leave this
	// at zero rather than race two ceilings against each other.
	Timeout time.Duration
	// Client names this process in the handshake's clientInfo.name. Empty
	// falls back to a generic name rather than a blank one.
	Client string
}

// Client is one live MCP session against one streamable-HTTP endpoint.
// Session state cannot be shared across endpoints: a handshake against one
// server is meaningless to another.
type Client struct {
	endpoint   string
	headers    map[string]string
	clientName string
	timeout    time.Duration
	http       *http.Client

	// handshakeMu serializes the initialize exchange itself. wireMu cannot:
	// the handshake is a check, a round trip, and then a write, and holding
	// a field lock across a network call would block every concurrent
	// sibling from so much as reading nextID. Held only on the path that has
	// no session yet, so an established connection never touches it.
	handshakeMu sync.Mutex
	// wireMu guards session, nextID and version below. A caller may run many
	// calls concurrently against one Client -- Atenea's own MCP server adapter
	// fans out up to sixteen at once inside a single held commission lock --
	// and every one of them touches these fields, so they get their own,
	// finer lock rather than whatever coarser lock the caller holds.
	wireMu sync.Mutex
	// session is the MCP session id, established lazily on the first call
	// and reused. Guarded by wireMu.
	session string
	// nextID numbers the JSON-RPC requests on this session. Guarded by
	// wireMu.
	nextID int
	// version is what the server called itself when the session opened.
	// Guarded by wireMu.
	version string
}

// New validates opts and returns a Client for its endpoint.
func New(opts Options) (*Client, error) {
	endpoint := strings.TrimSpace(opts.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"mcphttp: endpoint %q must be an absolute http or https URL", opts.Endpoint)
	}
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"mcphttp: timeout must not be negative, got %s", opts.Timeout)
	}
	clientName := strings.TrimSpace(opts.Client)
	if clientName == "" {
		clientName = defaultClientName
	}
	headers := make(map[string]string, len(opts.Headers))
	for k, v := range opts.Headers {
		headers[k] = v
	}
	return &Client{
		endpoint:   endpoint,
		headers:    headers,
		clientName: clientName,
		timeout:    opts.Timeout,
		// The per-call deadline is carried on the context (plus this
		// client's own Timeout, applied in Call), so the http.Client keeps
		// no timeout of its own: two ceilings on the same call would race,
		// and the one that fired first would be the one nobody configured.
		http: &http.Client{},
	}, nil
}

// Version reports what the far side called itself during the handshake,
// cleaned by toolversion.Clean. Empty until a session has been established:
// a server that has not introduced itself yet has nothing to report, which
// is a fact rather than a guess.
func (c *Client) Version() string {
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	return c.version
}

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

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc %d: %s", e.Code, e.Message) }

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

// Call runs one tool and returns its text content.
//
// A session that has gone away is not repaired here: the call fails, and it
// is the caller's job to decide what that means -- retry with a new Client,
// mark a provider down, whatever its own failure handling calls for.
// Reconnecting in silence would hide a server that is flapping.
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	if err := c.handshake(ctx); err != nil {
		return "", err
	}
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var out toolResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("mcphttp: %s: unreadable answer: %w", tool, err)
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
		// goes back untouched for the caller's own classifier to bin.
		// Rewording it here would destroy the only evidence a human has.
		if body == "" {
			// isError with nothing in content is a server that flagged a
			// failure and then said nothing about it. An error built from
			// that would trim down to "", which is worse than no evidence:
			// it looks like the call answered instead of the call failing
			// silently. The raw frame is the only text left to show.
			return "", fmt.Errorf("mcphttp: %s reported an error with no message: %s", tool, Clip(string(raw)))
		}
		return "", fmt.Errorf("%s", strings.TrimSpace(body))
	}
	return body, nil
}

// handshake establishes the MCP session, once. It assumes no lock is held by
// the caller: Call may run many times concurrently against one Client, and
// every one of them begins by asking whether a session exists.
func (c *Client) handshake(ctx context.Context) error {
	c.wireMu.Lock()
	established := c.session != ""
	c.wireMu.Unlock()
	if established {
		return nil
	}
	// The established session is read without handshakeMu above so the
	// ordinary call pays nothing, and re-read under it below because the
	// first read decides nothing: between the two, a sibling goroutine may
	// have completed the whole exchange. Only one initialize per session
	// gets sent.
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
			"clientInfo":      map[string]any{"name": c.clientName, "version": "0"},
		},
	})
	if err != nil {
		return err
	}
	reply, err := c.post(ctx, body, "")
	if err != nil {
		return err
	}
	if reply.session == "" {
		return fmt.Errorf("mcphttp: handshake returned no session id: %s", Clip(reply.body))
	}
	result, err := decode(reply, handshakeID)
	if err != nil {
		return err
	}
	// The version rides in on the handshake the caller already pays for, so
	// filing measurements under the server that actually produced them costs
	// nothing extra. A server that does not introduce itself leaves it
	// empty, which is a fact rather than a guess.
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
	// The spec requires this notification before any tool call, and a
	// server that never receives it is entitled to refuse everything
	// afterwards.
	note, err := json.Marshal(rpcRequest{Version: "2.0", Method: "notifications/initialized"})
	if err != nil {
		return err
	}
	if _, err := c.post(ctx, note, reply.session); err != nil {
		c.wireMu.Lock()
		c.session = ""
		c.wireMu.Unlock()
		return err
	}
	return nil
}

// rpc sends one request on the established session.
func (c *Client) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.wireMu.Lock()
	c.nextID++
	id := c.nextID
	session := c.session
	c.wireMu.Unlock()
	body, err := json.Marshal(rpcRequest{Version: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	reply, err := c.post(ctx, body, session)
	if err != nil {
		// A dead session must not be reused: dropping it here is what lets
		// the next call start clean instead of failing forever.
		c.wireMu.Lock()
		c.session = ""
		c.wireMu.Unlock()
		return nil, err
	}
	return decode(reply, id)
}

// answer is one HTTP exchange, read to completion.
//
// contentType is carried because it is what decides the framing: a
// streamable HTTP server may answer one request as a JSON document and the
// next as an SSE stream, and the server says which in the header rather than
// leaving it to be guessed from the first few bytes of the body.
type answer struct {
	session     string
	contentType string
	body        string
}

// post sends one message to the endpoint and returns what came back.
//
// It returns the read body rather than the response on purpose: the body is
// read and closed here, so handing back a *http.Response would be handing
// back a reader that is already spent and a trap for whoever reads this
// next.
func (c *Client) post(ctx context.Context, body []byte, session string) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Both are advertised because a streamable-HTTP server chooses: it may
	// answer as one JSON document or as a stream of SSE frames, and refusing
	// either would make this client depend on which mood the server is in.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	// Applied after the defaults above, and to every request including the
	// handshake, so a caller reaching a server that requires authentication
	// -- Authorization: Bearer <token>, measured against kivgraph's daemon,
	// which answers 401 without it -- gets it on initialize as well as on
	// every tool call that follows, rather than needing to know which
	// requests this client happens to send.
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, err
	}
	if resp.StatusCode >= 400 {
		return answer{}, fmt.Errorf("mcp server answered %s: %s", resp.Status, Clip(string(text)))
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
// progress notifications, or replies to other requests, in the same
// response, each as its own event. Concatenating the data of every event in
// the body into one string and parsing that produces valid JSON only when
// exactly one event ever arrives.
func decode(reply answer, id int) (json.RawMessage, error) {
	payload := strings.TrimSpace(reply.body)
	if payload == "" {
		return nil, nil
	}
	if isEventStream(reply) {
		found, ok := sseReply(reply.body, id)
		if !ok {
			return nil, fmt.Errorf("mcp server sent no event carrying a reply to request %d: %s", id, Clip(reply.body))
		}
		payload = found
	}
	var out rpcResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("mcp server sent unreadable JSON: %s", Clip(reply.body))
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
// decoder, and "unreadable JSON" is a much worse thing to tell a caller than
// the answer it actually got.
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
// Events are separated by a blank line and a single event's data may be
// split across several data: lines, which the spec says to rejoin with a
// newline. Both rules are the point: joining across the blank line would
// turn two perfectly good events into one unparseable string.
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
// is textual because a server is free to echo the number as a JSON string,
// and a client that refused that would be right about the spec and useless.
func matchesID(raw json.RawMessage, id int) bool {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return text != "" && text == strconv.Itoa(id)
}

// normalizeNewlines makes the blank-line split work on a body framed with
// CRLF, which the SSE grammar allows and some proxies produce.
func normalizeNewlines(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// Clip keeps an error message readable when a server answers with a page
// instead of a sentence. Exported because a caller parsing a server's own
// answers directly -- a provider adapter decodes its own tool payload --
// wants the same truncation this package already applies to its own error
// text, rather than growing a second copy of it.
func Clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
