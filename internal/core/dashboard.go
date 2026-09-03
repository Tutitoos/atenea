package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/dashboard"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func (c *Core) newDashboard() (*dashboard.Server, error) {
	if !c.settings.Dashboard.Enabled {
		return nil, nil
	}
	listeners := []dashboard.Listener{{Addr: c.settings.Dashboard.Listen, Mode: c.settings.Dashboard.Access}}
	if c.settings.Dashboard.LANListen != "" {
		listeners = append(listeners, dashboard.Listener{Addr: c.settings.Dashboard.LANListen, Mode: "lan", CertFile: c.settings.Dashboard.LANCertFile, KeyFile: c.settings.Dashboard.LANKeyFile, TokenFile: c.settings.Dashboard.LANTokenFile})
	}
	return dashboard.NewServer(dashboard.Config{Enabled: true, Listeners: listeners, PageLimit: c.settings.Dashboard.PageLimit, SessionTTL: c.settings.Dashboard.SessionTTL}, dashboard.Provider{
		Snapshot:  func() (any, error) { return c.dashboardStatus(), nil },
		Overview:  c.dashboardOverview,
		Sessions:  c.dashboardSessions,
		Session:   c.dashboardSession,
		Runs:      c.dashboardRuns,
		Run:       c.dashboardRun,
		Metrics:   c.dashboardMetrics,
		Traces:    c.dashboardTraces,
		Incidents: c.dashboardIncidents,
		Catalog:   func() (any, error) { return c.dashboardCatalog(), nil },
		Events:    c.events,
	})
}

func (c *Core) dashboardStatus() Status {
	status := c.Status()
	for ci := range status.Capabilities {
		for ii := range status.Capabilities[ci].Implementations {
			h := &status.Capabilities[ci].Implementations[ii].Health
			h.Raw = ""
			h.Reason = safeDashboardText(h.Reason, 240)
		}
	}
	for i := range status.Servers {
		status.Servers[i].Reason = safeDashboardText(status.Servers[i].Reason, 240)
	}
	for i := range status.Maintenance {
		status.Maintenance[i].Failure = safeDashboardText(status.Maintenance[i].Failure, 240)
	}
	return status
}

func (c *Core) dashboardCatalog() any {
	status := c.dashboardStatus()
	return struct {
		Capabilities []CapabilityStatus `json:"capabilities"`
		Repositories []RepositoryStatus `json:"repositories"`
		Servers      []ServerStatus     `json:"servers"`
	}{status.Capabilities, status.Repositories, status.Servers}
}

type dashboardPage struct {
	Items      any    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      int    `json:"total"`
}

type dashboardRun struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	// The *_at/state/project/repositories aliases keep the DTO pleasant for
	// clients while the original receipt-shaped fields remain compatible with
	// the v2 dashboard contract.
	Session       string          `json:"session,omitempty"`
	Task          string          `json:"task"`
	Project       string          `json:"project,omitempty"`
	Repositories  []string        `json:"repositories,omitempty"`
	Started       time.Time       `json:"started"`
	StartedAt     time.Time       `json:"started_at,omitempty"`
	Updated       time.Time       `json:"updated"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty"`
	FinishedAt    time.Time       `json:"finished_at,omitempty"`
	DurationMS    int64           `json:"duration_ms,omitempty"`
	Closed        bool            `json:"closed"`
	Verdict       string          `json:"verdict"`
	State         string          `json:"state,omitempty"`
	BudgetUSD     float64         `json:"budget_usd"`
	Tokens        int64           `json:"tokens,omitempty"`
	TokensKnown   bool            `json:"tokens_known"`
	SpentUSD      float64         `json:"spent_usd,omitempty"`
	SpentUSDKnown bool            `json:"spent_usd_known"`
	Projects      []string        `json:"projects,omitempty"`
	Steps         []dashboardStep `json:"steps,omitempty"`
}

// dashboardMetricSummary is deliberately a small aggregate over retained
// attempts. Fields that cannot be calculated from the rollup stay omitted,
// rather than being represented as a misleading zero.
type dashboardMetricSummary struct {
	Attempts      int64    `json:"attempts,omitempty"`
	Successes     int64    `json:"successes,omitempty"`
	Failures      int64    `json:"failures,omitempty"`
	SuccessRate   *float64 `json:"success_rate,omitempty"`
	DurationMS    int64    `json:"duration_ms,omitempty"`
	DurationP50MS int64    `json:"duration_p50_ms,omitempty"`
	DurationP95MS int64    `json:"duration_p95_ms,omitempty"`
	DurationMaxMS int64    `json:"duration_max_ms,omitempty"`
	Tokens        int64    `json:"tokens,omitempty"`
	Rows          int64    `json:"rows,omitempty"`
	Cost          *float64 `json:"cost,omitempty"`
	CostKnownRuns int      `json:"cost_known_runs,omitempty"`
}

type dashboardMetricsSummary struct {
	Stats dashboardMetricSummary `json:"stats"`
	Items []metrics.Row          `json:"items"`
	Total int                    `json:"total"`
}

type dashboardStep struct {
	ID               string            `json:"id"`
	Capability       string            `json:"capability,omitempty"`
	Repository       string            `json:"repository"`
	Implementation   string            `json:"implementation,omitempty"`
	Provider         string            `json:"provider,omitempty"`
	Verdict          string            `json:"verdict"`
	State            string            `json:"state,omitempty"`
	Tool             string            `json:"tool,omitempty"`
	Review           string            `json:"review,omitempty"`
	Failure          string            `json:"failure,omitempty"`
	DurationMS       int64             `json:"duration_ms,omitempty"`
	Attempt          int               `json:"attempt,omitempty"`
	Ended            time.Time         `json:"ended,omitempty"`
	EndedAt          time.Time         `json:"ended_at,omitempty"`
	InputTokens      int64             `json:"input_tokens,omitempty"`
	OutputTokens     int64             `json:"output_tokens,omitempty"`
	CacheReadTokens  int64             `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64             `json:"cache_write_tokens,omitempty"`
	Tokens           int64             `json:"tokens,omitempty"`
	TokensKnown      bool              `json:"tokens_known"`
	PeakRSS          int64             `json:"peak_rss,omitempty"`
	RSSKnown         bool              `json:"rss_known"`
	ToolVersion      string            `json:"tool_version,omitempty"`
	SpentUSD         float64           `json:"spent_usd,omitempty"`
	SpentUSDKnown    bool              `json:"spent_usd_known"`
	OverspendUSD     float64           `json:"overspend_usd,omitempty"`
	Funnel           checkpoint.Funnel `json:"funnel"`
}

func (c *Core) dashboardRunView(run checkpoint.Run) dashboardRun {
	project := ""
	projects := safeDashboardProjects(run.Repositories)
	if len(projects) > 0 {
		project = projects[0]
	}
	duration := int64(0)
	if !run.Started.IsZero() && !run.Updated.IsZero() && run.Updated.After(run.Started) {
		duration = run.Updated.Sub(run.Started).Milliseconds()
	}
	out := dashboardRun{ID: run.ID, Kind: run.Kind, SessionID: run.Session, Session: run.Session, Task: safeDashboardText(run.Task, 280), Project: project, Repositories: projects, Started: run.Started, StartedAt: run.Started, Updated: run.Updated, UpdatedAt: run.Updated, Closed: run.Closed, Verdict: run.Verdict, State: run.Verdict, DurationMS: duration, BudgetUSD: run.BudgetUSD, Projects: append([]string(nil), projects...)}
	if run.Closed {
		out.FinishedAt = run.Updated
	}
	for _, step := range run.Steps {
		stepDuration := step.DurationMS
		if stepDuration < 0 {
			stepDuration = 0
		}
		view := dashboardStep{ID: step.ID, Capability: safeDashboardText(step.Capability, 120), Repository: safeDashboardProject(step.Repository), Implementation: safeDashboardText(step.Implementation, 120), Tool: safeDashboardText(step.Implementation, 120), Verdict: safeDashboardText(step.Verdict, 40), State: safeDashboardText(step.Verdict, 40), Review: safeDashboardText(step.Review, 240), Failure: safeDashboardText(step.Failure, 240), DurationMS: stepDuration, InputTokens: step.InputTokens, OutputTokens: step.OutputTokens, CacheReadTokens: step.CacheReadTokens, CacheWriteTokens: step.CacheWriteTokens, Tokens: step.Tokens, TokensKnown: step.TokensKnown, PeakRSS: step.PeakRSS, RSSKnown: step.RSSKnown, ToolVersion: safeDashboardText(step.ToolVersion, 120), SpentUSD: step.SpentUSD, SpentUSDKnown: step.SpentUSDKnown, OverspendUSD: step.OverspendUSD, Funnel: safeDashboardFunnel(step.Funnel)}
		if c != nil && c.catalog != nil {
			if implementation, err := c.catalog.Implementation(step.Implementation); err == nil {
				view.Provider = implementation.Provider
			}
		}
		if !step.ClosedAt.IsZero() {
			view.Ended, view.EndedAt = step.ClosedAt, step.ClosedAt
		}
		out.Steps = append(out.Steps, view)
		if step.TokensKnown {
			out.TokensKnown = true
			out.Tokens += dashboardStepTokens(view)
		}
		if step.SpentUSDKnown {
			out.SpentUSDKnown = true
			out.SpentUSD += step.SpentUSD
		}
	}
	return out
}

func dashboardStepTokens(step dashboardStep) int64 {
	if step.Tokens != 0 {
		return step.Tokens
	}
	return step.InputTokens + step.OutputTokens + step.CacheReadTokens + step.CacheWriteTokens
}

func safeDashboardFunnel(in checkpoint.Funnel) checkpoint.Funnel {
	out := checkpoint.Funnel{State: in.State}
	if len(in.Stages) == 0 {
		return out
	}
	out.Stages = make([]checkpoint.FunnelStage, 0, len(in.Stages))
	for _, stage := range in.Stages {
		safe := checkpoint.FunnelStage{Name: stage.Name, In: stage.In, Out: stage.Out}
		if len(stage.Dropped) > 0 {
			safe.Dropped = make([]checkpoint.FunnelDrop, 0, len(stage.Dropped))
			for _, drop := range stage.Dropped {
				safe.Dropped = append(safe.Dropped, checkpoint.FunnelDrop{
					Implementation: drop.Implementation,
					Reason:         safeDashboardText(drop.Reason, 240),
				})
			}
		}
		out.Stages = append(out.Stages, safe)
	}
	return out
}

func (c *Core) dashboardRuns(q dashboard.Query) (any, error) {
	q.Since = dashboardSince(q)
	if q.Limit <= 0 {
		q.Limit = c.settings.Dashboard.PageLimit
		if q.Limit <= 0 {
			q.Limit = 100
		}
	}
	ids, err := c.checkpoints.List()
	if err != nil {
		return nil, err
	}
	// Checkpoint.List is intentionally oldest-first for retention. The web
	// history is newest-first and uses an opaque id cursor, so reverse a copy
	// before paging instead of exposing filesystem order.
	ids = append([]string(nil), ids...)
	for left, right := 0, len(ids)-1; left < right; left, right = left+1, right-1 {
		ids[left], ids[right] = ids[right], ids[left]
	}
	start := dashboardIDCursorIndex(ids, q.Cursor)
	items := make([]dashboardRun, 0, q.Limit)
	hasMore := false
	for offset, id := range ids[start:] {
		run, loadErr := c.checkpoints.Load(id)
		if loadErr != nil {
			continue
		}
		if q.Since.After(run.Updated) || !c.dashboardRunMatches(run, q.Filter) {
			continue
		}
		items = append(items, c.dashboardRunView(run))
		if len(items) >= q.Limit {
			hasMore = offset+1 < len(ids[start:])
			break
		}
	}
	next := ""
	if hasMore && len(items) > 0 {
		next = encodeCursor(items[len(items)-1].ID)
	}
	return dashboardPage{Items: items, NextCursor: next, Total: len(ids)}, nil
}

func dashboardIDCursorIndex(ids []string, raw string) int {
	if raw == "" {
		return 0
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || !strings.HasPrefix(string(body), "id:") {
		return 0
	}
	id := strings.TrimPrefix(string(body), "id:")
	for i, candidate := range ids {
		if candidate == id {
			return i + 1
		}
	}
	return 0
}

func (c *Core) dashboardRunMatches(run checkpoint.Run, filter map[string]string) bool {
	if filter == nil {
		return true
	}
	if want := strings.TrimSpace(filter["kind"]); want != "" && run.Kind != want {
		return false
	}
	if want := strings.TrimSpace(filter["state"]); want != "" && run.Verdict != want {
		return false
	}
	if want := strings.TrimSpace(filter["session"]); want != "" && run.Session != want {
		return false
	}
	if want := strings.TrimSpace(filter["project"]); want != "" && !slices.Contains(run.Repositories, want) {
		return false
	}
	if want := strings.ToLower(strings.TrimSpace(filter["q"])); want != "" && !strings.Contains(strings.ToLower(run.ID+" "+safeDashboardText(run.Task, 280)), want) {
		return false
	}
	for key, match := range map[string]func(checkpoint.StepState) bool{
		"capability":     func(step checkpoint.StepState) bool { return step.Capability == filter["capability"] },
		"implementation": func(step checkpoint.StepState) bool { return step.Implementation == filter["implementation"] },
		"tool":           func(step checkpoint.StepState) bool { return step.Implementation == filter["tool"] },
	} {
		if filter[key] != "" && !slices.ContainsFunc(run.Steps, match) {
			return false
		}
	}
	if want := strings.TrimSpace(filter["provider"]); want != "" {
		matched := slices.ContainsFunc(run.Steps, func(step checkpoint.StepState) bool {
			if c == nil || c.catalog == nil {
				return false
			}
			implementation, err := c.catalog.Implementation(step.Implementation)
			return err == nil && implementation.Provider == want
		})
		if !matched {
			return false
		}
	}
	return true
}

func (c *Core) dashboardRun(id string) (any, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\\`) {
		return nil, fmt.Errorf("invalid run id")
	}
	run, err := c.checkpoints.Load(id)
	if err != nil {
		return nil, err
	}
	return c.dashboardRunView(run), nil
}

func (c *Core) dashboardMetrics(q dashboard.Query) (any, error) {
	q.Since = dashboardSince(q)
	if q.Limit <= 0 {
		q.Limit = c.settings.Dashboard.PageLimit
		if q.Limit <= 0 {
			q.Limit = 100
		}
	}
	since := q.Since
	if since.IsZero() {
		since = time.Now().Add(-24 * time.Hour)
	}
	if q.Filter["view"] == "series" {
		bucket := dashboardBucket(q.Filter["bucket"])
		if c.measurements == nil {
			return dashboardPage{Items: []metrics.SeriesPoint{}, Total: 0}, nil
		}
		points, err := c.measurements.Series(context.Background(), since, bucket)
		if err != nil {
			return nil, err
		}
		return dashboardPage{Items: points, Total: len(points)}, nil
	}
	rows, err := c.Measurements(since)
	if err != nil {
		return nil, err
	}
	filtered := rows[:0]
	for _, row := range rows {
		if want := q.Filter["capability"]; want != "" && row.Capability != want {
			continue
		}
		if want := q.Filter["tool"]; want != "" && row.Implementation != want {
			continue
		}
		if want := q.Filter["implementation"]; want != "" && row.Implementation != want {
			continue
		}
		if want := q.Filter["provider"]; want != "" && row.Provider != want {
			continue
		}
		if want := q.Filter["repository"]; want != "" && row.Repository != want {
			continue
		}
		filtered = append(filtered, row)
	}
	if q.Filter["view"] == "summary" {
		summary := summarizeDashboardMetrics(filtered)
		if cost, known := c.dashboardCheckpointCost(q); known > 0 {
			summary.Cost = &cost
			summary.CostKnownRuns = known
		}
		return dashboardMetricsSummary{Stats: summary, Items: filtered, Total: len(filtered)}, nil
	}
	keys := make([]string, len(filtered))
	for i, row := range filtered {
		keys[i] = metricCursorKey(row)
	}
	start := cursorIndex(keys, q.Cursor)
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + q.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	next := ""
	if end < len(filtered) {
		next = encodeCursor(keys[end-1])
	}
	return dashboardPage{Items: filtered[start:end], NextCursor: next, Total: len(filtered)}, nil
}

// dashboardCheckpointCost complements DuckDB's operational rollups with the
// explicit provider charges retained on checkpoints. A missing charge is not a
// zero-dollar run, so the aggregate is omitted until at least one run reports
// a known value.
func (c *Core) dashboardCheckpointCost(q dashboard.Query) (float64, int) {
	if c == nil || c.checkpoints == nil {
		return 0, 0
	}
	ids, err := c.checkpoints.List()
	if err != nil {
		return 0, 0
	}
	var total float64
	known := 0
	for _, id := range ids {
		run, loadErr := c.checkpoints.Load(id)
		if loadErr != nil || (!q.Since.IsZero() && run.Updated.Before(q.Since)) || !c.dashboardRunMatches(run, q.Filter) {
			continue
		}
		view := c.dashboardRunView(run)
		if !view.SpentUSDKnown {
			continue
		}
		total += view.SpentUSD
		known++
	}
	return total, known
}

func dashboardBucket(raw string) time.Duration {
	switch strings.TrimSpace(raw) {
	case "1m":
		return time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return 5 * time.Minute
	}
}

func summarizeDashboardMetrics(rows []metrics.Row) dashboardMetricSummary {
	var summary dashboardMetricSummary
	var durationTotal int64
	for _, row := range rows {
		summary.Rows++
		summary.Attempts += max(row.Attempts, 0)
		summary.Successes += max(row.Successes, 0)
		summary.Failures += max(row.Failures, 0)
		if row.Slowest.Milliseconds() > summary.DurationMaxMS {
			summary.DurationMaxMS = row.Slowest.Milliseconds()
		}
		if row.Successes > 0 {
			durationTotal += row.Mean.Milliseconds() * row.Successes
		}
		summary.Tokens += max(row.Tokens, 0)
	}
	if summary.Attempts > 0 {
		rate := float64(summary.Successes) * 100 / float64(summary.Attempts)
		summary.SuccessRate = &rate
	}
	if summary.Successes > 0 {
		summary.DurationMS = durationTotal / summary.Successes
	}
	return summary
}

func metricCursorKey(row metrics.Row) string {
	return strings.Join([]string{row.Capability, row.Implementation, row.Provider, row.Repository, row.ToolVersion}, "\x00")
}

type dashboardTrace struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	TypeName  string    `json:"type_name"`
	Kind      string    `json:"kind"`
	Objective string    `json:"objective"`
	Depth     int       `json:"depth"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Verdict   string    `json:"verdict"`
	Reason    string    `json:"reason,omitempty"`
	Attempt   int       `json:"attempt"`
	RetryOf   string    `json:"retry_of,omitempty"`
	Reviews   string    `json:"reviews,omitempty"`
	Swept     bool      `json:"swept"`
}

func (c *Core) dashboardTraces(q dashboard.Query) (any, error) {
	store, err := trace.Open(context.Background(), "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	if q.Limit <= 0 {
		q.Limit = c.settings.Dashboard.PageLimit
		if q.Limit <= 0 {
			q.Limit = 100
		}
	}
	// Fetch one extra row so the dashboard can issue a stable cursor without
	// exposing SQLite offsets. Trace ids are monotonic, and List's ordering is
	// newest first, so walking after the cursor remains valid when new traces
	// arrive between requests.
	f := trace.Filter{Limit: 1000}
	if q.Filter["id"] != "" {
		f.ID = q.Filter["id"]
	}
	f.TypeName = q.Filter["type"]
	if q.Filter["open"] == "true" {
		f.OpenOnly = true
	}
	if !q.Since.IsZero() {
		f.Since = q.Since
	}
	if raw := q.Filter["retry_of"]; raw != "" {
		f.RetryOf = raw
	}
	if raw := q.Filter["reviews"]; raw != "" {
		f.Reviews = raw
	}
	if raw := q.Filter["verdict"]; raw != "" {
		if verdict, parseErr := contract.ParseVerdict(raw); parseErr == nil {
			f.Verdict = verdict
		}
	}
	rows, err := store.List(context.Background(), f)
	if err != nil {
		return nil, err
	}
	start := traceCursorIndex(rows, q.Cursor)
	items := make([]dashboardTrace, 0, q.Limit)
	for _, row := range rows[start:] {
		if len(items) >= q.Limit {
			break
		}
		items = append(items, dashboardTrace{ID: row.ID, ParentID: row.ParentID, TypeName: row.TypeName, Kind: row.Kind.String(), Objective: safeDashboardText(row.Objective, 280), Depth: row.Depth, StartedAt: row.StartedAt, EndedAt: row.EndedAt, Verdict: row.Verdict.String(), Reason: safeDashboardText(row.Reason.String(), 240), Attempt: row.Attempt, RetryOf: row.RetryOf, Reviews: safeDashboardText(row.Reviews, 240), Swept: row.Swept})
	}
	next := ""
	if start+len(items) < len(rows) && len(items) > 0 {
		next = encodeCursor(items[len(items)-1].ID)
	}
	return dashboardPage{Items: items, NextCursor: next, Total: len(rows)}, nil
}

func traceCursorIndex(rows []trace.Row, raw string) int {
	if raw == "" {
		return 0
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || !strings.HasPrefix(string(body), "id:") {
		return 0
	}
	id := strings.TrimPrefix(string(body), "id:")
	for i, row := range rows {
		if row.ID == id {
			return i + 1
		}
	}
	return 0
}

type dashboardIncident struct {
	At             time.Time `json:"at"`
	Op             string    `json:"op"`
	Detail         string    `json:"detail"`
	RunID          string    `json:"run_id,omitempty"`
	Step           string    `json:"step,omitempty"`
	Capability     string    `json:"capability,omitempty"`
	Implementation string    `json:"implementation,omitempty"`
	Repository     string    `json:"repository,omitempty"`
	PID            int       `json:"pid"`
}

func (c *Core) dashboardIncidents(q dashboard.Query) (any, error) {
	read, err := c.Incidents()
	if err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		q.Limit = c.settings.Dashboard.PageLimit
		if q.Limit <= 0 {
			q.Limit = 100
		}
	}
	start := incidentCursorOffset(q.Cursor)
	matched, skipped := 0, 0
	items := make([]dashboardIncident, 0, q.Limit)
	hasMore := false
	for _, in := range read.Incidents {
		if !q.Since.IsZero() && in.At.Before(q.Since) {
			continue
		}
		if want := q.Filter["op"]; want != "" && in.Op != want {
			continue
		}
		if want := q.Filter["run_id"]; want != "" && in.RunID != want {
			continue
		}
		matched++
		if skipped < start {
			skipped++
			continue
		}
		if len(items) >= q.Limit {
			hasMore = true
			continue
		}
		items = append(items, dashboardIncident{At: in.At, Op: safeDashboardText(in.Op, 80), Detail: safeDashboardText(in.Detail, 280), RunID: safeDashboardText(in.RunID, 120), Step: safeDashboardText(in.Step, 120), Capability: safeDashboardText(in.Capability, 120), Implementation: safeDashboardText(in.Implementation, 120), Repository: safeDashboardProject(in.Repository), PID: in.PID})
	}
	next := ""
	if hasMore {
		next = encodeIncidentCursor(start + len(items))
	}
	return dashboardPage{Items: items, NextCursor: next, Total: matched}, nil
}

func encodeIncidentCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("offset:%d", offset)))
}

func incidentCursorOffset(raw string) int {
	if raw == "" {
		return 0
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || !strings.HasPrefix(string(body), "offset:") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(body), "offset:"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func safeDashboardText(raw string, limit int) string {
	raw = contract.RedactRaw(strings.TrimSpace(raw))
	if limit <= 0 {
		return ""
	}
	if len(raw) > limit {
		if limit <= len("…") {
			cut := limit
			for cut > 0 && !utf8.RuneStart(raw[cut]) {
				cut--
			}
			return raw[:cut]
		}
		cut := limit - len("…")
		for cut > 0 && !utf8.RuneStart(raw[cut]) {
			cut--
		}
		return raw[:cut] + "…"
	}
	return raw
}
func encodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("id:" + id))
}
func cursorIndex(ids []string, raw string) int {
	if raw == "" {
		return 0
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0
	}
	decoded := string(body)
	if strings.HasPrefix(decoded, "id:") {
		id := strings.TrimPrefix(decoded, "id:")
		for i, candidate := range ids {
			if candidate > id {
				return i
			}
		}
		return len(ids)
	}
	// Accept cursors generated by the first dashboard build so an operator can
	// refresh an already-open page during an upgrade without losing its place.
	n, err := strconv.Atoi(decoded)
	if err != nil || n < 0 || n > len(ids) {
		return 0
	}
	return n
}
