// Package scrapling answers the `web.*` capabilities by reaching the open web
// through a Scrapling MCP server.
//
// # Why an adapter and not a passthrough
//
// Scrapling ships its own MCP server, so re-offering its tools verbatim under
// `raw.scrapling.*` would have cost one settings block and no Go at all. It is
// deliberately not what happens here, for the same reason internal/adapter/desktop
// gives: a passthrough forwards the caller's arguments unexamined --
// internal/passthrough filters on the tool NAME and nothing else -- and the
// argument that matters to a fetcher is the destination. `raw.scrapling.make_request`
// is an unrestricted HTTP client handed to every connected client, and the
// interesting addresses on a developer's machine are not on the open web: they
// are the MCP servers on loopback, the admin services on the LAN, and the
// cloud metadata endpoint at 169.254.169.254.
//
// So every request this package sends is BUILT here from the capability's
// typed inputs, and the URL crosses mayReach before anything is dialed. That
// sentence is the whole gate, and it only holds while nothing in this file
// passes a caller's map straight through.
//
// # The gate is about addresses, not names
//
// mayReach resolves the host and judges the IP, never the string it was
// given. A hostname is somebody else's claim about where it points: `denied`
// full of loopback ranges refuses nothing at all if the check runs against
// the text, because `localtest.me` is a public name with an A record to
// 127.0.0.1 and there are as many of those as anybody cares to register.
//
// The one hole left is the redirect, and it is named rather than papered
// over. The far side follows redirects inside its own process, so a URL that
// passes the gate can still end somewhere that would not have. What this
// package can do it does: the destination the server reports having actually
// landed on is put back through the same gate before the answer is handed
// over, and an answer that came from an address the gate refuses is a
// failure rather than a result. What it cannot do is stop the request from
// having been made. Closing it properly means the far side not following
// redirects at all, and make_request DOES expose that -- see the note below on
// what was measured, and docs/content/not-built-yet.md for why it is still
// open.
//
// # Three levels, and why a block has to be a failure
//
// Scrapling answers the same question three ways at very different prices:
// a plain HTTP request with browser impersonation, a real browser render, and
// a stealth render that defeats anti-bot interstitials. They are declared as
// three implementations of one capability rather than one implementation with
// a `mode` input, because ranking equivalent work by measured cost is the
// entire job of the funnel and a `mode` input would move that decision to
// whoever wrote the prompt.
//
// That only works if a cheap attempt that did not really answer says so. An
// anti-bot interstitial arrives as a perfectly successful HTTP 200 carrying a
// page about how much the site cares about security. Reported as VerdictOK,
// the funnel would learn that the cheapest level answers every time, rank it
// first forever, and hand back challenge pages as content. blocked() is what
// keeps that from happening, and it is the same shape of guard as
// checkGraphReady in internal/adapter/kivgraph -- there an empty graph
// answers successfully with zeros, here a blocked fetch answers successfully
// with an interstitial. Both are a response that looks like an answer and is
// not one.
//
// How the escalation actually reaches the next level is worth stating exactly,
// because it is not what the phrase suggests. contract.FailureUnavailable does
// not retry anything inside this call: there is one dispatch per commission,
// the failure marks this implementation unhealthy, and it is the NEXT call
// whose funnel drops it at the health stage and reaches for the level above.
// Measured from a clean state: three calls for the first page behind
// Cloudflare, then straight to stealth while the health record stands.
//
// And that record is per implementation, not per host -- so one protected site
// keeps the cheap level skipped for every site after it. That is a real cost,
// it is not fixable from inside this package, and it is written up under "One
// blocked site downgrades every site" in docs/content/not-built-yet.md rather
// than left for somebody to rediscover from a browser bill.
//
// # What the far side actually said
//
// Measured against `scrapling-mcp` 0.3.9 on 2026-08-26, over stdio. Three
// things were confirmed and one was a surprise:
//
//   - `css_selector` narrows on all three tools, and `extraction_type` is a
//     text|html|markdown enum that decides the rendering ON THE WAY IN. So
//     only one rendering ever comes back, and the capability's `format` is
//     sent rather than applied to the answer.
//   - the answer's `content` is a LIST of strings, not a string, and what it
//     holds depends on the selector: without one it is the whole page and a
//     trailing empty string; with one it is ONE ELEMENT PER MATCH and a
//     trailing empty string, with the page absent entirely. Measured on Hacker
//     News: no selector gave [3881 chars, ""], `.titleline` gave thirty short
//     strings and an empty one. That second shape is the whole reason
//     web.extract needs no CSS engine of its own -- the list IS the result set.
//   - there is no `title` in the answer at all. The declared output keeps the
//     field optional and this adapter leaves it empty, because parsing one out
//     of the body would be this package inventing a fact.
//   - make_request takes `follow_redirects` (false, or safe|all|obeycode|
//     firstonly, default safe) and `max_redirects`. That means the redirect
//     hole above IS closable at this level -- see the note in
//     docs/content/not-built-yet.md for why it is not closed yet and what
//     closing it costs.
package scrapling

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// DefaultTimeout bounds one call to the far side.
//
// Thirty seconds rather than the ten desktop uses, and the difference is the
// browser. A plain request is milliseconds; a stealth render starts Chromium,
// waits out a challenge and can legitimately take the better part of a
// minute on a slow site. A ceiling under that would turn the level that
// exists to succeed on hard pages into the level that always times out.
const DefaultTimeout = 30 * time.Second

// Capability and implementation ids this adapter answers.
//
// Two capabilities, three prices each. The capability says WHAT -- one page,
// or named fields out of one page -- and the implementations say HOW MUCH,
// which is why they are named for the machinery rather than for the question.
// Six implementations rather than three with a flag, because an implementation
// answers exactly one capability and contract.RunRequest.Validate refuses any
// other reading. One word per dotted segment, matching every capability
// already shipped.
const (
	CapabilityFetch   = "web.fetch"
	CapabilityExtract = "web.extract"

	ImplementationRequest = "scrapling.request"
	ImplementationFetch   = "scrapling.fetch"
	ImplementationStealth = "scrapling.stealth"

	// The extract half. Three more implementations rather than an input on
	// the three above, because they are the same three prices paid for a
	// different promise, and an implementation answers exactly one
	// capability -- contract.RunRequest.Validate refuses any other reading.
	ImplementationExtractRequest = "scrapling.extract_request"
	ImplementationExtractFetch   = "scrapling.extract_fetch"
	ImplementationExtractStealth = "scrapling.extract_stealth"
)

// levels is the whole dispatch table: which far-side tool each implementation
// calls, and what it answers.
//
// Nothing outside this table reaches the far side. A caller names a
// capability and the funnel names an implementation; neither of them ever
// names a tool, which is what keeps the set of things this adapter can do
// finite and readable in one screen.
var levels = map[string]struct {
	capability string
	tool       string
	// escalates says whether a block at this level is worth reporting as
	// unavailable. The stealth level is the last one there is, so a block
	// there is the real answer -- reporting it as unavailable would ask the
	// funnel to fall back to nobody and lose the reason on the way.
	escalates bool
}{
	ImplementationRequest: {CapabilityFetch, "make_request", true},
	ImplementationFetch:   {CapabilityFetch, "fetch", true},
	ImplementationStealth: {CapabilityFetch, "stealthy_fetch", false},

	ImplementationExtractRequest: {CapabilityExtract, "make_request", true},
	ImplementationExtractFetch:   {CapabilityExtract, "fetch", true},
	ImplementationExtractStealth: {CapabilityExtract, "stealthy_fetch", false},
}

// DefaultImplementations is what this adapter answers for when a settings file
// does not narrow it.
func DefaultImplementations() []string {
	out := make([]string, 0, len(levels))
	for id := range levels {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Options configures the adapter.
type Options struct {
	// Implementations narrows what this runner claims. Empty takes
	// DefaultImplementations.
	Implementations []string
	// Timeout bounds one call. Zero takes DefaultTimeout.
	Timeout time.Duration
	// Session hands over a live session with the server. A function rather
	// than a value because the process behind it belongs to the supervisor
	// and may not have been started when this adapter was built.
	Session func(ctx context.Context) (*mcpstdio.Session, error)
	// Domains narrows what may be reached, by host. EMPTY MEANS ANY PUBLIC
	// HOST, which is the opposite of [desktop] applications and is a
	// considered difference rather than an oversight: every window on a
	// machine can be a credential, and an arbitrary public web page cannot.
	// What can is the private side of this network, and that is Denied's job.
	Domains []string
	// Denied always wins over Domains, and is seeded rather than empty. It
	// takes CIDR blocks and host patterns in one list -- "10.0.0.0/8" and
	// "*.lan" are both entries -- because the thing being refused is one
	// idea, "the inside of this network", that is spelled two ways.
	//
	// An explicitly empty Denied is honored. Somebody who writes it has asked
	// for an unrestricted HTTP client and said so out loud, which is the only
	// form of that request this package will answer.
	Denied []string
	// Resolve turns a host into addresses. Injected so the gate can be tested
	// without a resolver, and so a caller that already knows better than the
	// system resolver can say so.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Runner is the far side of the web capabilities.
type Runner struct {
	implementations []string
	timeout         time.Duration
	session         func(ctx context.Context) (*mcpstdio.Session, error)
	resolve         func(ctx context.Context, host string) ([]netip.Addr, error)
	domains         []string
	deniedNets      []netip.Prefix
	deniedHosts     []string
}

// New prepares the adapter. Nothing is dialed here: the server is started by
// the supervisor on the first call that needs it.
func New(opts Options) (*Runner, error) {
	if opts.Session == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"scrapling: no session to the server was supplied")
	}
	implementations := slices.Clone(opts.Implementations)
	if len(implementations) == 0 {
		implementations = DefaultImplementations()
	}
	known := DefaultImplementations()
	for _, id := range implementations {
		if !slices.Contains(known, id) {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"scrapling: nothing here answers implementation %q", id)
		}
	}
	nets, hosts, err := splitDenied(opts.Denied)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	resolve := opts.Resolve
	if resolve == nil {
		resolve = systemResolve
	}
	return &Runner{
		implementations: implementations,
		timeout:         timeout,
		session:         opts.Session,
		resolve:         resolve,
		domains:         normalizeHosts(opts.Domains),
		deniedNets:      nets,
		deniedHosts:     hosts,
	}, nil
}

// splitDenied sorts the one declared list into the two kinds of rule it
// actually holds, once, at startup.
//
// A malformed entry is refused here rather than skipped at call time, and the
// distinction matters more than it looks: a denial rule nobody can parse is a
// denial that silently does not apply, and the settings file would keep
// saying it did. The same reading every other list in this tree gets -- a
// settings file that says something impossible is stopped at the door.
func splitDenied(entries []string) ([]netip.Prefix, []string, error) {
	nets := make([]netip.Prefix, 0, len(entries))
	hosts := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, nil, contract.Fail(contract.FailureInvalidInput,
					"scrapling: [web] denied entry %q is not a CIDR block: %v", entry, err)
			}
			nets = append(nets, prefix)
			continue
		}
		// A bare address is a /32 or /128 spelled the short way, which is
		// how somebody will write it the first time.
		if addr, err := netip.ParseAddr(entry); err == nil {
			nets = append(nets, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		hosts = append(hosts, strings.ToLower(entry))
	}
	return nets, hosts, nil
}

func normalizeHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func systemResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	found, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(found))
	for _, addr := range found {
		out = append(out, addr.Unmap())
	}
	return out, nil
}

// ID names the runner.
func (r *Runner) ID() string { return "scrapling" }

// Surface says how wide this adapter's reach currently is, which is the one
// fact about it worth a line on the status screen.
//
// It reports the posture rather than a count of rules, because "narrowed to 3
// domains" is a sentence somebody can check against what they meant and
// "3 allow, 8 deny" is a sentence they have to go read the file to
// understand.
func (r *Runner) Surface() string {
	reach := "any-public-host"
	if len(r.domains) > 0 {
		reach = fmt.Sprintf("%d allowed host(s)", len(r.domains))
	}
	if len(r.deniedNets) == 0 && len(r.deniedHosts) == 0 {
		// Worth saying out loud on the status screen, because it is the one
		// configuration in which this adapter is the thing its own package
		// comment warns about.
		return reach + " (no denied ranges: private addresses are reachable)"
	}
	return reach
}

// Serves reports whether this runner answers that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Implementations is what this runner declares itself the far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Capabilities is what its code can actually dispatch, which the wiring above
// checks against Implementations before anything runs.
func (r *Runner) Capabilities() []string {
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		if !slices.Contains(out, level.capability) {
			out = append(out, level.capability)
		}
	}
	slices.Sort(out)
	return out
}

// Run executes one step.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	level, ok := levels[req.Implementation.ID]
	if !ok {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"scrapling: nothing here answers implementation %q", req.Implementation.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if level.capability == CapabilityExtract {
		return r.extract(ctx, req, level.tool, level.escalates)
	}
	return r.fetch(ctx, req, level.tool, level.escalates)
}

// fetch answers web.fetch at whichever level the funnel picked.
func (r *Runner) fetch(ctx context.Context, req contract.RunRequest, tool string, escalates bool) (contract.Outcome, error) {
	started := time.Now()

	target, _ := req.Payload["url"].(string)
	if err := r.mayReach(ctx, target); err != nil {
		return contract.Outcome{}, err
	}

	// Built field by field. The caller's payload is read here and never
	// forwarded: an argument the far side understands and this adapter does
	// not is an argument nobody authorized. Measured 2026-08-26, that far side
	// takes twenty-odd arguments on make_request alone -- proxies, auth,
	// headers, TLS verification, browser executables. Three of them are
	// reachable from here and the rest are unreachable BY CONSTRUCTION rather
	// than by an allow-list somebody has to remember to update.
	args := map[string]any{
		"url": target,
		// Chosen on the way in: the far side renders once, in this format,
		// and only this one comes back. Its own enum is text|html|markdown,
		// the same three the capability declares, so the value crosses
		// unchanged -- and it is always sent, because the far side's default
		// is markdown while the capability's is text.
		"extraction_type": format(req.Payload),
	}
	if selector, ok := req.Payload["selector"].(string); ok && selector != "" {
		// Narrowing before the answer leaves the server, which is where it is
		// cheapest -- the alternative is carrying a whole page across the
		// transport to throw most of it away here.
		args["css_selector"] = selector
	}

	text, err := r.call(ctx, tool, args)
	if err != nil {
		return contract.Outcome{}, err
	}
	answer, err := answerOf(tool, text)
	if err != nil {
		return contract.Outcome{}, err
	}
	// web.fetch promises a page, so an empty one is a provider that did not
	// answer. web.extract makes a different promise and does not apply this.
	if err := requireBody(tool, text, answer); err != nil {
		return contract.Outcome{}, err
	}

	// The destination check, again, against where the server says it landed.
	// See the package comment: this is the half of the redirect problem that
	// can be closed from here.
	if answer.finalURL != "" && !sameTarget(answer.finalURL, target) {
		if err := r.mayReach(ctx, answer.finalURL); err != nil {
			return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
				"scrapling: %s redirected to %s, which the gate refuses: %v",
				target, answer.finalURL, err)
		}
	}

	if escalates && blocked(answer.status, answer.content) {
		// Unavailable and not a verdict, because this is the sentence the
		// funnel reads to try the next level up. See the package comment.
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"scrapling: %s answered %s with an anti-bot challenge rather than the page "+
				"(status %d) -- a heavier implementation may get through",
			tool, target, answer.status)
	}

	return contract.Outcome{
		Result: map[string]any{"page": []map[string]any{{
			"url":       target,
			"final_url": answer.finalURL,
			"status":    answer.status,
			"title":     answer.title,
			"content":   answer.pick(format(req.Payload)),
			"truncated": answer.truncated,
		}}},
		Verdict: contract.VerdictOK,
		// Duration only. No tokens and no memory: the far side is a process
		// the supervisor owns rather than one this call spawned, and an HTTP
		// request is not a model turn. Inventing either figure would poison
		// the baseline the selector ranks on -- which for this capability is
		// the whole point, since three implementations are competing on it.
		Spent: contract.Sample{Duration: time.Since(started)},
		// Zero WITH Known, which are two different statements: nothing here
		// charges anything, and saying so is not the same as staying silent.
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}

// extract answers web.extract: named selectors in, one row per match out.
//
// # Why the output is long and not wide
//
// The obvious shape is one record per result with a column per field, and it
// is not declarable here. Output fields are declared statically in the catalog
// -- name, type, required -- and a shape that depends on the selectors a
// caller happens to pass cannot be named in advance. So the rows are
// (field, index, value) and the caller pivots. That is a real cost paid to
// keep the capability honest: a wide shape would have to be declared as an
// untyped bag, which is a promise that says nothing.
//
// # Why one call per field
//
// The far side's css_selector takes ONE selector, so N named fields are N
// calls -- measured warm at about 0.75s each. Fetching once and applying the
// selectors here was the alternative and was refused: it needs a CSS engine in
// Go, which means `.foo` could match differently in web.extract than it does
// in web.fetch, and a selector that means two things in one system is worse
// than a capability that costs more.
//
// Two consequences the caller should know, and the capability's semantics say
// so out loud: the origin sees N requests for one page, and the fields are
// read up to N*0.75s apart, so a page that changes underneath produces a
// record that was never true all at once.
func (r *Runner) extract(ctx context.Context, req contract.RunRequest, tool string, escalates bool) (contract.Outcome, error) {
	started := time.Now()

	target, _ := req.Payload["url"].(string)
	// Gated once rather than per field: it is the same URL every time, and a
	// second resolution would be a second answer to a question already asked
	// -- one that could differ from the first and leave half the fields read
	// under a verdict the other half never got.
	if err := r.mayReach(ctx, target); err != nil {
		return contract.Outcome{}, err
	}
	fields, err := fieldsOf(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}

	rendering := format(req.Payload)
	rows := make([]map[string]any, 0, len(fields))
	for _, field := range fields {
		text, err := r.call(ctx, tool, map[string]any{
			"url":             target,
			"css_selector":    field.selector,
			"extraction_type": rendering,
		})
		if err != nil {
			return contract.Outcome{}, err
		}
		answer, err := answerOf(tool, text)
		if err != nil {
			return contract.Outcome{}, err
		}
		if escalates && blocked(answer.status, answer.content) {
			// Stop at the first blocked field rather than finishing the
			// others. Every remaining call would be blocked the same way, and
			// a partial record built out of a challenge page is worse than no
			// record: it would be handed back as a successful extraction.
			return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
				"scrapling: %s answered %s with an anti-bot challenge rather than the page "+
					"(status %d, reading %q) -- a heavier implementation may get through",
				tool, target, answer.status, field.name)
		}
		// One row per match. A field that matched nothing contributes no rows,
		// which is the honest answer and is distinguishable from a field that
		// was never asked for only by the caller, who knows what they sent.
		for i, value := range answer.parts {
			rows = append(rows, map[string]any{
				"field": field.name,
				"index": i,
				"value": value,
			})
		}
	}

	return contract.Outcome{
		Result:  map[string]any{"rows": rows},
		Verdict: contract.VerdictOK,
		// Duration only, as with fetch: the far side is the supervisor's
		// process and an HTTP request is not a model turn.
		Spent:         contract.Sample{Duration: time.Since(started)},
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}

// field is one named selector the caller asked for.
type field struct{ name, selector string }

// fieldsOf reads the `fields` input, which is the catalog's first record_list
// INPUT -- every other capability shipped takes strings, ints and bools.
//
// contract.Capability.ValidateInput has already checked the shape and the
// required sub-fields by the time this runs, so what is left here is the part
// a schema cannot state: that two fields must not share a name, because the
// output is keyed by it and a duplicate would produce rows nobody can pivot.
func fieldsOf(payload map[string]any) ([]field, error) {
	raw, _ := payload["fields"].([]any)
	if len(raw) == 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"scrapling: web.extract needs at least one field to read")
	}
	out := make([]field, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, entry := range raw {
		record, ok := entry.(map[string]any)
		if !ok {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"scrapling: fields[%d] is not a record", i)
		}
		name, _ := record["name"].(string)
		selector, _ := record["selector"].(string)
		if strings.TrimSpace(name) == "" || strings.TrimSpace(selector) == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"scrapling: fields[%d] needs both a name and a selector", i)
		}
		if _, dup := seen[name]; dup {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"scrapling: fields names %q twice, and the rows are keyed by that name", name)
		}
		seen[name] = struct{}{}
		out = append(out, field{name: name, selector: selector})
	}
	return out, nil
}

// format reads the requested rendering, defaulting to text.
func format(payload map[string]any) string {
	if f, ok := payload["format"].(string); ok && f != "" {
		return f
	}
	return "text"
}

// call reaches the server and returns its text answer.
func (r *Runner) call(ctx context.Context, tool string, args map[string]any) (string, error) {
	session, err := r.session(ctx)
	if err != nil {
		// The remedy in the message, because the ordinary cause is that
		// Scrapling is not installed and the operating system's own words for
		// that are "no such file or directory".
		return "", contract.Fail(contract.FailureUnavailable,
			"scrapling: the server is not reachable: %v -- install it with "+
				"`pip install \"scrapling[fetchers,ai]\" && scrapling install` and point "+
				"orchestrator.scrapling.process.command at the scrapling-mcp binary", err)
	}
	text, err := session.Call(ctx, tool, args)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"scrapling: %s did not answer: %v", tool, err)
	}
	if text == "" {
		return "", contract.Fail(contract.FailureUnavailable,
			"scrapling: %s answered with nothing", tool)
	}
	return text, nil
}

// answer is one page as this adapter understands it, whatever shape it
// arrived in.
type answer struct {
	status    int
	finalURL  string
	title     string
	truncated bool
	text      string
	markdown  string
	html      string
	// content is whichever body field was present, used for the block check
	// so that a challenge page is caught regardless of which rendering the
	// caller asked for.
	content string
	// parts is the far side's `content` list before it was joined, which is
	// the whole reason web.extract can exist without a second CSS engine:
	// a narrowed answer arrives as one element per match, so the list IS the
	// result set. web.fetch joins it into one body; web.extract keeps the
	// rows apart. Empty on an answer that carried no list.
	parts []string
}

// pick returns the body in the requested rendering, falling back rather than
// failing.
//
// A format the far side did not produce is not worth refusing a fetch over:
// the page was retrieved, the bytes exist, and handing back the rendering
// that does exist is more useful than an error about the one that does not.
// The capability's enum is what keeps the request honest; this is only about
// what came back.
func (a answer) pick(want string) string {
	order := map[string][]string{
		"text":     {a.text, a.markdown, a.html},
		"markdown": {a.markdown, a.text, a.html},
		"html":     {a.html, a.text, a.markdown},
	}[want]
	if order == nil {
		order = []string{a.text, a.markdown, a.html}
	}
	for _, candidate := range order {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// answerOf decodes what the far side said.
//
// Measured against scrapling-mcp on 2026-08-26, whose answer to make_request,
// fetch and stealthy_fetch is:
//
//	{"status": 200, "url": "https://example.com/", "content": ["...", ""]}
//
// Three facts about that shape are worth writing down because none of them is
// the obvious guess:
//
//   - `content` is a LIST of strings, not a string, and what the list HOLDS
//     depends on whether a selector was given. With none, it is the whole page
//     followed by one empty string. With one, it is ONE ELEMENT PER MATCH,
//     followed by one empty string -- measured on Hacker News: no selector gave
//     [3881 chars, ""], and `.titleline` gave thirty short strings and an empty
//     one. The page is not in the list at all when a selector narrowed it.
//   - there is no `title` field at all. The declared output keeps `title`
//     optional and this adapter leaves it empty rather than parsing one out of
//     the body, which would be this package inventing a fact.
//   - the rendering is chosen on the way IN, by extraction_type, so only one
//     ever comes back. `pick` still exists for the fallback it names, but on
//     this far side it is choosing between one candidate and nothing.
//
// The decode stays permissive around those facts: a string `content`, and a
// bare page with no envelope at all, are both still read. What it will not do
// is invent a body -- an answer carrying none is a failure that quotes what
// arrived, never an empty page reported as a successful fetch.
func answerOf(tool, text string) (answer, error) {
	var raw struct {
		Status     *int            `json:"status"`
		StatusCode *int            `json:"status_code"`
		URL        string          `json:"url"`
		FinalURL   string          `json:"final_url"`
		Title      string          `json:"title"`
		Truncated  bool            `json:"truncated"`
		Content    json.RawMessage `json:"content"`
		Text       string          `json:"text"`
		Markdown   string          `json:"markdown"`
		HTML       string          `json:"html"`
		Body       string          `json:"body"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		// Not every answer is an envelope, and one that hands back the page
		// itself is giving us the only field that is strictly required.
		// Treating that as the body is a reading, so it is marked as one: no
		// status, no title, nothing claimed that was not said.
		return answer{text: text, content: text}, nil //nolint:nilerr // a bare page is the body, not a decode failure
	}
	parts := partsOf(raw.Content)
	body := strings.Join(parts, "\n\n")
	out := answer{
		parts:     parts,
		finalURL:  first(raw.FinalURL, raw.URL),
		title:     raw.Title,
		truncated: raw.Truncated,
		text:      first(raw.Text, body),
		markdown:  raw.Markdown,
		html:      first(raw.HTML, raw.Body),
	}
	switch {
	case raw.Status != nil:
		out.status = *raw.Status
	case raw.StatusCode != nil:
		out.status = *raw.StatusCode
	}
	out.content = out.pick("text")
	return out, nil
}

// requireBody is the guard that used to live inside answerOf, and moving it
// out is the whole reason web.extract can share that decoder.
//
// An answer with no body means two different things depending on who asked.
// For web.fetch it is a failure: the promise is a page, and a page that came
// back empty is a provider that did not answer. For web.extract it is an
// ordinary result: the promise is whatever the selector matched, and matching
// nothing is a fact about the page rather than a fault in the far side.
//
// Deciding that inside the decoder meant deciding it once for both, which is
// the same conflation internal/adapter/kivgraph warns about at length -- there,
// an empty graph and a query that legitimately matched nothing look identical
// and must not be treated alike. So the decoder decodes, and each caller says
// what an empty answer means to the promise it is keeping.
func requireBody(tool, text string, out answer) error {
	if out.content != "" {
		return nil
	}
	return contract.Fail(contract.FailureUnavailable,
		"scrapling: %s answered with no page body under any name this understands "+
			"(content, text, markdown, html, body) -- got %s", tool, clip(text))
}

// bodyOf reads the `content` field, which arrives as a list on the far side
// measured here and as a plain string on anything simpler.
//
// Every non-empty part is joined, and the count is not fixed: one part for an
// unnarrowed page, one part PER MATCH when a selector narrowed it, and always
// a trailing empty string. Joining rather than indexing is what makes both
// shapes come out right without this function having to know which it was
// handed -- an earlier version described the list as [page, match] and would
// have kept only the first two of thirty matched rows had it ever indexed on
// that belief.
func partsOf(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		// The trailing empty string the far side always sends is dropped
		// here rather than at each caller, so neither the joined body nor
		// the extracted rows ever carry a blank nobody asked for.
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return kept
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func clip(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// challenges are the markers an anti-bot interstitial leaves in a page that
// otherwise looks like a successful fetch.
//
// Matched against the body rather than inferred from a status, because the
// whole difficulty is that the status is 200. Kept as an explicit list rather
// than a heuristic about page length: a short page is not evidence of a block,
// and treating it as one would turn every genuinely small page into a fallback
// to the most expensive implementation there is.
var challenges = []string{
	"just a moment...",
	"checking your browser",
	"cf-browser-verification",
	"cf_chl_opt",
	"__cf_chl",
	"challenge-platform",
	"attention required! | cloudflare",
	"enable javascript and cookies to continue",
	"verifying you are human",
}

// blocked reports whether an apparently successful answer is really a refusal.
//
// 403 and 429 join the body markers because both are how a bot filter says no
// without bothering to render anything, and both are worth another
// implementation's attempt. Every other status is left alone: a 404 is an
// answer about the page and escalating it would spend a browser to be told
// the same thing more slowly.
func blocked(status int, body string) bool {
	if status == 403 || status == 429 {
		return true
	}
	lowered := strings.ToLower(body)
	for _, marker := range challenges {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// sameTarget reports whether two URLs address the same place, so an ordinary
// answer does not pay for a second resolution.
func sameTarget(a, b string) bool {
	parsedA, errA := url.Parse(a)
	parsedB, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(parsedA.Host, parsedB.Host)
}

// mayReach is the gate. Nothing in this package dials anything without it
// having returned nil first.
//
// The order is the rule rather than an optimization. Scheme before host,
// because a `file://` URL has no host to judge and must not fall through to a
// list that is about networks. Denied before Domains, because a settings file
// naming the same place in both lists is a mistake and the safe reading of a
// mistake is the refusal. And addresses last, after resolution, because that
// is the only step that judges where the request will actually go rather than
// what it was asked to go to.
func (r *Runner) mayReach(ctx context.Context, rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return contract.Fail(contract.FailureInvalidInput, "scrapling: no url was given")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput,
			"scrapling: %q is not a url: %v", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return contract.Fail(contract.FailurePermissionDenied,
			"scrapling: %q is not http or https -- this capability reaches the web and "+
				"nothing else", rawURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"scrapling: %q names no host", rawURL)
	}
	if pattern, refused := r.deniedHost(host); refused {
		return contract.Fail(contract.FailurePermissionDenied,
			"scrapling: %s is refused by [web] denied entry %q", host, pattern)
	}
	if len(r.domains) > 0 && !r.allowedHost(host) {
		return contract.Fail(contract.FailurePermissionDenied,
			"scrapling: %s is not in [web] domains -- that list narrows this adapter to "+
				"the hosts it names, so add it there or empty the list to reach any public host",
			host)
	}
	return r.addressesAllowed(ctx, host)
}

// addressesAllowed resolves the host and judges what came back.
//
// This is the step the whole gate rests on. A name is a claim about an
// address and anybody may publish one: refusing `127.0.0.1` while allowing a
// public name whose A record points there is not a weaker version of this
// check, it is no check at all.
func (r *Runner) addressesAllowed(ctx context.Context, host string) error {
	if len(r.deniedNets) == 0 {
		// Nothing to hold an address against. Resolving anyway would spend a
		// lookup to reach a foregone conclusion, and this is the posture
		// somebody asked for explicitly -- see Options.Denied.
		return nil
	}
	// A literal address needs no resolver, and going through one would let a
	// resolver's opinion overrule what the caller plainly wrote.
	if addr, err := netip.ParseAddr(host); err == nil {
		return r.addressAllowed(host, addr.Unmap())
	}
	addrs, err := r.resolve(ctx, host)
	if err != nil {
		return contract.Fail(contract.FailureNotFound,
			"scrapling: %s does not resolve: %v", host, err)
	}
	if len(addrs) == 0 {
		return contract.Fail(contract.FailureNotFound,
			"scrapling: %s resolves to no address", host)
	}
	// Every address, not the first. A name with one public and one private
	// record would otherwise pass or fail on resolver ordering, which is not
	// a decision anybody made.
	for _, addr := range addrs {
		if err := r.addressAllowed(host, addr); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) addressAllowed(host string, addr netip.Addr) error {
	for _, prefix := range r.deniedNets {
		if prefix.Contains(addr) {
			return contract.Fail(contract.FailurePermissionDenied,
				"scrapling: %s resolves to %s, which [web] denied refuses through %s",
				host, addr, prefix)
		}
	}
	return nil
}

// deniedHost matches a host against the name-shaped denial rules, and returns
// which one refused it so the message can name it.
func (r *Runner) deniedHost(host string) (string, bool) {
	for _, pattern := range r.deniedHosts {
		if matchHost(pattern, host) {
			return pattern, true
		}
	}
	return "", false
}

func (r *Runner) allowedHost(host string) bool {
	for _, pattern := range r.domains {
		if matchHost(pattern, host) {
			return true
		}
	}
	return false
}

// matchHost reads one host rule.
//
// `*.lan` covers any label under `lan` AND `lan` itself, because somebody who
// refuses the LAN means the whole of it and a bare `lan` slipping through
// would be a surprise nobody wants to debug. A pattern without the star is a
// domain: it matches itself and anything under it, so `example.com` covers
// `api.example.com`, which is what a person writing a domain into an
// allow-list means by it.
func matchHost(pattern, host string) bool {
	if suffix, found := strings.CutPrefix(pattern, "*."); found {
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}
