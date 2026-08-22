#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

output="$(go test ./internal/orchestrator -run '^$' -bench '^BenchmarkRunPlanConcurrentMediumDAG$' -benchtime=1x -count=1 -benchmem)"
line="$(awk '$1 ~ /^BenchmarkRunPlanConcurrentMediumDAG-/ { print; exit }' <<<"$output")"
test -n "$line" || {
	echo "load benchmark did not produce a result" >&2
	exit 1
}

ns="$(awk '{ for (i = 1; i <= NF; i++) if ($i == "ns/op") print $(i - 1) }' <<<"$line")"
bytes="$(awk '{ for (i = 1; i <= NF; i++) if ($i == "B/op") print $(i - 1) }' <<<"$line")"
test -n "$ns" && test -n "$bytes" || {
	echo "load benchmark output is missing ns/op or B/op" >&2
	echo "$line" >&2
	exit 1
}

awk -v ns="$ns" -v bytes="$bytes" 'BEGIN {
	if (ns > 5000000000) {
		printf "medium DAG load took %.0f ns, over the 5s ceiling\n", ns
		exit 1
	}
	if (bytes > 10000000) {
		printf "medium DAG load allocated %.0f bytes, over the 10MB ceiling\n", bytes
		exit 1
	}
	printf "medium DAG load: %.0f ns/op, %.0f B/op\n", ns, bytes
}'
