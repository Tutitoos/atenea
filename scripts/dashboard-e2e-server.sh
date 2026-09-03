#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
go run ./internal/dashboard/e2e_fixture --port 8799 &
fixture_pid=$!
cleanup() { kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
for _ in $(seq 1 50); do
	if curl -fsS http://127.0.0.1:8799/healthz >/dev/null 2>&1; then break; fi
	sleep 0.1
done
ATENEA_DASHBOARD_API=http://127.0.0.1:8799 bun run --cwd dashboard dev --host 127.0.0.1 --port 5173
