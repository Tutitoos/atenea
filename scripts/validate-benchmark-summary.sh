#!/usr/bin/env bash
set -euo pipefail

summary="${1:-benchmarks/runs/latest/summary.json}"
go run ./cmd/atenea-benchmark --validate-only --input "$summary"
