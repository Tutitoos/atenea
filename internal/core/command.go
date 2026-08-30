package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/clientconfig"
	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// CommandRequest is the closed, client-facing read-only command contract.
// Keeping this typed prevents chat clients from turning the command surface
// into an arbitrary shell or CLI passthrough.
type CommandRequest struct {
	Name           string `json:"name"`
	Format         string `json:"format,omitempty"`
	Capability     string `json:"capability,omitempty"`
	Implementation string `json:"implementation,omitempty"`
	Repository     string `json:"repository,omitempty"`
	ID             string `json:"id,omitempty"`
	Type           string `json:"type,omitempty"`
	Verdict        string `json:"verdict,omitempty"`
	Open           bool   `json:"open,omitempty"`
	Since          string `json:"since,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	All            bool   `json:"all,omitempty"`
	Client         string `json:"client,omitempty"`
	Profile        string `json:"profile,omitempty"`
}

// CommandResponse is the rendered result of a read-only Atenea command.
type CommandResponse struct {
	Command  string `json:"command"`
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	Data     any    `json:"data,omitempty"`
	Markdown string `json:"markdown"`
}

const commandTool = "atenea.command"

var readOnlyCommands = []string{
	"help", "status", "metrics", "traces", "catalog", "doctor",
	"detect", "incidents", "floor", "config", "intent",
}

// Command answers the small read-only surface intended for chat shortcuts.
// It deliberately does not call the CLI handlers: the service is the single
// source of truth for connected desktop clients.
func (c *Core) Command(ctx context.Context, req CommandRequest) (CommandResponse, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" || name == "health" {
		name = "status"
	}
	if name == "providers" {
		name = "catalog"
	}
	if !containsCommand(name) {
		return CommandResponse{}, contract.Fail(contract.FailureInvalidInput,
			"unknown atenea command %q; use /atenea help", req.Name)
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "json" && format != "text" {
		return CommandResponse{}, contract.Fail(contract.FailureInvalidInput,
			"unsupported command format %q; use markdown, json or text", req.Format)
	}
	if req.Limit < 0 {
		return CommandResponse{}, contract.Fail(contract.FailureInvalidInput,
			"limit must not be negative")
	}

	response := CommandResponse{Command: name, Status: "ok"}
	switch name {
	case "help":
		response.Summary = "Available read-only Atenea commands"
		response.Data = map[string]any{"commands": readOnlyCommands}
	case "status":
		response.Summary = "Atenea service status"
		response.Data = c.Status()
	case "metrics":
		rows, err := c.Measurements(throughZero())
		if err != nil {
			return CommandResponse{}, err
		}
		filtered := make([]metrics.Row, 0, len(rows))
		for _, row := range rows {
			if req.Capability != "" && row.Capability != req.Capability ||
				req.Implementation != "" && row.Implementation != req.Implementation ||
				req.Repository != "" && row.Repository != req.Repository {
				continue
			}
			filtered = append(filtered, row)
		}
		response.Summary = fmt.Sprintf("%d measurement row(s)", len(filtered))
		response.Data = filtered
	case "traces":
		store, err := trace.Open(ctx, trace.DefaultPath())
		if err != nil {
			return CommandResponse{}, err
		}
		defer func() { _ = store.Close() }()
		filter := trace.Filter{ID: req.ID, TypeName: req.Type, OpenOnly: req.Open, Limit: req.Limit}
		if req.Verdict != "" {
			verdict, err := contract.ParseVerdict(req.Verdict)
			if err != nil {
				return CommandResponse{}, err
			}
			filter.Verdict = verdict
		}
		if req.Since != "" {
			window, err := time.ParseDuration(req.Since)
			if err != nil || window <= 0 {
				return CommandResponse{}, contract.Fail(contract.FailureInvalidInput,
					"since must be a positive duration, got %q", req.Since)
			}
			filter.Since = time.Now().Add(-window)
		}
		rows, err := store.List(ctx, filter)
		if err != nil {
			return CommandResponse{}, err
		}
		response.Summary = fmt.Sprintf("%d trace row(s)", len(rows))
		response.Data = rows
	case "catalog":
		response.Summary = "Atenea capability catalog"
		response.Data = map[string]any{
			"capabilities": c.Registry().Capabilities(),
			"repositories": c.Registry().Repositories(),
		}
	case "detect":
		detection, err := c.Detect(ctx, req.Repository)
		if err != nil {
			return CommandResponse{}, err
		}
		response.Summary = "Provider and index detection"
		response.Data = detection
	case "incidents":
		book, err := c.Incidents()
		if err != nil {
			return CommandResponse{}, err
		}
		if !req.All {
			book.Incidents = book.New()
		}
		response.Summary = fmt.Sprintf("%d incident(s)", len(book.Incidents))
		response.Data = book
	case "floor":
		store, err := floor.Open("")
		if err != nil {
			return CommandResponse{}, err
		}
		rows, err := store.List()
		if err != nil {
			return CommandResponse{}, err
		}
		response.Summary = fmt.Sprintf("%d floor measurement(s)", len(rows))
		response.Data = rows
	case "config":
		settings := c.Settings()
		effects := make([]string, 0, len(settings.Orchestrator.ClientEffects))
		for _, effect := range settings.Orchestrator.ClientEffects {
			effects = append(effects, effect.String())
		}
		response.Summary = "Effective settings summary"
		response.Data = map[string]any{
			"source":                     settings.Source,
			"contract":                   settings.Contract,
			"client_effects":             effects,
			"client_denied_capabilities": settings.Orchestrator.ClientDeniedCapabilities,
			"desktop_scope": map[string]any{
				"applications":    len(settings.Desktop.Applications),
				"denied":          len(settings.Desktop.Denied),
				"look_then_act":   settings.Desktop.LookThenAct,
				"visual_feedback": settings.Desktop.VisualFeedback,
			},
			"repositories": len(settings.Repositories),
			"capabilities": len(settings.Capabilities),
		}
	case "doctor":
		client := req.Client
		if client == "" {
			client = "claude"
		}
		response.Summary = "MCP compatibility telemetry"
		response.Data = map[string]any{
			"client":    client,
			"profile":   req.Profile,
			"telemetry": ReadCompatibilitySummaryFor(client, req.Profile),
		}
		for _, runner := range c.runners {
			candidate := runner
			if unwrapped, ok := runner.(interface{ Unwrap() contract.Runner }); ok {
				candidate = unwrapped.Unwrap()
			}
			if health, ok := candidate.(interface {
				Health(context.Context) (map[string]any, error)
			}); ok {
				if desktopHealth, err := health.Health(ctx); err == nil {
					response.Data.(map[string]any)["desktop"] = desktopHealth
				} else {
					response.Data.(map[string]any)["desktop"] = map[string]any{
						"status": "degraded", "error": err.Error(),
					}
				}
			}
		}
	case "intent":
		if req.Repository == "" {
			return CommandResponse{}, contract.Fail(contract.FailureInvalidInput,
				"intent requires repository")
		}
		repo, err := c.Registry().Repository(req.Repository)
		if err != nil {
			return CommandResponse{}, err
		}
		reading, err := clientconfig.Read(repo.Path)
		if err != nil {
			return CommandResponse{}, err
		}
		response.Summary = fmt.Sprintf("Client declarations for %s", repo.ID)
		response.Data = reading
	}
	response.Markdown = commandMarkdown(response)
	if format == "text" {
		response.Markdown = commandText(response.Markdown)
	}
	return response, nil
}

// throughZero keeps the command independent from wall-clock filtering while
// still using Core.Measurements' existing read-only API.
func throughZero() (zero time.Time) { return }

func containsCommand(name string) bool {
	for _, candidate := range readOnlyCommands {
		if candidate == name {
			return true
		}
	}
	return false
}

func commandMarkdown(response CommandResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Atenea: %s\n\n", response.Command)
	fmt.Fprintf(&b, "**Estado:** `%s`  \n%s\n\n", response.Status, response.Summary)
	switch data := response.Data.(type) {
	case []string:
		for _, item := range data {
			fmt.Fprintf(&b, "- `/atenea %s`\n", item)
		}
	case Status:
		fmt.Fprintf(&b, "- **Luz:** `%s`\n- **Versión:** `%s`\n- **Rol:** `%s`\n- **Repositorios:** %d\n- **Capacidades:** %d\n",
			data.Light.String(), data.Version, data.Role, len(data.Repositories), len(data.Capabilities))
		if len(data.Orchestrator.ClientFloor) > 0 {
			fmt.Fprintf(&b, "- **Permisos de cliente:** `%s`\n", strings.Join(data.Orchestrator.ClientFloor, "`, `"))
		}
	case []metrics.Row:
		if len(data) == 0 {
			b.WriteString("No hay mediciones para los filtros indicados.\n")
			break
		}
		b.WriteString("| Capacidad | Implementación | Proveedor | Repositorio | OK | Error |\n")
		b.WriteString("|---|---|---|---|---:|---:|\n")
		for _, row := range data {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %d |\n", md(row.Capability), md(row.Implementation), md(row.Provider), md(row.Repository), row.Successes, row.Failures)
		}
	case map[string]any:
		if commands, ok := data["commands"].([]string); ok {
			for _, item := range commands {
				fmt.Fprintf(&b, "- `/atenea %s`\n", item)
			}
			break
		}
		if capabilities, ok := data["capabilities"].([]contract.Capability); ok {
			for _, capability := range capabilities {
				fmt.Fprintf(&b, "- **`%s`**: %s\n", capability.ID, md(capability.Summary))
			}
			break
		}
		encoded, _ := json.MarshalIndent(data, "", "  ")
		fmt.Fprintf(&b, "```json\n%s\n```\n", encoded)
	case []trace.Row:
		b.WriteString("| Started | Type | Verdict | Status | Objective |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, row := range data {
			state := row.Verdict.String()
			if row.Open() {
				state = "open"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", row.StartedAt.Local().Format("2006-01-02 15:04:05"), md(row.TypeName), md(row.Verdict.String()), state, md(row.Objective))
		}
	case []floor.Measurement:
		b.WriteString("| Repository | Agent | Model | USD | Prefix tokens |\n")
		b.WriteString("|---|---|---|---:|---:|\n")
		for _, row := range data {
			fmt.Fprintf(&b, "| %s | %s | %s | %.4f | %d |\n", md(row.Repository), md(row.Agent), md(row.Model), row.USD, row.Prefix())
		}
	case notebook.Read:
		if len(data.Incidents) == 0 {
			b.WriteString("No hay incidencias nuevas.\n")
			break
		}
		b.WriteString("| At | Operacion | Detalle |\n")
		b.WriteString("|---|---|---|\n")
		for _, incident := range data.Incidents {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", incident.At.Local().Format("2006-01-02 15:04:05"), md(incident.Op), md(incident.Detail))
		}
	default:
		encoded, _ := json.MarshalIndent(data, "", "  ")
		fmt.Fprintf(&b, "```json\n%s\n```\n", encoded)
	}
	return b.String()
}

func md(value string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(value)
}

func commandText(markdown string) string {
	return strings.NewReplacer(
		"## ", "",
		"### ", "",
		"**", "",
		"`", "",
		"\\|", "|",
	).Replace(markdown)
}

// CommandJSON is used by adapters that need a stable machine-readable body.
func CommandJSON(response CommandResponse) string {
	body, _ := json.Marshal(response)
	return string(body)
}
