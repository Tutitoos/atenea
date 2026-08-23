#!/usr/bin/env bash
set -euo pipefail

summary=${1:-benchmarks/runs/latest/summary.json}
test -f "$summary" || {
	echo "benchmark summary not found: $summary" >&2
	exit 1
}
go run ./cmd/atenea-benchmark --render-only --input "$summary"
