package scrapling_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/scrapling"
	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func crawlCapability() contract.Capability {
	return contract.Capability{
		ID: scrapling.CapabilityCrawl, Version: contract.Version{Major: 1},
		Summary:     "Walk one site from a starting page.",
		Effects:     []contract.Effect{contract.EffectRead, contract.EffectExternal},
		SubjectFrom: "start_url", SubjectKind: contract.SubjectURLHost,
		Inputs: []contract.Field{
			{Name: "start_url", Type: contract.TypeString, Required: true, Summary: "The seed."},
			{Name: "max_pages", Type: contract.TypeInt, Summary: "How many at most."},
			{Name: "max_depth", Type: contract.TypeInt, Summary: "How deep."},
			{Name: "selector", Type: contract.TypeString, Summary: "Narrow each page."},
			{Name: "format", Type: contract.TypeString, Summary: "Rendering.",
				Enum: []string{"text", "markdown", "html"}},
		},
		Outputs: []contract.Field{{Name: "pages", Type: contract.TypeRecordList, Required: true,
			Summary: "The pages reached.",
			Fields: []contract.Field{
				{Name: "url", Type: contract.TypeString, Required: true, Summary: "Its url."},
				{Name: "depth", Type: contract.TypeInt, Required: true, Summary: "Links from the seed."},
				{Name: "status", Type: contract.TypeInt, Summary: "HTTP status."},
				{Name: "title", Type: contract.TypeString, Summary: "Its title."},
				{Name: "content", Type: contract.TypeString, Required: true, Summary: "The page."},
			}}},
	}
}

func crawlRequest(t *testing.T, implementation string, payload map[string]any) contract.RunRequest {
	t.Helper()
	capability := crawlCapability()
	return contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: implementation, Capability: capability.ID},
		Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
		Payload:        payload,
		Permission:     contract.Permission{Task: "walk a site", Effects: capability.Effects},
	}
}

func walked(pages ...map[string]any) map[string]any {
	list := make([]any, 0, len(pages))
	for _, p := range pages {
		list = append(list, p)
	}
	return map[string]any{"pages": list, "stopped_by": "the crawl ran out of links", "host": "example.com"}
}

func page0(url string, depth int) map[string]any {
	return map[string]any{"url": url, "depth": depth, "status": 200, "title": "T", "content": "body"}
}

func TestACrawlComesBackAsPagesInOrder(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, nil),
		Spider: fakeServer(t, map[string]any{"crawl": walked(
			page0("https://example.com/", 0),
			page0("https://example.com/a", 1),
			page0("https://example.com/b", 1),
		)}),
	})
	out, err := runner.Run(t.Context(), crawlRequest(t, scrapling.ImplementationCrawl,
		map[string]any{"start_url": "https://example.com/", "max_pages": 3, "max_depth": 1}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	pages, ok := out.Result["pages"].([]map[string]any)
	if !ok || len(pages) != 3 {
		t.Fatalf("result = %+v", out.Result)
	}
	if pages[0]["depth"] != 0 || pages[1]["depth"] != 1 {
		t.Errorf("depths = %v, %v, want the seed first", pages[0]["depth"], pages[1]["depth"])
	}
	if err := crawlCapability().ValidateOutput(out.Result); err != nil {
		t.Errorf("ValidateOutput: %v", err)
	}
	// Why the walk ended is the one thing the rows cannot say: a budget that
	// ran out and a site with no more links look identical, and only one of
	// them means there is more here.
	if len(out.Discoveries) == 0 || !strings.Contains(out.Discoveries[0].Note, "ran out of links") {
		t.Errorf("discoveries = %+v, want the reason the walk ended", out.Discoveries)
	}
}

// The gate is the same gate, and it runs before the helper is even woken.
func TestACrawlSeedCrossesTheSameGate(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, nil),
		Spider:  fakeServer(t, map[string]any{"crawl": walked(page0("http://127.0.0.1/", 0))}),
	})
	_, err := runner.Run(t.Context(), crawlRequest(t, scrapling.ImplementationCrawl,
		map[string]any{"start_url": "http://127.0.0.1:40010/mcp"}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailurePermissionDenied {
		t.Fatalf("error = %v, want permission_denied", err)
	}
}

// The two levels differ by one argument this adapter builds, never by one a
// caller passes.
func TestStealthIsAnArgumentThisAdapterBuilds(t *testing.T) {
	for _, tc := range []struct {
		implementation string
		want           bool
	}{
		{scrapling.ImplementationCrawl, false},
		{scrapling.ImplementationCrawlStealth, true},
	} {
		runner := newRunner(t, scrapling.Options{
			Session: fakeServer(t, nil),
			Spider:  fakeServer(t, map[string]any{"crawl": walked(page0("https://example.com/", 0))}),
		})
		if _, err := runner.Run(t.Context(), crawlRequest(t, tc.implementation,
			map[string]any{"start_url": "https://example.com/"})); err != nil {
			t.Fatalf("Run: %v", err)
		}
		params := <-callsSeen
		args, _ := params["arguments"].(map[string]any)
		if args["stealth"] != tc.want {
			t.Errorf("%s sent stealth=%v, want %v", tc.implementation, args["stealth"], tc.want)
		}
		if params["name"] != "crawl" {
			t.Errorf("both levels must call the one helper tool, got %v", params["name"])
		}
	}
}

// A walk that reached nothing is a walk that did not run: the seed is always
// page zero when the fetch worked, so an empty list is not "a site with no
// links" -- it is a provider that answered without doing anything.
func TestAnEmptyWalkIsAFailureRatherThanAnEmptyAnswer(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, nil),
		Spider:  fakeServer(t, map[string]any{"crawl": walked()}),
	})
	_, err := runner.Run(t.Context(), crawlRequest(t, scrapling.ImplementationCrawl,
		map[string]any{"start_url": "https://example.com/"}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

// Without a helper the crawl implementations are not claimed at all.
//
// Claiming them and failing at dispatch would be worse: the funnel would rank
// one, choose it, and learn at the far side that it was never there --
// contract.Runner's own doc calls that the disagreement Capabilities exists to
// catch.
func TestWithoutAHelperTheCrawlIsNotOffered(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, nil)})
	if slices.Contains(runner.Capabilities(), scrapling.CapabilityCrawl) {
		t.Error("web.crawl is offered with no helper behind it")
	}
	for _, id := range []string{scrapling.ImplementationCrawl, scrapling.ImplementationCrawlStealth} {
		if runner.Serves(id) {
			t.Errorf("%s is claimed with no helper behind it", id)
		}
	}
	// And the other two capabilities are untouched by its absence.
	if !slices.Contains(runner.Capabilities(), scrapling.CapabilityFetch) {
		t.Error("web.fetch went missing with the helper")
	}
}

func TestWithAHelperTheCrawlIsOffered(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, nil),
		Spider: func(context.Context) (*mcpstdio.Session, error) {
			return nil, errors.New("not started, and not asked to be")
		},
	})
	if !slices.Contains(runner.Capabilities(), scrapling.CapabilityCrawl) {
		t.Error("web.crawl is not offered with a helper declared")
	}
}

// The remedy belongs in the message: the ordinary cause is a settings file
// that never named the helper, and "not reachable" alone sends somebody
// looking at the network.
func TestAnUnreachableHelperSaysWhereItLives(t *testing.T) {
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, nil),
		Spider: func(context.Context) (*mcpstdio.Session, error) {
			return nil, errors.New("no such file or directory")
		},
	})
	_, err := runner.Run(t.Context(), crawlRequest(t, scrapling.ImplementationCrawl,
		map[string]any{"start_url": "https://example.com/"}))
	if err == nil {
		t.Fatal("a missing helper was not reported")
	}
	if !strings.Contains(err.Error(), "helper/scrapling-spider") {
		t.Errorf("error = %v, want it to name where the helper lives", err)
	}
}
