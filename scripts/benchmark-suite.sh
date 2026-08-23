#!/usr/bin/env bash
set -euo pipefail

profile=${1:-quick}
runs=${ATENEA_BENCHMARK_RUNS:-3}
if [[ "$profile" == "qualification" || "$profile" == "stress" ]]; then
	runs=${ATENEA_BENCHMARK_RUNS:-10}
fi

go run ./cmd/atenea-benchmark --profile "$profile" --benchmark-runs "$runs"
