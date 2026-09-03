package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/Tutitoos/atenea/internal/dashboard"
	"github.com/Tutitoos/atenea/internal/observability"
)

func main() {
	port := flag.String("port", "8799", "fixture port")
	flag.Parse()
	hub := observability.New(32)
	provider := dashboard.Provider{
		Snapshot: func() (any, error) {
			return map[string]any{"light": "green", "active_sessions": 1, "active_runs": 1}, nil
		},
		Overview: func(dashboard.Query) (any, error) {
			return map[string]any{"snapshot": map[string]any{"light": "green"}, "sessions": map[string]any{"total": 1, "active": 1}, "runs": map[string]any{"total": 2, "active": 1}, "stats": map[string]any{"success_rate": 100, "duration_ms": 920, "coverage": 100}}, nil
		},
		Sessions: func(dashboard.Query) (any, error) {
			return map[string]any{"items": []any{map[string]any{"id": "fixture-session", "name": "Validación dashboard", "name_basis": "provided", "active": true, "state": "active", "primary_project": "atenea", "projects": []string{"atenea"}, "origin": map[string]any{"client": "Playwright", "surface": "e2e", "transport": "http"}, "stats": map[string]any{"runs": 2, "success": 100, "coverage": 100}}}, "total": 1}, nil
		},
		Session: func(string) (any, error) {
			return map[string]any{"id": "fixture-session", "name": "Validación dashboard", "active": true, "state": "active", "primary_project": "atenea", "projects": []string{"atenea"}, "origin": map[string]any{"client": "Playwright", "surface": "e2e", "transport": "http"}, "stats": map[string]any{"runs": 2, "duration_ms": 920, "tokens": 256, "retries": 0}, "runs": []any{}}, nil
		},
		Runs: func(dashboard.Query) (any, error) {
			return map[string]any{"items": []any{map[string]any{"id": "fixture-run", "session": "fixture-session", "task": "Validación dashboard", "project": "atenea", "state": "green", "duration_ms": 920}}, "total": 1}, nil
		},
		Run: func(string) (any, error) {
			return map[string]any{"id": "fixture-run", "session": "fixture-session", "task": "Validación dashboard", "project": "atenea", "state": "green", "duration_ms": 920, "steps": []any{}}, nil
		},
		Metrics: func(dashboard.Query) (any, error) {
			return map[string]any{"items": []any{map[string]any{"bucket": "now", "success_rate": 100}}, "success_rate": 100, "duration_ms": 920}, nil
		},
		Incidents: func(dashboard.Query) (any, error) { return map[string]any{"items": []any{}}, nil },
		Catalog: func() (any, error) {
			return map[string]any{"items": []any{map[string]any{"id": "code.search", "name": "code.search", "status": "available"}}}, nil
		},
		Events: hub,
	}
	server, err := dashboard.NewServer(dashboard.Config{Enabled: true, Listeners: []dashboard.Listener{{Addr: "127.0.0.1:" + *port, Mode: "loopback"}}}, provider)
	if err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Start(ctx); err != nil {
		panic(err)
	}
	<-ctx.Done()
}
