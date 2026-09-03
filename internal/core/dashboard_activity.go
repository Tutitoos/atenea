package core

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/dashboard"
	"github.com/Tutitoos/atenea/internal/observability"
)

// dashboardStats keeps counters and measurement coverage separate. A missing
// token or price is never converted to zero by this projection.
type dashboardStats struct {
	Runs              int      `json:"runs"`
	ActiveRuns        int      `json:"active_runs"`
	Steps             int      `json:"steps"`
	Successes         int      `json:"successes"`
	Failures          int      `json:"failures"`
	SuccessRate       *float64 `json:"success_rate,omitempty"`
	Retries           int      `json:"retries"`
	Reviews           int      `json:"reviews"`
	DurationMS        int64    `json:"duration_ms,omitempty"`
	Tokens            int64    `json:"tokens,omitempty"`
	TokensKnownRuns   int      `json:"tokens_known_runs"`
	SpentUSD          float64  `json:"spent_usd,omitempty"`
	SpentUSDKnownRuns int      `json:"spent_usd_known_runs"`
	PeakRSS           int64    `json:"peak_rss,omitempty"`
	RSSKnownSteps     int      `json:"rss_known_steps"`
	MeasuredSteps     int      `json:"measured_steps"`
	UnmeasuredSteps   int      `json:"unmeasured_steps"`
	Coverage          float64  `json:"coverage,omitempty"`
}

type dashboardSession struct {
	ID                           string          `json:"id"`
	Name                         string          `json:"name"`
	NameBasis                    string          `json:"name_basis"`
	Client                       string          `json:"client,omitempty"`
	PrimaryProject               string          `json:"primary_project,omitempty"`
	Origin                       dashboardOrigin `json:"origin"`
	ExternalConversationObserved bool            `json:"external_conversation_observed"`
	State                        string          `json:"state"`
	Opened                       time.Time       `json:"opened,omitempty"`
	LastActivity                 time.Time       `json:"last_activity,omitempty"`
	TimeBasis                    string          `json:"time_basis"`
	Active                       bool            `json:"active"`
	Projects                     []string        `json:"projects,omitempty"`
	Capabilities                 []string        `json:"capabilities,omitempty"`
	Implementations              []string        `json:"implementations,omitempty"`
	Providers                    []string        `json:"providers,omitempty"`
	Tools                        []string        `json:"tools,omitempty"`
	Stats                        dashboardStats  `json:"stats"`
}

type dashboardOrigin struct {
	Client    string `json:"client,omitempty"`
	Surface   string `json:"surface,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type dashboardSessionDetail struct {
	dashboardSession
	Runs     []dashboardRun      `json:"runs"`
	Timeline []dashboardTimeline `json:"timeline"`
	Graph    dashboardGraph      `json:"graph"`
}

type dashboardTimeline struct {
	ID             string    `json:"id"`
	At             time.Time `json:"at"`
	Kind           string    `json:"kind"`
	SessionID      string    `json:"session_id,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	StepID         string    `json:"step_id,omitempty"`
	Capability     string    `json:"capability,omitempty"`
	Implementation string    `json:"implementation,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Repository     string    `json:"repository,omitempty"`
	State          string    `json:"state,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	DurationMS     int64     `json:"duration_ms,omitempty"`
	Tokens         int64     `json:"tokens,omitempty"`
	Source         string    `json:"source"`
}

type dashboardGraph struct {
	Nodes []dashboardGraphNode `json:"nodes"`
	Edges []dashboardGraphEdge `json:"edges"`
}

type dashboardGraphNode struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	State       string `json:"state,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	StepID      string `json:"step_id,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Tool        string `json:"tool,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	Tokens      int64  `json:"tokens,omitempty"`
	TokensKnown bool   `json:"tokens_known"`
}

type dashboardGraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type dashboardOverview struct {
	At             time.Time          `json:"at"`
	Range          string             `json:"range"`
	Snapshot       any                `json:"snapshot"`
	Sessions       []dashboardSession `json:"sessions"`
	Runs           dashboardCounts    `json:"runs"`
	ActiveSessions int                `json:"active_sessions"`
	Stats          dashboardStats     `json:"stats"`
	Trends         map[string]any     `json:"trends"`
}

type dashboardCounts struct {
	Total  int `json:"total"`
	Active int `json:"active"`
}

func (c *Core) dashboardOverview(q dashboard.Query) (any, error) {
	q.Since = dashboardSince(q)
	sessions, err := c.dashboardSessionValues(q)
	if err != nil {
		return nil, err
	}
	stats := dashboardStats{}
	for _, session := range sessions {
		stats = addDashboardStats(stats, session.Stats)
	}
	setDashboardSuccessRate(&stats)
	rangeName := q.Filter["range"]
	if rangeName == "" {
		rangeName = "24h"
	}
	return dashboardOverview{At: time.Now().UTC(), Range: rangeName, Snapshot: c.dashboardStatus(), Sessions: sessions, Runs: dashboardCounts{Total: stats.Runs, Active: stats.ActiveRuns}, ActiveSessions: countActiveDashboardSessions(sessions), Stats: stats, Trends: map[string]any{"available": false, "reason": "comparison requires retained attempt detail"}}, nil
}

func countActiveDashboardSessions(items []dashboardSession) int {
	count := 0
	for _, item := range items {
		if item.Active {
			count++
		}
	}
	return count
}

func (c *Core) dashboardSessions(q dashboard.Query) (any, error) {
	q.Since = dashboardSince(q)
	items, err := c.dashboardSessionValues(q)
	if err != nil {
		return nil, err
	}
	start := dashboardIDCursorIndexSession(items, q.Cursor)
	if start > len(items) {
		start = len(items)
	}
	end := start + q.Limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = encodeCursor(items[end-1].ID)
	}
	return dashboardPage{Items: items[start:end], NextCursor: next, Total: len(items)}, nil
}

func dashboardIDCursorIndexSession(items []dashboardSession, raw string) int {
	if raw == "" {
		return 0
	}
	body, err := base64DecodeCursor(raw)
	if err != nil || !strings.HasPrefix(body, "id:") {
		return 0
	}
	id := strings.TrimPrefix(body, "id:")
	for i, item := range items {
		if item.ID == id {
			return i + 1
		}
	}
	return 0
}

func (c *Core) dashboardSession(id string) (any, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\\`) {
		return nil, fmt.Errorf("invalid session id")
	}
	items, err := c.dashboardSessionValues(dashboard.Query{Limit: 100000, Filter: map[string]string{"range": "all"}})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		detail := dashboardSessionDetail{dashboardSession: item, Runs: []dashboardRun{}, Timeline: []dashboardTimeline{}, Graph: dashboardGraph{Nodes: []dashboardGraphNode{{ID: "session:" + id, Kind: "session", Label: cmp.Or(item.Name, id), State: item.State}}, Edges: []dashboardGraphEdge{}}}
		ids, listErr := c.checkpoints.List()
		if listErr != nil {
			return nil, listErr
		}
		for _, runID := range ids {
			run, loadErr := c.checkpoints.Load(runID)
			if loadErr != nil || run.Session != id {
				continue
			}
			runView := c.dashboardRunView(run)
			detail.Runs = append(detail.Runs, runView)
			detail.Graph = appendRunGraph(detail.Graph, runView)
			for _, step := range runView.Steps {
				detail.Timeline = append(detail.Timeline, dashboardTimeline{ID: "step:" + run.ID + "/" + step.ID, At: step.Ended, Kind: "step.closed", SessionID: id, RunID: run.ID, StepID: step.ID, Capability: step.Capability, Implementation: step.Implementation, Provider: step.Provider, Repository: step.Repository, State: step.Verdict, DurationMS: step.DurationMS, Tokens: dashboardStepTokens(step), Source: "checkpoint"})
			}
		}
		for _, event := range c.eventsForSession(id) {
			detail.Timeline = append(detail.Timeline, timelineFromEvent(event))
		}
		sort.SliceStable(detail.Runs, func(i, j int) bool { return detail.Runs[i].Updated.After(detail.Runs[j].Updated) })
		sort.SliceStable(detail.Timeline, func(i, j int) bool { return detail.Timeline[i].At.Before(detail.Timeline[j].At) })
		return detail, nil
	}
	return nil, fmt.Errorf("session not found")
}

func appendRunGraph(graph dashboardGraph, run dashboardRun) dashboardGraph {
	runNode := "run:" + run.ID
	graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: runNode, Kind: "run", Label: run.ID, RunID: run.ID, State: run.Verdict})
	graph.Edges = append(graph.Edges, dashboardGraphEdge{From: "session:" + run.SessionID, To: runNode, Kind: "contains"})
	terminals := make([]string, 0, len(run.Steps))
	for _, step := range run.Steps {
		stepNode := "step:" + run.ID + "/" + step.ID
		graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: stepNode, Kind: "step", Label: cmp.Or(step.Capability, step.ID), RunID: run.ID, StepID: step.ID, State: step.Verdict, DurationMS: step.DurationMS, Tokens: dashboardStepTokens(step), TokensKnown: step.TokensKnown})
		graph.Edges = append(graph.Edges, dashboardGraphEdge{From: runNode, To: stepNode, Kind: "contains"})
		parentNode := stepNode
		if step.Funnel.State == "kept" {
			selectorNode := stepNode + ":selector"
			graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: selectorNode, Kind: "selector", Label: "constraints → reach → health → cost", RunID: run.ID, StepID: step.ID, State: "completed"})
			graph.Edges = append(graph.Edges, dashboardGraphEdge{From: stepNode, To: selectorNode, Kind: "selected_by"})
			parentNode = selectorNode
		}
		if step.Implementation != "" {
			toolNode := stepNode + ":tool"
			graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: toolNode, Kind: "tool", Label: step.Implementation, Tool: step.Implementation, RunID: run.ID, StepID: step.ID, State: step.Verdict, DurationMS: step.DurationMS, Tokens: dashboardStepTokens(step), TokensKnown: step.TokensKnown})
			graph.Edges = append(graph.Edges, dashboardGraphEdge{From: stepNode, To: toolNode, Kind: "uses"})
			parentNode = toolNode
		}
		if step.Provider != "" {
			providerNode := stepNode + ":provider"
			graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: providerNode, Kind: "provider", Label: step.Provider, Provider: step.Provider, Tool: step.Implementation, RunID: run.ID, StepID: step.ID, State: step.Verdict, DurationMS: step.DurationMS, Tokens: dashboardStepTokens(step), TokensKnown: step.TokensKnown})
			graph.Edges = append(graph.Edges, dashboardGraphEdge{From: parentNode, To: providerNode, Kind: "executed_by"})
			parentNode = providerNode
		}
		if step.Review != "" {
			reviewNode := stepNode + ":review"
			graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: reviewNode, Kind: "review", Label: "Review", RunID: run.ID, StepID: step.ID, State: step.Review})
			graph.Edges = append(graph.Edges, dashboardGraphEdge{From: parentNode, To: reviewNode, Kind: "reviewed"})
			parentNode = reviewNode
		}
		if step.Attempt > 1 {
			retryNode := stepNode + ":retry"
			graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: retryNode, Kind: "retry", Label: fmt.Sprintf("Retry #%d", step.Attempt-1), RunID: run.ID, StepID: step.ID, State: "retry"})
			graph.Edges = append(graph.Edges, dashboardGraphEdge{From: parentNode, To: retryNode, Kind: "retry"})
			parentNode = retryNode
		}
		terminals = append(terminals, parentNode)
	}
	closeNode := "run:" + run.ID + ":close"
	graph.Nodes = append(graph.Nodes, dashboardGraphNode{ID: closeNode, Kind: "close", Label: "Cierre", RunID: run.ID, State: run.Verdict})
	if len(terminals) == 0 {
		graph.Edges = append(graph.Edges, dashboardGraphEdge{From: runNode, To: closeNode, Kind: "closed"})
	} else {
		for _, terminal := range terminals {
			graph.Edges = append(graph.Edges, dashboardGraphEdge{From: terminal, To: closeNode, Kind: "closed"})
		}
	}
	return graph
}

func (c *Core) dashboardSessionValues(q dashboard.Query) ([]dashboardSession, error) {
	byID := map[string]*dashboardSession{}
	active := map[string]*Session{}
	for _, live := range c.Sessions() {
		active[live.ID()] = live
		name, basis, project, origin, external := live.DashboardMetadata()
		client := safeDashboardText(live.Client(), 80)
		item := &dashboardSession{ID: live.ID(), Name: safeDashboardText(name, 120), NameBasis: basis, Client: client, PrimaryProject: project, Origin: dashboardOrigin{Client: client, Surface: origin.Surface, Transport: origin.Transport}, ExternalConversationObserved: external, State: "active", Opened: live.Opened(), LastActivity: live.Opened(), Active: true, TimeBasis: "exact"}
		if project != "" {
			item.Projects = []string{project}
		}
		byID[live.ID()] = item
	}
	ids, err := c.checkpoints.List()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		run, loadErr := c.checkpoints.Load(id)
		if loadErr != nil || run.Session == "" {
			continue
		}
		if !q.Since.IsZero() && run.Updated.Before(q.Since) {
			continue
		}
		item := byID[run.Session]
		if item == nil {
			item = &dashboardSession{ID: run.Session, State: "closed", TimeBasis: "observed"}
			byID[run.Session] = item
		}
		if item.Client == "" {
			item.Client = safeDashboardText(run.SessionClient, 80)
			item.Origin.Client = item.Client
		}
		applyCheckpointSessionMetadata(item, run)
		if item.Opened.IsZero() || (!run.Started.IsZero() && run.Started.Before(item.Opened)) {
			item.Opened = run.Started
		}
		if run.Updated.After(item.LastActivity) {
			item.LastActivity = run.Updated
		}
		if item.State != "active" {
			if run.Closed {
				item.State = "closed"
			} else {
				item.State = "running"
			}
		}
		accumulateRunSession(item, c.dashboardRunView(run))
	}
	items := make([]dashboardSession, 0, len(byID))
	for _, item := range byID {
		item.Active = active[item.ID] != nil
		if item.Active {
			item.State = "active"
			item.TimeBasis = "exact"
		}
		finalizeSession(item)
		if !q.Since.IsZero() && !item.Active && item.LastActivity.Before(q.Since) {
			continue
		}
		if !sessionMatches(*item, q.Filter) {
			continue
		}
		items = append(items, *item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].LastActivity.Equal(items[j].LastActivity) {
			return items[i].ID > items[j].ID
		}
		return items[i].LastActivity.After(items[j].LastActivity)
	})
	return items, nil
}

func dashboardSince(q dashboard.Query) time.Time {
	if !q.Since.IsZero() {
		return q.Since
	}
	rangeName := q.Filter["range"]
	var d time.Duration
	switch rangeName {
	case "all":
		return time.Time{}
	case "1h":
		d = time.Hour
	case "7d":
		d = 7 * 24 * time.Hour
	case "30d":
		d = 30 * 24 * time.Hour
	default:
		d = 24 * time.Hour
	}
	return time.Now().UTC().Add(-d)
}

func accumulateRunSession(item *dashboardSession, run dashboardRun) {
	item.Stats.Runs++
	if !run.Closed {
		item.Stats.ActiveRuns++
	}
	if !run.Started.IsZero() && !run.Updated.IsZero() && run.Updated.After(run.Started) {
		item.Stats.DurationMS += run.Updated.Sub(run.Started).Milliseconds()
	}
	if run.TokensKnown {
		item.Stats.Tokens += run.Tokens
		item.Stats.TokensKnownRuns++
	}
	if run.SpentUSDKnown {
		item.Stats.SpentUSD += run.SpentUSD
		item.Stats.SpentUSDKnownRuns++
	}
	addUnique := func(dst *[]string, value string) {
		if value != "" && !slices.Contains(*dst, value) {
			*dst = append(*dst, value)
		}
	}
	for _, repo := range run.Projects {
		addUnique(&item.Projects, repo)
	}
	for _, step := range run.Steps {
		item.Stats.Steps++
		if step.Verdict == "ok" {
			item.Stats.Successes++
		} else if step.Verdict != "" {
			item.Stats.Failures++
		}
		if step.Attempt > 1 {
			item.Stats.Retries++
		}
		if step.Review != "" {
			item.Stats.Reviews++
		}
		if step.TokensKnown {
			item.Stats.MeasuredSteps++
		} else {
			item.Stats.UnmeasuredSteps++
		}
		if step.RSSKnown {
			item.Stats.RSSKnownSteps++
			if step.PeakRSS > item.Stats.PeakRSS {
				item.Stats.PeakRSS = step.PeakRSS
			}
		}
		addUnique(&item.Capabilities, step.Capability)
		addUnique(&item.Implementations, step.Implementation)
		addUnique(&item.Providers, step.Provider)
		addUnique(&item.Tools, step.Implementation)
	}
}

func addDashboardStats(a, b dashboardStats) dashboardStats {
	a.Runs += b.Runs
	a.ActiveRuns += b.ActiveRuns
	a.Steps += b.Steps
	a.Successes += b.Successes
	a.Failures += b.Failures
	a.Retries += b.Retries
	a.Reviews += b.Reviews
	a.DurationMS += b.DurationMS
	a.Tokens += b.Tokens
	a.TokensKnownRuns += b.TokensKnownRuns
	a.SpentUSD += b.SpentUSD
	a.SpentUSDKnownRuns += b.SpentUSDKnownRuns
	a.PeakRSS = max(a.PeakRSS, b.PeakRSS)
	a.RSSKnownSteps += b.RSSKnownSteps
	a.MeasuredSteps += b.MeasuredSteps
	a.UnmeasuredSteps += b.UnmeasuredSteps
	return a
}

func finalizeSession(item *dashboardSession) {
	if item.Name == "" {
		item.Name = "Sesión " + shortSessionID(item.ID)
		item.NameBasis = "unknown"
	}
	if item.PrimaryProject == "" && len(item.Projects) > 0 {
		item.PrimaryProject = item.Projects[0]
	}
	if item.Stats.Steps > 0 {
		item.Stats.Coverage = float64(item.Stats.MeasuredSteps) / float64(item.Stats.Steps)
	}
	setDashboardSuccessRate(&item.Stats)
	sort.Strings(item.Projects)
	sort.Strings(item.Capabilities)
	sort.Strings(item.Implementations)
	sort.Strings(item.Providers)
	sort.Strings(item.Tools)
}

func setDashboardSuccessRate(stats *dashboardStats) {
	if stats == nil {
		return
	}
	denominator := stats.Successes + stats.Failures
	if denominator == 0 {
		stats.SuccessRate = nil
		return
	}
	rate := float64(stats.Successes) * 100 / float64(denominator)
	stats.SuccessRate = &rate
}

func sessionMatches(item dashboardSession, filter map[string]string) bool {
	if want := filter["session"]; want != "" && item.ID != want {
		return false
	}
	if want := filter["state"]; want != "" && item.State != want {
		return false
	}
	if want := filter["client"]; want != "" && item.Client != want {
		return false
	}
	if want := filter["project"]; want != "" && !slices.Contains(item.Projects, want) {
		return false
	}
	if want := filter["provider"]; want != "" && !slices.Contains(item.Providers, want) {
		return false
	}
	if want := filter["origin"]; want != "" && item.Origin.Surface != want && item.Origin.Transport != want {
		return false
	}
	if want := filter["capability"]; want != "" && !slices.Contains(item.Capabilities, want) {
		return false
	}
	if want := filter["implementation"]; want != "" && !slices.Contains(item.Implementations, want) {
		return false
	}
	if want := filter["tool"]; want != "" && !slices.Contains(item.Tools, want) {
		return false
	}
	if want := strings.ToLower(strings.TrimSpace(filter["q"])); want != "" {
		values := []string{item.Name, item.ID, item.Client, item.PrimaryProject, item.Origin.Surface, item.Origin.Transport}
		values = append(values, item.Projects...)
		values = append(values, item.Capabilities...)
		values = append(values, item.Implementations...)
		values = append(values, item.Providers...)
		values = append(values, item.Tools...)
		haystack := strings.ToLower(strings.Join(values, " "))
		if !strings.Contains(haystack, want) {
			return false
		}
	}
	return true
}

func applyCheckpointSessionMetadata(item *dashboardSession, run checkpoint.Run) {
	name := safeDashboardText(run.SessionName, 120)
	basis := run.SessionNameBasis
	if name != "" && (item.Name == "" || basis == "provided") {
		item.Name, item.NameBasis = name, cmp.Or(basis, "derived")
	} else if item.Name == "" {
		item.Name, item.NameBasis = safeDashboardText(run.Task, 120), "derived"
	}
	if item.PrimaryProject == "" {
		item.PrimaryProject = safeDashboardProject(run.SessionPrimaryProject)
	}
	if item.Origin.Surface == "" {
		item.Origin.Surface = safeDashboardText(run.SessionOriginSurface, 40)
	}
	if item.Origin.Transport == "" {
		item.Origin.Transport = safeDashboardText(run.SessionOriginTransport, 40)
	}
	item.ExternalConversationObserved = item.ExternalConversationObserved || run.SessionExternalObserved
}

func safeDashboardProject(raw string) string {
	value := safeDashboardText(raw, 80)
	if value == "" || strings.HasPrefix(value, "~") || strings.ContainsAny(value, `/\\`) || strings.Contains(value, ":") {
		return ""
	}
	return value
}

func safeDashboardProjects(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = safeDashboardProject(value); value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

func (c *Core) eventsForSession(id string) []observability.Event {
	if c.events == nil {
		return nil
	}
	all := c.events.Events(0)
	out := make([]observability.Event, 0)
	for _, event := range all {
		if event.SessionID == id {
			out = append(out, event)
		}
	}
	return out
}

func timelineFromEvent(event observability.Event) dashboardTimeline {
	return dashboardTimeline{ID: fmt.Sprintf("event:%d", event.Seq), At: event.At, Kind: event.Kind, SessionID: event.SessionID, RunID: event.RunID, StepID: event.StepID, Capability: event.Capability, Implementation: event.Implementation, Provider: event.Provider, Repository: event.Repository, State: event.State, Reason: safeDashboardText(event.Reason, 240), DurationMS: event.DurationMS, Tokens: event.Tokens, Source: "live"}
}

func base64DecodeCursor(raw string) (string, error) {
	body, err := base64.RawURLEncoding.DecodeString(raw)
	return string(body), err
}
