package core

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/dashboard"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestDashboardCursorUsesStableLastID(t *testing.T) {
	ids := []string{"20260830T100000-a", "20260830T100001-b", "20260830T100002-c"}
	cursor := encodeCursor(ids[0])
	if got := cursorIndex(ids, cursor); got != 1 {
		t.Fatalf("cursor index = %d, want 1", got)
	}
	// Newer rows append at the end and do not move the continuation point.
	ids = append(ids, "20260830T100003-d")
	if got := cursorIndex(ids, cursor); got != 1 {
		t.Fatalf("cursor index after append = %d, want 1", got)
	}
	if got := cursorIndex(ids, "not-a-cursor"); got != 0 {
		t.Fatalf("invalid cursor index = %d, want 0", got)
	}
}

func TestDashboardSessionsRebuildHistoryAndGraph(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute).UTC()
	if err := store.Save(checkpoint.Run{ID: "20260830T120000-aabbcc", Kind: checkpoint.KindAsk, Session: "chat-1", SessionClient: "codex", Task: "buscar rutas", Started: started, Updated: started.Add(10 * time.Second), Closed: true, Verdict: "ok", Repositories: []string{"atenea"}, Steps: []checkpoint.StepState{{ID: "step-1", Capability: "code.search", Repository: "atenea", Implementation: "ripgrep", Verdict: "ok", Review: "ok", Tokens: 42, TokensKnown: true, ClosedAt: started.Add(10 * time.Second)}}}); err != nil {
		t.Fatal(err)
	}
	c := &Core{checkpoints: store, sessions: map[string]*Session{}, events: observability.New(8)}
	items, err := c.dashboardSessions(dashboard.Query{Limit: 10, Filter: map[string]string{"range": "all"}})
	if err != nil {
		t.Fatal(err)
	}
	page := items.(dashboardPage)
	got := page.Items.([]dashboardSession)
	if len(got) != 1 || got[0].ID != "chat-1" || got[0].Client != "codex" {
		t.Fatalf("sessions = %+v", got)
	}
	if got[0].Stats.Tokens != 42 || got[0].Stats.TokensKnownRuns != 1 {
		t.Fatalf("stats = %+v", got[0].Stats)
	}
	detailRaw, err := c.dashboardSession("chat-1")
	if err != nil {
		t.Fatal(err)
	}
	detail := detailRaw.(dashboardSessionDetail)
	if len(detail.Graph.Nodes) < 4 || len(detail.Runs) != 1 || len(detail.Timeline) == 0 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestDashboardTextRedactsAndBoundsProviderWords(t *testing.T) {
	if got := safeDashboardText(`Authorization: Bearer abc123`, 240); strings.Contains(got, "abc123") {
		t.Fatalf("credential survived redaction: %q", got)
	}
	got := safeDashboardText(strings.Repeat("x", 20), 8)
	if len(got) > 8 || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded text = %q", got)
	}
}

func TestDashboardRunViewIncludesProviderFromCatalog(t *testing.T) {
	catalog := registry.New()
	capability := contract.Capability{ID: "code.search", Version: contract.Version{Major: 1, Minor: 0, Patch: 0}, Summary: "search", Inputs: []contract.Field{{Name: "query", Type: contract.TypeString, Required: true}}}
	if err := catalog.AddCapability(capability); err != nil {
		t.Fatal(err)
	}
	if err := catalog.AddImplementation(contract.Implementation{ID: "codex.search", Provider: "codex", Capability: "code.search"}); err != nil {
		t.Fatal(err)
	}
	view := (&Core{catalog: catalog}).dashboardRunView(checkpoint.Run{ID: "r1", Steps: []checkpoint.StepState{{ID: "s1", Implementation: "codex.search", Repository: "atenea", Verdict: "ok"}}})
	if got := view.Steps[0].Provider; got != "codex" {
		t.Fatalf("provider = %q, want codex", got)
	}
}

func TestDashboardRunViewDoesNotExposeRepositoryPaths(t *testing.T) {
	view := (&Core{}).dashboardRunView(checkpoint.Run{
		ID:           "r-path",
		Repositories: []string{"/Users/private/project", "atenea"},
		Steps:        []checkpoint.StepState{{ID: "s1", Repository: "/Users/private/project", Implementation: "ripgrep"}},
	})
	if len(view.Repositories) != 1 || view.Repositories[0] != "atenea" || view.Steps[0].Repository != "" {
		t.Fatalf("unsafe paths survived dashboard projection: %+v", view)
	}
}

func TestDashboardGraphIncludesReviewRetryAndCloseNodes(t *testing.T) {
	graph := appendRunGraph(dashboardGraph{Nodes: []dashboardGraphNode{{ID: "session:s1", Kind: "session", Label: "s1"}}}, dashboardRun{
		ID: "r1", SessionID: "s1", Verdict: "ok",
		Steps: []dashboardStep{{ID: "step-1", Capability: "code.search", Implementation: "ripgrep", Provider: "local", Verdict: "ok", Review: "ok", Attempt: 2}},
	})
	kinds := map[string]bool{}
	for _, node := range graph.Nodes {
		kinds[node.Kind] = true
	}
	for _, kind := range []string{"review", "retry", "close"} {
		if !kinds[kind] {
			t.Fatalf("graph missing %s node: %+v", kind, graph.Nodes)
		}
	}
}

func TestDashboardRunFiltersAcrossSafeDimensions(t *testing.T) {
	catalog := registry.New()
	if err := catalog.AddCapability(contract.Capability{ID: "code.search", Version: contract.Version{Major: 1}, Summary: "search", Inputs: []contract.Field{{Name: "query", Type: contract.TypeString, Required: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.AddImplementation(contract.Implementation{ID: "codex.search", Provider: "codex", Capability: "code.search"}); err != nil {
		t.Fatal(err)
	}
	c := &Core{catalog: catalog}
	run := checkpoint.Run{ID: "r1", Session: "s1", Task: "Review dashboard", Repositories: []string{"atenea"}, Steps: []checkpoint.StepState{{Capability: "code.search", Implementation: "codex.search"}}}
	for key, value := range map[string]string{"session": "s1", "project": "atenea", "capability": "code.search", "implementation": "codex.search", "tool": "codex.search", "provider": "codex", "q": "dashboard"} {
		if !c.dashboardRunMatches(run, map[string]string{key: value}) {
			t.Errorf("filter %s=%s did not match", key, value)
		}
	}
	if c.dashboardRunMatches(run, map[string]string{"provider": "other"}) {
		t.Error("foreign provider matched")
	}
}

func TestSessionMetadataUsesProvidedNameAndMostSpecificProject(t *testing.T) {
	root := t.TempDir()
	nested := root + "/nested"
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := registry.New()
	for _, repo := range []contract.Repository{
		contract.NewRepository("workspace", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
		contract.NewRepository("nested", nested, nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
	} {
		if err := catalog.AddRepository(repo); err != nil {
			t.Fatal(err)
		}
	}
	c := &Core{catalog: catalog, sessions: map[string]*Session{}}
	session, err := c.Open(SessionOptions{ID: "s1", Client: "codex", Title: "Mejorar dashboard", ExternalID: "codex:opaque", Workspace: nested + "/src", Origin: SessionOrigin{Surface: "codex", Transport: "mcp-stdio"}})
	if err != nil {
		t.Fatal(err)
	}
	name, basis, project, origin, external := session.DashboardMetadata()
	if name != "Mejorar dashboard" || basis != "provided" || project != "nested" || origin.Transport != "mcp-stdio" || !external {
		t.Fatalf("metadata = %q %q %q %+v %v", name, basis, project, origin, external)
	}
}

func TestOldCheckpointDerivesSafeSessionIdentity(t *testing.T) {
	item := &dashboardSession{}
	applyCheckpointSessionMetadata(item, checkpoint.Run{Task: `Authorization: Bearer secret`, Repositories: []string{"atenea"}})
	if strings.Contains(item.Name, "secret") || item.NameBasis != "derived" {
		t.Fatalf("derived identity = %+v", item)
	}
}

func TestDashboardMetricSummaryKeepsUnknownsOutOfTheAggregate(t *testing.T) {
	summary := summarizeDashboardMetrics([]metrics.Row{{Attempts: 4, Successes: 3, Failures: 1, Mean: 2 * time.Second, Slowest: 5 * time.Second, Tokens: 12}})
	if summary.SuccessRate == nil || *summary.SuccessRate != 75 {
		t.Fatalf("success rate = %+v, want 75", summary.SuccessRate)
	}
	if summary.DurationMS != 2000 || summary.DurationMaxMS != 5000 || summary.Tokens != 12 {
		t.Fatalf("summary = %+v", summary)
	}
	empty := summarizeDashboardMetrics(nil)
	if empty.SuccessRate != nil || empty.DurationMS != 0 {
		t.Fatalf("empty summary invented a value: %+v", empty)
	}
}
