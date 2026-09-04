package scrapling

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// crawl answers web.crawl: one site walked from a starting page.
//
// The seed is gated before dispatch and returned pages are gated before release.
// The external crawler's allowed_domains limits discovery, not every network
// connection: DNS changes, redirects and browser subresources require a network
// sandbox in the process making requests. Output validation cannot undo a fetch.
//
// # Two levels rather than three
//
// A crawl is already many requests. A middle tier between plain and stealth
// would multiply a cost that is the whole reason to think twice before
// reaching for this capability rather than web.fetch.
//
// And neither escalates. With two levels the cheap one moving up IS the whole
// ladder, and a crawl that spent its page budget being challenged has spent
// it -- reporting that as unavailable would buy a retry of everything at the
// expensive level, for a site that has already said no once per page.
func (r *Runner) crawl(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	started := time.Now()
	if r.spider == nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"scrapling: web.crawl has no spider helper configured -- Scrapling's MCP server has no "+
				"crawl tool, so this capability needs orchestrator.scrapling.spider pointed at "+
				"helper/scrapling-spider/atenea_spider.py")
	}

	seed, _ := req.Payload["start_url"].(string)
	if err := r.mayReach(ctx, seed); err != nil {
		return contract.Outcome{}, err
	}

	args := map[string]any{
		"url":             seed,
		"extraction_type": format(req.Payload),
		"stealth":         req.Implementation.ID == ImplementationCrawlStealth,
	}
	if pages, ok := intAt(req.Payload, "max_pages"); ok {
		args["max_pages"] = pages
	}
	if depth, ok := intAt(req.Payload, "max_depth"); ok {
		args["max_depth"] = depth
	}
	if selector, ok := req.Payload["selector"].(string); ok && selector != "" {
		args["selector"] = selector
	}

	text, err := r.callSpider(ctx, "crawl", args)
	if err != nil {
		return contract.Outcome{}, err
	}
	var answer struct {
		Pages []struct {
			URL     string `json:"url"`
			Depth   int    `json:"depth"`
			Status  int    `json:"status"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"pages"`
		StoppedBy string `json:"stopped_by"`
		Host      string `json:"host"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"scrapling: the spider's answer is not the shape this expects: %v -- got %s", err, clip(text))
	}
	if len(answer.Pages) == 0 {
		// A crawl that reached nothing is a crawl that did not run. The seed
		// itself is always page zero when the fetch worked, so an empty list
		// means the walk never started rather than a site with no links.
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"scrapling: the spider walked %s and came back with no pages at all", seed)
	}

	rows := make([]map[string]any, 0, len(answer.Pages))
	seedURL, _ := url.Parse(seed)
	for _, page := range answer.Pages {
		pageURL, parseErr := url.Parse(page.URL)
		if parseErr != nil || !strings.EqualFold(pageURL.Hostname(), seedURL.Hostname()) {
			return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied, "spider returned a page outside the seed host")
		}
		if err := r.mayReach(ctx, page.URL); err != nil {
			return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied, "spider page destination refused")
		}

		rows = append(rows, map[string]any{
			"url":     page.URL,
			"depth":   page.Depth,
			"status":  page.Status,
			"title":   page.Title,
			"content": page.Content,
		})
	}

	out := contract.Outcome{
		Result:      map[string]any{"pages": rows},
		Verdict:     contract.VerdictOK,
		ToolVersion: r.spiderVersion(ctx),
		// Duration only, as with the other two: the helper is the supervisor's
		// process and a crawl is not a model turn.
		Spent:         contract.Sample{Duration: time.Since(started)},
		SpentUSD:      0,
		SpentUSDKnown: true,
	}
	// Why the walk ended is the one thing a caller cannot work out from the
	// rows: a budget that ran out and a site that ran out of links produce the
	// same shape, and only one of them means "there is more here".
	if answer.StoppedBy != "" {
		out.Discoveries = append(out.Discoveries, contract.Discovery{
			Note: "the crawl of " + answer.Host + " ended because " + answer.StoppedBy,
		})
	}
	return out, nil
}

// callSpider reaches the crawl helper. Separate from call because the two have
// different far sides and their remedies are different sentences.
func (r *Runner) callSpider(ctx context.Context, tool string, args map[string]any) (string, error) {
	session, err := r.spider(ctx)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"scrapling: the spider helper is not reachable: %v -- it ships in this repository at "+
				"helper/scrapling-spider/ and runs on the same interpreter Scrapling is installed on", err)
	}
	text, err := session.Call(ctx, tool, args)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"scrapling: the spider did not answer: %v", err)
	}
	if text == "" {
		return "", contract.Fail(contract.FailureUnavailable,
			"scrapling: the spider answered with nothing")
	}
	return text, nil
}

func (r *Runner) spiderVersion(ctx context.Context) string {
	if r.spider == nil {
		return ""
	}
	session, err := r.spider(ctx)
	if err != nil || session == nil {
		return ""
	}
	return session.Version()
}

// servesCrawl reports whether this runner kept the crawl implementations,
// which it does only when a helper was supplied. Capabilities() asks, so that
// what the runner says it can dispatch matches what it actually claims.
func (r *Runner) servesCrawl() bool {
	for _, id := range r.implementations {
		if levels[id].capability == CapabilityCrawl {
			return true
		}
	}
	return false
}

// intAt reads an integer the way every adapter in this tree does, because JSON
// numbers arrive as float64 and the MCP path has always delivered them so.
func intAt(payload map[string]any, key string) (int, bool) {
	switch value := payload[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}
