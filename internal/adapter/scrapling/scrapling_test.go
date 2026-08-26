package scrapling_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/scrapling"
	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// fakeServer is an MCP server over pipes, standing in for scrapling-mcp.
//
// A double rather than the real server, because installing it pulls a browser
// onto every CI leg and because a test that reaches the open web fails on the
// day somebody else's site changes. The properties worth pinning here are what
// the adapter SENDS and what it does with what comes back, and both are the
// same whether or not a real fetch happened.
//
// What the double answers is not invented: `page` below is the shape measured
// against the real scrapling-mcp, so these tests fail if that shape is ever
// decoded wrongly again -- which is how the list-shaped `content` was caught.
func fakeServer(t *testing.T, answers map[string]any) func(context.Context) (*mcpstdio.Session, error) {
	t.Helper()
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	seen := make(chan map[string]any, 8)

	go func() {
		defer func() { _ = fromServer.Close() }()
		decoder := json.NewDecoder(toServer)
		for {
			var msg map[string]any
			if err := decoder.Decode(&msg); err != nil {
				return
			}
			id, hasID := msg["id"]
			if !hasID {
				continue // a notification answers nobody
			}
			var result any
			switch msg["method"] {
			case "initialize":
				result = map[string]any{"protocolVersion": "2025-06-18",
					"serverInfo": map[string]any{"name": "Scrapling", "version": "0.4.15"}}
			case "tools/call":
				params, _ := msg["params"].(map[string]any)
				seen <- params
				name, _ := params["name"].(string)
				answer := answers[name]
				// web.extract asks the same tool once per field, so a test
				// about extraction has to answer differently per call. A
				// function answer gets the arguments and decides; everything
				// else is the fixed value it always was.
				if vary, isFunc := answer.(func(map[string]any) any); isFunc {
					args, _ := params["arguments"].(map[string]any)
					answer = vary(args)
				}
				text, isText := answer.(string)
				if !isText {
					body, _ := json.Marshal(answer)
					text = string(body)
				}
				result = map[string]any{
					"content": []any{map[string]any{"type": "text", "text": text}},
				}
			default:
				result = map[string]any{}
			}
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			_, _ = fromServer.Write(append(out, '\n'))
		}
	}()

	session := mcpstdio.New(fromClient, toClient, mcpstdio.Options{})
	t.Cleanup(func() { _ = session.Close() })
	callsSeen = seen
	return func(context.Context) (*mcpstdio.Session, error) { return session, nil }
}

// callsSeen carries what the fake was asked, so a test can assert on the
// arguments the adapter BUILT rather than on the answer it got back. That is
// the property worth pinning: a caller's payload must never reach the far
// side, because the argument that matters to a fetcher is the destination.
var callsSeen chan map[string]any

// page is the answer shape measured against scrapling-mcp on 2026-08-26:
// a status, a url, and `content` as a LIST -- the unnarrowed page followed by
// one empty string. A narrowed answer is a different shape entirely, one
// element per match, and has its own test below. Used wherever the test is
// about something other than decoding, so the ordinary path is exercised
// against the real shape rather than a convenient one.
func page(body string) map[string]any {
	return map[string]any{"status": 200, "url": "https://example.com/", "content": []any{body, ""}}
}

func fetchCapability() contract.Capability {
	return contract.Capability{
		ID: scrapling.CapabilityFetch, Version: contract.Version{Major: 1},
		Summary: "Fetch one web page and return its content.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectExternal},
		Inputs: []contract.Field{
			{Name: "url", Type: contract.TypeString, Required: true, Summary: "The page to fetch."},
			{Name: "selector", Type: contract.TypeString, Summary: "CSS selector to narrow to."},
			{Name: "format", Type: contract.TypeString, Summary: "How to render the body.",
				Enum: []string{"text", "markdown", "html"}},
		},
		Outputs: []contract.Field{{Name: "page", Type: contract.TypeRecordList, Required: true,
			Summary: "The page that was fetched.",
			Fields: []contract.Field{
				{Name: "url", Type: contract.TypeString, Required: true, Summary: "The url asked for."},
				{Name: "final_url", Type: contract.TypeString, Summary: "Where it landed."},
				{Name: "status", Type: contract.TypeInt, Summary: "The HTTP status."},
				{Name: "title", Type: contract.TypeString, Summary: "The page title."},
				{Name: "content", Type: contract.TypeString, Required: true, Summary: "The body."},
				{Name: "truncated", Type: contract.TypeBool, Summary: "Whether the body was cut."},
			}}},
	}
}

func request(t *testing.T, implementation string, payload map[string]any) contract.RunRequest {
	t.Helper()
	capability := fetchCapability()
	return contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: implementation, Capability: capability.ID},
		Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
		Payload:        payload,
		Permission:     contract.Permission{Task: "read one page", Effects: capability.Effects},
	}
}

// resolvesTo is a stand-in resolver, so the gate can be tested without asking
// the network what a name means today.
func resolvesTo(addrs ...string) func(context.Context, string) ([]netip.Addr, error) {
	return func(context.Context, string) ([]netip.Addr, error) {
		out := make([]netip.Addr, 0, len(addrs))
		for _, raw := range addrs {
			out = append(out, netip.MustParseAddr(raw))
		}
		return out, nil
	}
}

func newRunner(t *testing.T, opts scrapling.Options) *scrapling.Runner {
	t.Helper()
	if opts.Resolve == nil {
		opts.Resolve = resolvesTo("93.184.216.34")
	}
	if opts.Denied == nil {
		opts.Denied = []string{"127.0.0.0/8", "10.0.0.0/8", "169.254.0.0/16", "*.lan", "localhost"}
	}
	runner, err := scrapling.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

func TestAPageIsReadBackFromTheServer(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": page("hello from the open web"),
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows, ok := out.Result["page"].([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("result = %+v", out.Result)
	}
	if rows[0]["content"] != "hello from the open web" || rows[0]["status"] != 200 {
		t.Errorf("page = %+v", rows[0])
	}
	if out.Verdict != contract.VerdictOK {
		t.Errorf("verdict = %v, want ok", out.Verdict)
	}
	// Free work says so, rather than staying silent and being read as
	// "nobody said". The two are different facts.
	if out.SpentUSD != 0 || !out.SpentUSDKnown {
		t.Errorf("spent = %v known = %v, want a declared zero", out.SpentUSD, out.SpentUSDKnown)
	}
	// The answer has to satisfy the schema it was declared under, or the
	// adapter is handing the core something the catalog does not describe.
	if err := fetchCapability().ValidateOutput(out.Result); err != nil {
		t.Errorf("ValidateOutput: %v", err)
	}
	// Filed under the version the far side gave on the handshake, not one
	// written down here. An earlier comment in this package named a version
	// read out of documentation, and it was wrong by two minor releases;
	// measurements filed under a guess average an upgrade into the numbers
	// that came before it.
	if out.ToolVersion != "0.4.15" {
		t.Errorf("ToolVersion = %q, want what the server called itself", out.ToolVersion)
	}
}

// The whole reason this is an adapter and not a passthrough: the far side is
// asked for exactly the arguments this package built, under the names it
// chose, and nothing else travels.
func TestOnlyBuiltArgumentsReachTheServer(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": page("body"),
	})})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest, map[string]any{
		"url": "https://example.com/", "selector": "main article",
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	params := <-callsSeen
	args, _ := params["arguments"].(map[string]any)
	if args["url"] != "https://example.com/" {
		t.Errorf("url = %v", args["url"])
	}
	// Renamed on the way out, which is only possible because it was read and
	// rebuilt rather than forwarded.
	if args["css_selector"] != "main article" {
		t.Errorf("css_selector = %v, want the selector translated", args["css_selector"])
	}
	// The rendering is chosen on the way in, so it is always sent.
	if args["extraction_type"] != "text" {
		t.Errorf("extraction_type = %v, want the declared default", args["extraction_type"])
	}
	// The far side takes twenty-odd arguments on make_request -- proxies,
	// auth, headers, TLS verification. Exactly three are reachable from here,
	// and that is the property this number is guarding.
	if len(args) != 3 {
		t.Errorf("args = %+v, want exactly the three this adapter builds", args)
	}
}

// A field the capability never declared is refused before anything is dialed.
// The gate that matters most is the destination, but a payload that can grow
// fields is a payload that can eventually grow one the far side understands.
func TestAnUndeclaredFieldNeverReachesTheGate(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": page("body"),
	})})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest, map[string]any{
		"url": "https://example.com/", "headers": "X-Secret: 1",
	}))
	if err == nil {
		t.Fatal("an undeclared field was accepted")
	}
	if !strings.Contains(err.Error(), "headers") {
		t.Errorf("error = %v, want it to name the field", err)
	}
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

// The decisive one. A hostname is somebody else's claim about where it points,
// so a gate that judges the string refuses nothing at all: localtest.me is a
// real public name with an A record to 127.0.0.1, and anybody may publish
// another.
func TestAPublicNameResolvingToLoopbackIsRefused(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": page("body")}),
		Resolve: resolvesTo("127.0.0.1"),
	})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://localtest.me/"}))
	if err == nil {
		t.Fatal("a name resolving to loopback was allowed through")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailurePermissionDenied {
		t.Fatalf("error = %v, want permission_denied", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error = %v, want it to name the address it resolved to", err)
	}
}

// A name with one public and one private record must not pass on resolver
// ordering, which is not a decision anybody made.
func TestEveryResolvedAddressIsJudged(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": page("body")}),
		Resolve: resolvesTo("93.184.216.34", "10.1.2.3"),
	})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://split-horizon.example/"}))
	if err == nil {
		t.Fatal("a name with a private record was allowed through")
	}
	if !strings.Contains(err.Error(), "10.1.2.3") {
		t.Errorf("error = %v, want it to name the refused address", err)
	}
}

func TestTheGateRefusesWhatItShould(t *testing.T) {
	cases := []struct {
		name, url, want string
	}{
		{"a scheme that is not the web", "file:///etc/passwd", "not http or https"},
		{"another one", "ftp://example.com/x", "not http or https"},
		{"no url at all", "", "no url"},
		{"a host-shaped denial", "http://portainer.kena.lan/", "denied entry"},
		{"a bare denied name", "http://localhost:8080/", "denied entry"},
		{"a literal private address", "http://10.1.2.3/", "denied"},
		{"the metadata endpoint", "http://169.254.169.254/latest/meta-data/", "denied"},
		{"a url with no host", "http:///nowhere", "names no host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newRunner(t, scrapling.Options{
				Session: fakeServer(t, map[string]any{"make_request": page("body")}),
			})
			_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
				map[string]any{"url": tc.url}))
			if err == nil {
				t.Fatalf("%s was allowed through", tc.url)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A literal address is judged as written. Going through a resolver would let
// the resolver's opinion overrule what the caller plainly typed.
func TestALiteralAddressNeedsNoResolver(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": page("body")}),
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			t.Error("the resolver was asked about a literal address")
			return nil, errors.New("unreachable")
		},
	})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "http://127.0.0.1:40010/mcp"}))
	if err == nil {
		t.Fatal("loopback was allowed through")
	}
}

// Empty means any public host, which is the whole difference from [desktop]
// applications and the thing most likely to be "fixed" by somebody who read
// the other list first.
func TestAnEmptyDomainListReachesThePublicWeb(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": page("body")}),
		Domains: nil,
	})
	if _, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"})); err != nil {
		t.Fatalf("a public host was refused by an empty allow-list: %v", err)
	}
}

func TestANonEmptyDomainListNarrows(t *testing.T) {
	session := fakeServer(t, map[string]any{"make_request": page("body")})
	runner := newRunner(t, scrapling.Options{Session: session, Domains: []string{"example.com"}})
	// A bare domain covers its subdomains, which is what somebody writing a
	// domain into an allow-list means by it.
	if _, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://api.example.com/v1"})); err != nil {
		t.Fatalf("a subdomain of an allowed domain was refused: %v", err)
	}
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://github.com/"}))
	if err == nil {
		t.Fatal("a host outside the allow-list was reached")
	}
	if !strings.Contains(err.Error(), "[web] domains") {
		t.Errorf("error = %v, want it to name the list that refused", err)
	}
}

// An explicitly empty denied list is a statement and is honored. It is the one
// configuration in which this adapter is the thing its own package comment
// warns about, so it is pinned rather than left to chance.
func TestAnEmptyDeniedListIsHonored(t *testing.T) {
	runner, err := scrapling.New(scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": page("body")}),
		Denied:  []string{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "http://127.0.0.1:40010/mcp"})); err != nil {
		t.Fatalf("an explicitly empty denied list was not honored: %v", err)
	}
	if !strings.Contains(runner.Surface(), "no denied ranges") {
		t.Errorf("surface = %q, want it to say the gate is open", runner.Surface())
	}
}

// A denial rule nobody can parse is a denial that silently does not apply, and
// the settings file would keep saying it did.
func TestAMalformedDenialIsRefusedAtTheDoor(t *testing.T) {
	_, err := scrapling.New(scrapling.Options{
		Session: fakeServer(t, nil),
		Denied:  []string{"10.0.0.0/99"},
	})
	if err == nil {
		t.Fatal("a malformed CIDR was accepted")
	}
	if !strings.Contains(err.Error(), "not a CIDR") {
		t.Errorf("error = %v, want it to say what was wrong", err)
	}
}

func TestAHostThatDoesNotResolveIsNotFound(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": page("body")}),
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("no such host")
		},
	})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://nowhere.example/"}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailureNotFound {
		t.Fatalf("error = %v, want not_found", err)
	}
}

// The half of the redirect problem that can be closed from here.
func TestARedirectOntoARefusedHostFailsTheAnswer(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{"make_request": map[string]any{
			"status": 200, "url": "http://10.1.2.3/admin", "text": "internal",
		}}),
	})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	if err == nil {
		t.Fatal("an answer from a refused address was handed back")
	}
	if !strings.Contains(err.Error(), "redirected to") {
		t.Errorf("error = %v, want it to say where it ended up", err)
	}
}

// ---------------------------------------------------------------------------
// Escalation
// ---------------------------------------------------------------------------

// The property the three-implementation design rests on. An interstitial
// arrives as a successful 200; reported as an answer, the funnel would learn
// that the cheapest level works every time.
func TestAChallengePageIsUnavailableSoTheFunnelEscalates(t *testing.T) {
	bodies := []string{
		"<html><title>Just a moment...</title><body>Checking your browser</body></html>",
		"<div id=\"cf-browser-verification\">please wait</div>",
		"Enable JavaScript and cookies to continue",
		"Verifying you are human",
	}
	for _, body := range bodies {
		t.Run(body[:min(len(body), 24)], func(t *testing.T) {
			runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
				"make_request": map[string]any{"status": 200, "text": body},
			})})
			_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
				map[string]any{"url": "https://example.com/"}))
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
				t.Fatalf("error = %v, want unavailable so the funnel falls back", err)
			}
		})
	}
}

func TestAForbiddenStatusEscalates(t *testing.T) {
	for _, status := range []int{403, 429} {
		runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
			"fetch": map[string]any{"status": status, "text": "no"},
		})})
		_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationFetch,
			map[string]any{"url": "https://example.com/"}))
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
			t.Fatalf("status %d: error = %v, want unavailable", status, err)
		}
	}
}

// Stealth is the last level there is, so a block reported from it would ask
// the funnel to fall back to nobody and lose the reason on the way.
func TestStealthReportsABlockAsTheAnswer(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"stealthy_fetch": map[string]any{"status": 403, "text": "Just a moment..."},
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationStealth,
		map[string]any{"url": "https://example.com/"}))
	if err != nil {
		t.Fatalf("the last level escalated instead of answering: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	if rows[0]["status"] != 403 {
		t.Errorf("page = %+v, want the refusal reported as what it is", rows[0])
	}
}

// A 404 is an answer about the page. Escalating it would spend a browser to be
// told the same thing more slowly.
func TestAnOrdinaryMissIsNotEscalated(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{"status": 404, "text": "Not Found"},
	})})
	if _, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/missing"})); err != nil {
		t.Fatalf("a 404 was escalated: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

func TestTheBodyIsFoundUnderWhicheverNameItArrivedIn(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"text", "text", "from text"},
		{"content", "content", "from content"},
		{"markdown", "markdown", "from markdown"},
		{"html", "html", "from html"},
		{"body", "body", "from body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
				"make_request": map[string]any{"status": 200, tc.key: tc.want},
			})})
			out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
				map[string]any{"url": "https://example.com/"}))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			rows := out.Result["page"].([]map[string]any)
			if rows[0]["content"] != tc.want {
				t.Errorf("content = %v, want %q", rows[0]["content"], tc.want)
			}
		})
	}
}

// A format the far side did not produce is not worth refusing a fetch over:
// the page was retrieved and the bytes exist.
func TestAMissingFormatFallsBackRatherThanFailing(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{"status": 200, "text": "plain only"},
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/", "format": "markdown"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	if rows[0]["content"] != "plain only" {
		t.Errorf("content = %v, want the rendering that does exist", rows[0]["content"])
	}
}

// A narrowed answer is one element PER MATCH, not two halves. Measured on
// Hacker News: `.titleline` came back as thirty strings and a trailing empty
// one, with the page itself absent. Every match has to survive -- an earlier
// version of this decoder described the list as [page, match], which would
// have thrown away twenty-eight rows the moment it indexed on that belief.
func TestEveryMatchOfANarrowedAnswerSurvives(t *testing.T) {
	matches := make([]any, 0, 31)
	for i := range 30 {
		matches = append(matches, fmt.Sprintf("headline %d", i))
	}
	matches = append(matches, "") // the trailing empty the far side always sends
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{"status": 200, "url": "https://news.ycombinator.com/",
			"content": matches},
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://news.ycombinator.com/", "selector": ".titleline"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	body, _ := rows[0]["content"].(string)
	for i := range 30 {
		if !strings.Contains(body, fmt.Sprintf("headline %d", i)) {
			t.Fatalf("match %d is missing from the body", i)
		}
	}
}

// A list whose second half is empty -- the ordinary case, no selector given --
// must not arrive with the separator dangling on the end.
func TestAnEmptyHalfIsNotJoinedIn(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{"status": 200, "content": []any{"just the page", ""}},
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	if rows[0]["content"] != "just the page" {
		t.Errorf("content = %q, want no trailing separator", rows[0]["content"])
	}
}

func TestTheRequestedFormatIsPreferredWhenItExists(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{
			"status": 200, "text": "plain", "markdown": "# heading", "html": "<h1>heading</h1>",
		},
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/", "format": "html"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	if rows[0]["content"] != "<h1>heading</h1>" {
		t.Errorf("content = %v, want the html", rows[0]["content"])
	}
}

// An answer carrying no body under any name is a failure that quotes what
// arrived, never an empty page reported as a successful fetch.
func TestAnAnswerWithNoBodyIsAFailure(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{"status": 200, "title": "nothing here"},
	})})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("error = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "no page body") {
		t.Errorf("error = %v, want it to say what was missing", err)
	}
}

// A server that hands back the page itself is giving us the only field that is
// strictly required. Treating that as the body is a reading, so nothing else
// is claimed alongside it.
func TestABarePageIsTakenAsTheBodyAndNothingElse(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": "<html>just the page</html>",
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	if rows[0]["content"] != "<html>just the page</html>" {
		t.Errorf("content = %v", rows[0]["content"])
	}
	if rows[0]["status"] != 0 || rows[0]["title"] != "" {
		t.Errorf("page = %+v, want nothing claimed that was not said", rows[0])
	}
}

func TestStatusIsReadUnderEitherSpelling(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": map[string]any{"status_code": 201, "text": "made"},
	})})
	out, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["page"].([]map[string]any)
	if rows[0]["status"] != 201 {
		t.Errorf("status = %v, want 201", rows[0]["status"])
	}
}

func TestAnEmptyAnswerIsUnavailable(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": "",
	})})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

// ---------------------------------------------------------------------------
// Shape
// ---------------------------------------------------------------------------

func TestTheRunnerDescribesItself(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, nil)})
	if runner.ID() != "scrapling" {
		t.Errorf("ID = %q", runner.ID())
	}
	// Implementations is what a settings file told this runner to answer for;
	// Capabilities is what its code can dispatch. The wiring above compares
	// them, so a difference here is caught before anything runs.
	if got := runner.Capabilities(); !slices.Equal(got,
		[]string{scrapling.CapabilityExtract, scrapling.CapabilityFetch}) {
		t.Errorf("Capabilities = %v", got)
	}
	want := []string{
		scrapling.ImplementationExtractFetch, scrapling.ImplementationExtractRequest,
		scrapling.ImplementationExtractStealth, scrapling.ImplementationFetch,
		scrapling.ImplementationRequest, scrapling.ImplementationStealth,
	}
	if got := runner.Implementations(); !slices.Equal(got, want) {
		t.Errorf("Implementations = %v, want %v", got, want)
	}
	for _, id := range want {
		if !runner.Serves(id) {
			t.Errorf("Serves(%q) = false", id)
		}
	}
	if runner.Serves("macos.click") {
		t.Error("it claims somebody else's implementation")
	}
}

// The declared list and the compiled fallback are compared by a settings test;
// this pins the order the fallback answers in, so a difference there reads as
// a difference in content rather than in sorting.
func TestDefaultImplementationsAreSorted(t *testing.T) {
	got := scrapling.DefaultImplementations()
	if !slices.IsSorted(got) {
		t.Errorf("DefaultImplementations = %v, want sorted", got)
	}
}

func TestSurfaceSaysHowWideTheReachIs(t *testing.T) {
	open := newRunner(t, scrapling.Options{Session: fakeServer(t, nil)})
	if !strings.Contains(open.Surface(), "any-public-host") {
		t.Errorf("surface = %q", open.Surface())
	}
	narrowed := newRunner(t, scrapling.Options{
		Session: fakeServer(t, nil), Domains: []string{"example.com", "docs.rs"},
	})
	if !strings.Contains(narrowed.Surface(), "2 allowed host(s)") {
		t.Errorf("surface = %q", narrowed.Surface())
	}
}

func TestNewRefusesWhatItCannotHonor(t *testing.T) {
	if _, err := scrapling.New(scrapling.Options{}); err == nil {
		t.Error("a runner with no session was built")
	}
	_, err := scrapling.New(scrapling.Options{
		Session:         fakeServer(t, nil),
		Implementations: []string{"scrapling.telepathy"},
	})
	if err == nil {
		t.Fatal("an implementation nothing here answers was accepted")
	}
	if !strings.Contains(err.Error(), "scrapling.telepathy") {
		t.Errorf("error = %v, want it to name the implementation", err)
	}
}

func TestAnUnknownImplementationIsRefusedAtDispatch(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, nil)})
	req := request(t, scrapling.ImplementationRequest, map[string]any{"url": "https://example.com/"})
	req.Implementation.ID = "scrapling.telepathy"
	_, err := runner.Run(t.Context(), req)
	if err == nil {
		t.Fatal("an unknown implementation was dispatched")
	}
}

// The remedy belongs in the message, because the ordinary cause is that
// Scrapling is not installed and the operating system's own words for that are
// "no such file or directory".
func TestAnUnreachableServerSaysHowToInstallIt(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: func(context.Context) (*mcpstdio.Session, error) {
			return nil, errors.New("no such file or directory")
		},
	})
	_, err := runner.Run(t.Context(), request(t, scrapling.ImplementationRequest,
		map[string]any{"url": "https://example.com/"}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("error = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "pip install") {
		t.Errorf("error = %v, want the remedy in the message", err)
	}
}
